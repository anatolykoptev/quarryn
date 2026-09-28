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
	if err != nil || resp.Status != 200 {
		return
	}
	p.shopifyJSMerge(prod, resp.Body)
}

// shopifyJSMerge folds a .js payload into prod: the variant matrix, plus
// name/price/availability when still missing — this is also the rescue for
// shopify pages whose HTML carries no schema.org Product (the endpoint
// answers even when the storefront page is thin or challenge-walled).
func (p *Pipeline) shopifyJSMerge(prod *Product, body string) {
	var jp shopifyJSProduct
	if err := json.Unmarshal([]byte(body), &jp); err != nil {
		return
	}
	variants, best, anyStock := shopifyJSVariants(prod.URL, jp.Variants)
	if len(variants) == 0 {
		return
	}
	prod.Variants = variants
	if strings.TrimSpace(prod.Name) == "" && jp.Title != "" {
		prod.Name = jp.Title
	}
	if prod.PriceMinor == nil && best != nil {
		prod.PriceMinor = best
	}
	// The .js mirror knows the full stock state — an all-unavailable
	// listing must report out_of_stock, not silently leave "" (unpinned
	// restock watches and availability filters key on it).
	if prod.Availability == "" {
		if anyStock {
			prod.Availability = "in_stock"
		} else {
			prod.Availability = "out_of_stock"
		}
	}
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
