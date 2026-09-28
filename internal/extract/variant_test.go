package extract

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

// routerFetcher serves canned responses keyed by URL — the .js rescue
// must hit /products/<handle>.js while the detail fetch takes the page.
// A URL in `once` is consumed a single time (first call), then falls
// back to pages/404 — lets a test flip an endpoint between Enrich runs.
type routerFetcher struct {
	mu    sync.Mutex
	seen  []string
	pages map[string]*wowa.FetchResponse
	once  map[string]*wowa.FetchResponse
}

func (r *routerFetcher) Fetch(_ context.Context, req wowa.FetchRequest) (*wowa.FetchResponse, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.URL)
	if resp, ok := r.once[req.URL]; ok {
		delete(r.once, req.URL)
		r.mu.Unlock()
		return resp, nil
	}
	r.mu.Unlock()
	if resp, ok := r.pages[req.URL]; ok {
		return resp, nil
	}
	return &wowa.FetchResponse{Status: 404, Body: "not found"}, nil
}

func (r *routerFetcher) count(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.seen {
		if strings.Contains(u, substr) {
			n++
		}
	}
	return n
}

// shopifyJSBody mirrors a live /products/<handle>.js payload (minor-unit
// integer prices, option titles, per-variant availability).
const shopifyJSBody = `{
  "id": 8123456789, "title": "MacBook Pro 14 (M5 Pro)", "vendor": "expercom",
  "variants": [
    {"id": 5001, "title": "15C/16G / 24GB / 512GB", "price": 238900, "available": true},
    {"id": 5002, "title": "18C/20G / 64GB / 1TB", "price": 352900, "available": true},
    {"id": 5003, "title": "18C/20G / 64GB / 4TB", "price": 472900, "available": false}
  ]
}`

// shopifyPageHTML carries the storefront markers looksShopifyPage keys
// on, and deliberately no schema.org Product — the .js mirror rescues it.
const shopifyPageHTML = `<!doctype html><html><head>
<script src="https://cdn.shopify.com/s/assets/app.js"></script>
</head><body><h1>MacBook Pro 14</h1></body></html>`

// shopifySchemaHTML carries the storefront marker AND a schema.org
// Product — the page validates and caches even when .js fails.
const shopifySchemaHTML = `<!doctype html><html><head>
<script src="https://cdn.shopify.com/s/assets/app.js"></script>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product","name":"Cached Widget",
 "offers":{"@type":"Offer","price":"19.99","priceCurrency":"USD",
  "availability":"https://schema.org/InStock"}}
</script></head><body><h1>Cached Widget</h1></body></html>`

func TestShopifyJSURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://s.example.com/products/widget", "https://s.example.com/products/widget.js"},
		{"https://s.example.com/products/widget/", "https://s.example.com/products/widget.js"},
		{"https://s.example.com/products/widget.js", "https://s.example.com/products/widget.js"},
		{"https://s.example.com/products/widget?variant=9", "https://s.example.com/products/widget.js"},
		{"https://s.example.com/collections/all", ""},
		{"https://s.example.com/products/a/b", ""},
		{"https://s.example.com/", ""},
		{"not a url", ""},
	} {
		if got := shopifyJSURL(tc.in); got != tc.want {
			t.Errorf("shopifyJSURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The rescue must fire exactly once per candidate even when the .js
// answer carries no usable variants — a second fetch is wasted budget.
func TestShopifyJSRescueFetchesOnce(t *testing.T) {
	page := "https://shop.example.com/products/widget"
	f := &routerFetcher{pages: map[string]*wowa.FetchResponse{
		page:         {Status: 200, Body: shopifyPageHTML},
		page + ".js": {Status: 200, Body: `{"title":"x","variants":[]}`},
	}}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "shopify", Title: "Widget", URL: page},
	})
	if f.count(".js") != 1 {
		t.Fatalf(".js fetches = %d, want 1 (jsTried dedupe)", f.count(".js"))
	}
	if len(out[0].Product.Variants) != 0 {
		t.Fatalf("empty variants payload must leave Variants empty: %+v", out[0].Product.Variants)
	}
}

// A SERP-complete shopify candidate still gets the matrix — the .js fetch
// is the only place option labels exist when the UCP leg ran.
func TestShopifyJSRescueOnSerpComplete(t *testing.T) {
	page := "https://shop.example.com/products/mbp"
	f := &routerFetcher{pages: map[string]*wowa.FetchResponse{
		page + ".js": {Status: 200, Body: shopifyJSBody},
	}}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "shopify", Title: "MacBook Pro 14", URL: page,
			PriceMinor: iminor(238900), Currency: "USD"},
	})
	if f.count(".js") != 1 || len(f.seen) != 1 {
		t.Fatalf("fetches = %v — want only the .js call", f.seen)
	}
	vs := out[0].Product.Variants
	if len(vs) != 3 || vs[1].VariantID != "5002" || vs[1].Price != "3529" {
		t.Fatalf("variants = %+v", vs)
	}
	if !strings.HasSuffix(vs[1].URL, "?variant=5002") {
		t.Fatalf("variant deep link wrong: %q", vs[1].URL)
	}
}

