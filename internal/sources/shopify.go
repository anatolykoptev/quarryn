package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

const (
	// shopifyFetchTimeoutSecs bounds the wowa fetch of a shop's
	// products.json.
	shopifyFetchTimeoutSecs = 30
	// shopifyProductLimit is the per-request catalog page size — 250 is the
	// endpoint's documented max.
	shopifyProductLimit = 250
)

// shopifyAdapter reads the public products.json endpoint of each configured
// shop (SHOPIFY_SHOPS). Every Shopify storefront exposes it — no key needed.
// Third-party traffic goes through go-wowa Fetch (ADR-1). Ships dark when
// no shops are configured or no Fetcher is wired.
type shopifyAdapter struct {
	fetch       Fetcher
	shops       []string
	timeoutSecs int
	// profileURL is the hosted UCP agent profile (SHOPIFY_UCP_PROFILE_URL);
	// empty disables both UCP legs.
	profileURL string
	// globalURL is the Global Catalog MCP endpoint (SHOPIFY_UCP_GLOBAL_URL,
	// empty → ucpGlobalCatalogURL).
	globalURL string
	// globalOff disables the global-catalog leg (SHOPIFY_UCP_GLOBAL=0).
	globalOff bool
}

// ShopifyConfig carries the adapter's knobs. Shops are bare shop domains
// (scheme/slashes stripped). ProfileURL enables the UCP MCP legs; empty
// ships the adapter dark unless shops are configured.
type ShopifyConfig struct {
	Shops      []string // SHOPIFY_SHOPS
	ProfileURL string   // SHOPIFY_UCP_PROFILE_URL
	GlobalURL  string   // SHOPIFY_UCP_GLOBAL_URL override
	GlobalOff  bool     // SHOPIFY_UCP_GLOBAL=0 disables the global-catalog leg
}

// NewShopify returns the generic Shopify adapter.
func NewShopify(fetcher Fetcher, cfg ShopifyConfig) Adapter {
	clean := make([]string, 0, len(cfg.Shops))
	for _, s := range cfg.Shops {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimRight(s, "/")
		if s != "" {
			clean = append(clean, s)
		}
	}
	global := strings.TrimSpace(cfg.GlobalURL)
	if global == "" {
		global = ucpGlobalCatalogURL
	}
	return &shopifyAdapter{
		fetch:       fetcher,
		shops:       clean,
		timeoutSecs: shopifyFetchTimeoutSecs,
		profileURL:  strings.TrimSpace(cfg.ProfileURL),
		globalURL:   global,
		globalOff:   cfg.GlobalOff,
	}
}

// Name implements sources.Source.
func (a *shopifyAdapter) Name() string { return "shopify" }

// Spec implements Adapter.
func (a *shopifyAdapter) Spec() SourceSpec {
	return SourceSpec{
		FetchClass: FetchClassFetch,
		// Storefronts live on arbitrary custom domains (mobilizephone.com,
		// malbon.com) plus *.myshopify.com and the global catalog — the
		// host set is genuinely open, so the manifest declares it.
		Manifest: Manifest{ID: a.Name(), AllowedHosts: []string{"*"}},
	}
}

// Enabled implements Adapter — needs a wowa fetcher and at least one shop,
// or the UCP agent profile (which alone unlocks the global catalog).
func (a *shopifyAdapter) Enabled() bool {
	return a.fetch != nil && (len(a.shops) > 0 || a.ucpEnabled())
}

// ucpEnabled reports whether the UCP MCP legs are armed.
func (a *shopifyAdapter) ucpEnabled() bool { return a.profileURL != "" }

// shopifyProductsResponse models GET /products.json.
type shopifyProductsResponse struct {
	Products []shopifyProduct `json:"products"`
}

type shopifyProduct struct {
	ID          int64            `json:"id"`
	Title       string           `json:"title"`
	Handle      string           `json:"handle"`
	Vendor      string           `json:"vendor"`
	ProductType string           `json:"product_type"`
	Tags        []string         `json:"tags"`
	Variants    []shopifyVariant `json:"variants"`
	Images      []struct {
		Src string `json:"src"`
	} `json:"images"`
}

