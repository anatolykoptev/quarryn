package extract

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

// Shopify storefronts expose a deterministic JSON mirror of every product
// page at /products/<handle>.js — full variant matrix (option labels,
// minor-unit prices, availability), no HTML scraping required. This file
// is the rescue path for candidates whose adapter leg could not carry
// variants (the UCP catalog has no option titles) and for direct
// product_match/watch URLs on shopify storefronts (issue #115).

// shopifyJSRe matches the product-page path shape: /products/<handle>
// (optionally already suffixed .js/.json). Collections and other paths
// return "" — the endpoint exists only under /products/.
func shopifyJSURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	path := strings.TrimSuffix(u.Path, "/")
	path = strings.TrimSuffix(strings.TrimSuffix(path, ".js"), ".json")
	if !strings.HasPrefix(path, "/products/") || strings.Count(path, "/") != 2 {
		return ""
	}
	return u.Scheme + "://" + u.Host + path + ".js"
}

// shopifySuspected is the cheap pre-fetch gate for the .js probe: the
// adapter source, or the canonical /products/<handle> path shape that
// only Shopify storefronts (and a rare false positive) carry.
func shopifySuspected(c sources.Candidate) bool {
	return c.Source == "shopify" || shopifyJSURL(c.URL) != ""
}

// looksShopifyPage detects a Shopify storefront from the fetched HTML —
// the CDN asset host and the Shopify global are both load-bearing marks a
// non-shopify page does not produce.
func looksShopifyPage(body string) bool {
	return strings.Contains(body, "cdn.shopify.com") ||
		strings.Contains(body, "window.Shopify") ||
		strings.Contains(body, "Shopify.theme")
}

// shopifyJSProduct models the /products/<handle>.js payload — variant
// prices arrive as integer minor units ("price":238900 = $2389.00),
// unlike products.json where they are decimal strings.
type shopifyJSProduct struct {
	Title       string             `json:"title"`
	Vendor      string             `json:"vendor"`
	Description string             `json:"description"` // HTML — kept off egress fields
	Available   bool               `json:"available"`
	PriceMin    int64              `json:"price_min"` // minor units, like variant prices
	Variants    []shopifyJSVariant `json:"variants"`
}

type shopifyJSVariant struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	SKU       string `json:"sku"`
	Price     int64  `json:"price"`
	Available bool   `json:"available"`
}

// tryShopifyVariants fetches the .js mirror when suspected is true and the
// product still lacks a variant matrix. It shares the per-Enrich detail
// budget — a refused spend leaves the candidate SERP-level, exactly like a
// refused page fetch. Best-effort: failures are silent by design because
// the caller's outcome vocabulary has no variant-specific disposition.
func (p *Pipeline) tryShopifyVariants(ctx context.Context, c sources.Candidate, prod *Product, budget *atomic.Int64, suspected bool) {
	if !suspected || len(prod.Variants) > 0 || prod.jsTried || p.fetch == nil {
		return
	}
	jsURL := shopifyJSURL(c.URL)
	if jsURL == "" || budget.Add(-1) < 0 {
		return
	}
	prod.jsTried = true // attempted once — a variant-less answer is final
	resp, err := p.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         jsURL,
		TimeoutSecs: p.cfg.FetchTimeoutSecs,
	})
	if err != nil {
		return
	}
	// Shopify normalizes storefront hosts (www→apex): wowa returns the
	// 301 verbatim, so follow the one hop — but only while the Location
	// still resolves to a /products/<handle>.js shape (bounds the hop to
	// the same endpoint family, never an arbitrary target).
	if resp.Status >= 300 && resp.Status < 400 {
		if next := redirectShopifyJS(jsURL, headerLocation(resp.Headers)); next != "" {
			if r2, err := p.fetch.Fetch(ctx, wowa.FetchRequest{
				URL:         next,
				TimeoutSecs: p.cfg.FetchTimeoutSecs,
			}); err == nil {
				resp = r2
				jsURL = next
			}
		}
	}
	if resp.Status != 200 {
		return
	}
	if p.shopifyJSMerge(prod, resp.Body) {
		p.tryShopifyCurrency(ctx, jsURL, prod, budget)
	}
}

// tryShopifyCurrency fills the store-level field the product doc does not
// carry: /cart.js answers {"currency":"USD",…} on every storefront. Runs
// only when the merge left Currency empty — a page/schema fill already
// knows it, and a dead page means the probe pays for itself.
func (p *Pipeline) tryShopifyCurrency(ctx context.Context, jsURL string, prod *Product, budget *atomic.Int64) {
	if prod.Currency != "" || budget.Add(-1) < 0 {
		return
	}
	u, err := url.Parse(jsURL)
	if err != nil || u.Host == "" {
		return
	}
	resp, err := p.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         u.Scheme + "://" + u.Host + "/cart.js",
		TimeoutSecs: p.cfg.FetchTimeoutSecs,
	})
	if err != nil || resp.Status != 200 {
		return
	}
	var cart struct {
		Currency string `json:"currency"`
	}
	if json.Unmarshal([]byte(resp.Body), &cart) == nil {
		prod.Currency = strings.ToUpper(strings.TrimSpace(cart.Currency))
	}
}