// A non-shopify page must never trigger the .js probe — the rescue is
// gated on storefront evidence, not on "page might be a product".
func TestShopifyJSRescueSkipsForeignPages(t *testing.T) {
	page := "https://plain.example.com/products/widget"
	f := &routerFetcher{pages: map[string]*wowa.FetchResponse{
		page: {Status: 200, Body: "<html><body>plain</body></html>"},
	}}
	p := New(f, nil, testConfig())
	p.Enrich(t.Context(), []sources.Candidate{
		{Source: "web", Title: "Widget", URL: page},
	})
	if f.count(".js") != 0 {
		t.Fatalf(".js fetched for a non-shopify page: %v", f.seen)
	}
}

func TestLooksShopifyPage(t *testing.T) {
	if !looksShopifyPage(shopifyPageHTML) {
		t.Fatal("cdn.shopify.com marker missed")
	}
	if looksShopifyPage("<html><body>plain</body></html>") {
		t.Fatal("plain page flagged as shopify")
	}
}

// publicVariants is the egress boundary — ordering, the cap and title
// sanitization all live here.
func TestPublicVariantsProjection(t *testing.T) {
	in := make([]sources.Variant, 0, PublicVariantMax+5)
	in = append(in, sources.Variant{Title: "oos", Available: boolp(false)})
	for i := 0; i < PublicVariantMax+4; i++ {
		in = append(in, sources.Variant{Title: strings.Repeat("x", 400) + "\nforged", Available: boolp(true)})
	}
	out := publicVariants(in)
	if len(out) != PublicVariantMax {
		t.Fatalf("len = %d, want %d", len(out), PublicVariantMax)
	}
	// In-stock head means the single OOS entry is clipped away entirely.
	for _, v := range out {
		if v.Title == "oos" {
			t.Fatal("out-of-stock variant leaked into the capped head")
		}
		if strings.ContainsAny(v.Title, "\r\n") {
			t.Fatalf("control char survived sanitize: %q", v.Title)
		}
	}
	if out[0].Title == "" {
		t.Fatal("empty projected title")
	}
}

// An all-unavailable .js matrix must stamp out_of_stock — leaving ""
// hides the listing from availability filters and restock watches.
func TestShopifyJSMergeAllUnavailable(t *testing.T) {
	prod := &Product{URL: "https://s.example.com/products/p"}
	p := New(nil, nil, testConfig())
	p.shopifyJSMerge(prod, `{"title":"T","variants":[
		{"id":1,"title":"64GB","price":352900,"available":false}]}`)
	if prod.Availability != "out_of_stock" {
		t.Fatalf("availability = %q, want out_of_stock", prod.Availability)
	}
}

// A variant-less cache hit still earns one rescue attempt — a pinned
// watch must not ride out the 24h TTL reporting no_offers (Review #116).
// Setup: first Enrich caches a valid product while .js 404s; the second
// run serves the cache, then the rescue must still try .js once more.
func TestShopifyJSRescueOnCacheHit(t *testing.T) {
	page := "https://shop.example.com/products/cached"
	f := &routerFetcher{
		pages: map[string]*wowa.FetchResponse{
			page:         {Status: 200, Body: shopifySchemaHTML},
			page + ".js": {Status: 200, Body: shopifyJSBody},
		},
		once: map[string]*wowa.FetchResponse{
			page + ".js": {Status: 404, Body: "gone"},
		},
	}
	cfg := testConfig()
	cfg.Cache = cache.New(cache.Config{L1MaxItems: 16})
	t.Cleanup(cfg.Cache.Close)
	p := New(f, nil, cfg)
	cand := sources.Candidate{Source: "shopify", Title: "", URL: page}
	out := p.Enrich(t.Context(), []sources.Candidate{cand})
	if len(out[0].Product.Variants) != 0 {
		t.Fatalf("first run unexpectedly had variants: %+v", out[0].Product.Variants)
	}
	out2 := p.Enrich(t.Context(), []sources.Candidate{cand})
	if f.count(".js") != 2 {
		t.Fatalf(".js fetches = %d, want 2 (once per Enrich)", f.count(".js"))
	}
	if len(out2[0].Product.Variants) != 3 {
		t.Fatalf("cached product never received its variant matrix: %+v", out2[0].Product.Variants)
	}
}

func boolp(v bool) *bool { return &v }