type shopifyVariant struct {
	ID             int64   `json:"id"`
	Title          string  `json:"title"`
	Price          string  `json:"price"`
	CompareAtPrice *string `json:"compare_at_price"`
	Available      bool    `json:"available"`
	SKU            string  `json:"sku"`
}

// Search queries the Shopify Global Catalog (when the UCP profile is
// configured) and each configured shop — UCP search_catalog first,
// products.json as fallback. A failing leg is logged and skipped — the
// adapter only errors when every leg fails.
func (a *shopifyAdapter) Search(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("shopify: adapter disabled (no wowa fetcher, SHOPIFY_SHOPS empty, no SHOPIFY_UCP_PROFILE_URL)")
	}
	var out []sources.Result
	var lastErr error
	if a.ucpEnabled() && !a.globalOff {
		res, err := a.searchGlobal(ctx, q)
		if err != nil {
			lastErr = err
			slog.Warn("shopify: global catalog search failed", slog.Any("error", err))
		} else {
			out = append(out, res...)
		}
	}
	for _, shop := range a.shops {
		res, err := a.searchShop(ctx, shop, q)
		if err != nil {
			lastErr = err
			continue // per-shop failure must not sink the whole adapter
		}
		out = append(out, res...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// searchGlobal queries Shopify's Global Catalog — cross-store relevance
// search with seller identity on every variant.
func (a *shopifyAdapter) searchGlobal(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	products, err := a.ucpSearch(ctx, a.globalURL, q.Text, ucpGlobalLimit)
	if err != nil {
		return nil, err
	}
	out := make([]sources.Result, 0, len(products))
	for _, p := range products {
		if r, ok := ucpProductToResult("", p); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// searchShop queries one shop's catalog: UCP search_catalog when the
// profile is configured, products.json as fallback (UCP failure or leg
// dark — storefronts without UCP still serve products.json).
func (a *shopifyAdapter) searchShop(ctx context.Context, shop string, q sources.Query) ([]sources.Result, error) {
	if a.ucpEnabled() {
		products, err := a.ucpSearch(ctx, "https://"+shop+"/api/ucp/mcp", q.Text, ucpShopLimit)
		if err == nil {
			out := make([]sources.Result, 0, len(products))
			for _, p := range products {
				if r, ok := ucpProductToResult(shop, p); ok {
					out = append(out, r)
				}
			}
			return out, nil
		}
		slog.Info("shopify: ucp leg failed, falling back to products.json",
			slog.String("shop", shop), slog.Any("error", err))
	}
	u := "https://" + shop + "/products.json?limit=" + strconv.Itoa(shopifyProductLimit)
	resp, err := a.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         u,
		TimeoutSecs: a.timeoutSecs,
	})
	if err != nil {
		return nil, fmt.Errorf("shopify %s: wowa fetch: %w", shop, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("shopify %s: upstream status %d", shop, resp.Status)
	}

	var body shopifyProductsResponse
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		return nil, fmt.Errorf("shopify %s: parse products.json: %w", shop, err)
	}

	out := make([]sources.Result, 0, len(body.Products))
	for _, p := range body.Products {
		if !shopifyMatches(p, q.Text) {
			continue
		}
		out = append(out, shopifyResult(shop, p))
	}
	return out, nil
}

// shopifyMatches applies the client-side query filter — products.json has
// no server-side text search.
func shopifyMatches(p shopifyProduct, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return true
	}
	// products.json has no server-side search — AND-match every query token
	// against the combined searchable text ("JBL speaker" must hit
	// "JBL Charge 6 ... Bluetooth Speaker", not the literal substring).
	hay := strings.ToLower(p.Title + " " + p.Vendor + " " + p.ProductType + " " + strings.Join(p.Tags, " "))
	for tok := range strings.FieldsSeq(needle) {
		if !strings.Contains(hay, tok) {
			return false
		}
	}
	return true
}

// shopifyResult maps one product to a Result: price = lowest in-stock
// variant price (falling back to lowest listed), discount_pct when
// compare_at_price exceeds price.
func shopifyResult(shop string, p shopifyProduct) sources.Result {
	price, discountPct, inStock := shopifyPrice(p)

	md := map[string]string{
		MetaListingID: strconv.FormatInt(p.ID, 10),
		MetaSeller:    shop,
	}
	if price != "" {
		md[MetaPrice] = price
	}
	if discountPct != "" {
		md[MetaDiscountPct] = discountPct
	}
	if inStock {
		md[MetaAvailability] = AvailabilityInStock
	} else {
		md[MetaAvailability] = AvailabilityOutOfStock
	}
	if p.Vendor != "" {
		md[MetaMerchant] = p.Vendor
	}
	if len(p.Images) > 0 && p.Images[0].Src != "" {
		md[MetaImageURL] = p.Images[0].Src
	}
	if sku := distinctVariantSKU(p.Variants); sku != "" {
		md[MetaSKU] = sku
	}
	if enc := EncodeVariants(shopifyVariants(shop, p)); enc != "" {
		md[MetaVariants] = enc
	}
	return sources.Result{
		Title:    p.Title,
		URL:      "https://" + shop + "/products/" + p.Handle,
		Content:  p.Title,
		Metadata: md,
	}
}

// distinctVariantSKU returns the merchant SKU only when it names the whole
// product — every variant carrying the identical non-empty code. A variant
// without a code, or any divergence, means per-configuration identity:
// promoting it would group the whole listing under one config's SKU and
// let best_offer crown a different configuration's price (issue #98).
func distinctVariantSKU(vs []shopifyVariant) string {
	if len(vs) == 0 {
		return ""
	}
	sku := strings.TrimSpace(vs[0].SKU)
	if sku == "" {
		return ""
	}
	for _, v := range vs[1:] {
		if strings.TrimSpace(v.SKU) != sku {
			return ""
		}
	}
	return sku
}

// shopifyVariants maps the products.json variant list onto the wire
// Variant shape — option label, price and availability ride through so a
// configurator listing's real configurations reach the judge (issue #115).
func shopifyVariants(shop string, p shopifyProduct) []Variant {
	if len(p.Variants) == 0 {
		return nil
	}
	out := make([]Variant, 0, len(p.Variants))
	for _, v := range p.Variants {
		if v.Title == "" || v.Title == "Default Title" {
			continue // single-variant listing — nothing to disambiguate
		}
		av := v.Available
		out = append(out, Variant{
			Title:     v.Title,
			VariantID: strconv.FormatInt(v.ID, 10),
			Price:     v.Price,
			Available: &av,
			URL:       "https://" + shop + "/products/" + p.Handle + "?variant=" + strconv.FormatInt(v.ID, 10),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// shopifyPrice picks the lowest in-stock variant price (falling back to the
// lowest price across all variants) and derives discount_pct from the
// highest compare_at_price that exceeds it.
func shopifyPrice(p shopifyProduct) (price, discountPct string, inStock bool) {
	best, bestAny, inStock := shopifyVariantPrices(p.Variants)
	if best < 0 {
		best = bestAny
	}
	if best < 0 {
		return "", "", inStock
	}
	price = strconv.FormatFloat(best, 'f', -1, 64)
	if mc := shopifyMaxCompareAt(p.Variants); mc > best && best > 0 {
		discountPct = strconv.FormatFloat((mc-best)/mc*100, 'f', 1, 64)
	}
	return price, discountPct, inStock
}

// shopifyVariantPrices scans variants once, returning the lowest in-stock
// price, the lowest price overall, and whether anything is in stock.
func shopifyVariantPrices(variants []shopifyVariant) (best, bestAny float64, inStock bool) {
	best, bestAny = -1.0, -1.0
	for _, v := range variants {
		pf, err := strconv.ParseFloat(v.Price, 64)
		if err != nil || pf < 0 {
			continue
		}
		if bestAny < 0 || pf < bestAny {
			bestAny = pf
		}
		if v.Available {
			inStock = true
			if best < 0 || pf < best {
				best = pf
			}
		}
	}
	return best, bestAny, inStock
}

// shopifyMaxCompareAt returns the highest compare_at_price across variants.
func shopifyMaxCompareAt(variants []shopifyVariant) float64 {
	maxCompare := 0.0
	for _, v := range variants {
		if v.CompareAtPrice == nil {
			continue
		}
		if cf, err := strconv.ParseFloat(*v.CompareAtPrice, 64); err == nil && cf > maxCompare {
			maxCompare = cf
		}
	}
	return maxCompare
}
