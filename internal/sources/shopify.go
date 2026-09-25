package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
}

// NewShopify returns the generic Shopify adapter. shops are bare shop
// domains (scheme/slashes stripped). Empty list ships the adapter dark.
func NewShopify(fetcher Fetcher, shops []string) Adapter {
	clean := make([]string, 0, len(shops))
	for _, s := range shops {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimRight(s, "/")
		if s != "" {
			clean = append(clean, s)
		}
	}
	return &shopifyAdapter{
		fetch:       fetcher,
		shops:       clean,
		timeoutSecs: shopifyFetchTimeoutSecs,
	}
}

// Name implements sources.Source.
func (a *shopifyAdapter) Name() string { return "shopify" }

// Spec implements Adapter.
func (a *shopifyAdapter) Spec() SourceSpec { return SourceSpec{FetchClass: FetchClassFetch} }

// Enabled implements Adapter — needs a wowa fetcher and at least one shop.
func (a *shopifyAdapter) Enabled() bool {
	return a.fetch != nil && len(a.shops) > 0
}

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

// Search fetches products.json from each configured shop and returns the
// products matching the query (case-insensitive substring over title,
// vendor, product_type, tags; empty query matches all). A failing shop is
// logged and skipped — the adapter only errors when every shop fails.
func (a *shopifyAdapter) Search(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("shopify: adapter disabled (no wowa fetcher or SHOPIFY_SHOPS empty)")
	}
	var out []sources.Result
	var lastErr error
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

// searchShop fetches and maps one shop's catalog.
func (a *shopifyAdapter) searchShop(ctx context.Context, shop string, q sources.Query) ([]sources.Result, error) {
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
	if strings.Contains(strings.ToLower(p.Title), needle) ||
		strings.Contains(strings.ToLower(p.Vendor), needle) ||
		strings.Contains(strings.ToLower(p.ProductType), needle) {
		return true
	}
	return slices.ContainsFunc(p.Tags, func(t string) bool {
		return strings.Contains(strings.ToLower(t), needle)
	})
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
	return sources.Result{
		Title:    p.Title,
		URL:      "https://" + shop + "/products/" + p.Handle,
		Content:  p.Title,
		Metadata: md,
	}
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