// headerLocation reads the redirect target case-insensitively — the
// upstream header map's key casing is a wowa implementation detail.
func headerLocation(h map[string]string) string {
	for k, v := range h {
		if strings.EqualFold(k, "location") {
			return v
		}
	}
	return ""
}

// redirectShopifyJS resolves a redirect target for the .js probe and
// refuses anything that is not a same-storefront product mirror. The
// host must match modulo www (shopify's canonical-host normalization);
// the path must keep the /products/<handle>.js shape. Both checks
// together bound the hop: a Location pointing at a private IP or a
// different endpoint family is dropped.
func redirectShopifyJS(from, loc string) string {
	next := shopifyJSURL(resolveLocation(from, loc))
	if next == "" || next == from {
		return ""
	}
	src, err1 := url.Parse(from)
	dst, err2 := url.Parse(next)
	if err1 != nil || err2 != nil || dst.Host == "" {
		return ""
	}
	strip := func(h string) string { return strings.TrimPrefix(strings.ToLower(h), "www.") }
	if strip(src.Host) != strip(dst.Host) {
		return ""
	}
	return next
}

// resolveLocation resolves a possibly-relative Location target against
// the URL that produced it.
func resolveLocation(from, loc string) string {
	loc = strings.TrimSpace(loc)
	u, err := url.Parse(loc)
	if err != nil || loc == "" {
		return ""
	}
	if u.IsAbs() {
		return loc
	}
	base, err := url.Parse(from)
	if err != nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

// shopifyJSMerge folds a .js payload into prod: the variant matrix, plus
// name/price/availability when still missing — this is also the rescue for
// shopify pages whose HTML carries no schema.org Product (the endpoint
// answers even when the storefront page is thin or challenge-walled).
// Single-variant products ship as "Default Title" — filtered from the
// matrix but still carrying the product's price/stock in the top-level
// fields. Returns whether the payload was a real product doc at all —
// an empty {} is not worth a currency probe.
func (p *Pipeline) shopifyJSMerge(prod *Product, body string) bool {
	var jp shopifyJSProduct
	if err := json.Unmarshal([]byte(body), &jp); err != nil {
		return false
	}
	variants, best, anyStock := shopifyJSVariants(prod.URL, jp.Variants)
	if jp.Title == "" && len(variants) == 0 {
		return false
	}
	if len(variants) > 0 {
		prod.Variants = variants
	}
	if shopifyJSFill(prod, &jp, best, anyStock) &&
		(prod.Method == MethodSERP || prod.Method == MethodLLM) {
		// The deterministic merchant doc outranks SERP metadata and an
		// LLM guess as provenance for the fields it filled.
		prod.Method = MethodShopifyJS
	}
	return true
}

// shopifyJSFill writes the product-level fields the .js doc carries,
// reporting whether anything landed — an already-complete product gets
// its variants but keeps its provenance.
func shopifyJSFill(prod *Product, jp *shopifyJSProduct, best *int64, anyStock bool) bool {
	contributed := len(prod.Variants) > 0 // caller just assigned them
	if strings.TrimSpace(prod.Name) == "" && jp.Title != "" {
		prod.Name = jp.Title
		contributed = true
	}
	if prod.PriceMinor == nil {
		// best = lowest in-stock variant price; price_min covers the
		// Default-Title shape and all-OOS matrices.
		if best == nil && jp.PriceMin > 0 {
			best = &jp.PriceMin
		}
		if best != nil {
			prod.PriceMinor = best
			contributed = true
		}
	}
	// The .js mirror knows the full stock state — an all-unavailable
	// listing must report out_of_stock, not silently leave "" (unpinned
	// restock watches and availability filters key on it).
	if prod.Availability == "" {
		if anyStock || jp.Available {
			prod.Availability = "in_stock"
		} else {
			prod.Availability = "out_of_stock"
		}
		contributed = true
	}
	return contributed
}

// shopifyJSVariants maps the .js variant list to wire form, skipping
// placeholder titles, and returns the lowest in-stock minor-unit price
// plus whether anything is orderable.
func shopifyJSVariants(pageURL string, vs []shopifyJSVariant) ([]sources.Variant, *int64, bool) {
	out := make([]sources.Variant, 0, len(vs))
	var best *int64
	anyStock := false
	for _, v := range vs {
		if v.Title == "" || v.Title == "Default Title" {
			continue
		}
		out = append(out, v.wire(pageURL))
		if v.Available {
			anyStock = true
			if best == nil || v.Price < *best {
				best = &v.Price
			}
		}
	}
	return out, best, anyStock
}

// wire projects one .js variant into the wire shape: minor-unit price to
// decimal string, deep link on the listing URL with the variant
// preselected.
func (v shopifyJSVariant) wire(pageURL string) sources.Variant {
	av := v.Available
	id := strconv.FormatInt(v.ID, 10)
	return sources.Variant{
		Title:     v.Title,
		VariantID: id,
		Price:     strconv.FormatFloat(float64(v.Price)/100, 'f', -1, 64),
		Available: &av,
		URL:       strings.Split(pageURL, "?")[0] + "?variant=" + id,
	}
}
