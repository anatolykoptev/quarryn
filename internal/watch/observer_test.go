package watch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/config"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/search"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

func variantProduct() *extract.Product {
	min := int64(238900)
	return &extract.Product{
		Name:         "MacBook Pro 14",
		URL:          "https://shop.example.com/products/mbp",
		PriceMinor:   &min,
		Currency:     "USD",
		Availability: "in_stock",
		Variants: []sources.Variant{
			{Title: "15C/16G / 24GB / 512GB", VariantID: "5001", Price: "2389.00",
				Available: boolRef(true), URL: "https://shop.example.com/products/mbp?variant=5001"},
			{Title: "18C/20G / 64GB / 1TB", VariantID: "5002", Price: "3529.00",
				Available: boolRef(false), URL: "https://shop.example.com/products/mbp?variant=5002"},
		},
	}
}

func boolRef(v bool) *bool { return &v }

// A pinned watch must observe ITS configuration — price and availability
// come from the matched variant, never the listing's min-price head.
func TestObserveVariantPinnedBySubstring(t *testing.T) {
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "64GB", Currency: "USD", OfferID: "url|x"}
	obs := observeVariant(w, variantProduct())
	if obs.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q (%s)", obs.Outcome, obs.Detail)
	}
	if obs.PriceMinor == nil || *obs.PriceMinor != 352900 {
		t.Fatalf("price = %v, want 352900 (the 64GB SKU, not the 2389 min)", obs.PriceMinor)
	}
	if obs.Availability != "out_of_stock" {
		t.Fatalf("availability = %q — the pinned variant's stock wins over the listing", obs.Availability)
	}
	if obs.OfferURL != "https://shop.example.com/products/mbp?variant=5002" {
		t.Fatalf("offer_url = %q", obs.OfferURL)
	}
}

func TestObserveVariantPinnedByID(t *testing.T) {
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "5001", Currency: "USD"}
	obs := observeVariant(w, variantProduct())
	if obs.Outcome != OutcomeOK || obs.PriceMinor == nil || *obs.PriceMinor != 238900 {
		t.Fatalf("id-pin failed: %+v", obs)
	}
	if obs.Availability != "in_stock" {
		t.Fatalf("availability = %q", obs.Availability)
	}
}

// Fail-closed is the contract: a selector matching nothing is a
// no_offers observation, never a silent watch of the wrong SKU.
func TestObserveVariantMissFailsClosed(t *testing.T) {
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "128GB", Currency: "USD"}
	obs := observeVariant(w, variantProduct())
	if obs.Outcome != OutcomeNoOffers {
		t.Fatalf("outcome = %q, want no_offers", obs.Outcome)
	}
	if obs.PriceMinor != nil {
		t.Fatalf("miss must not carry a price: %v", obs.PriceMinor)
	}
}

// A variant-less listing under a pinned watch fails closed the same way —
// the empty matrix has no member to follow.
func TestObserveVariantEmptyMatrix(t *testing.T) {
	w := Watch{URL: "https://s.example/p", VariantSel: "64GB"}
	obs := observeVariant(w, &extract.Product{Name: "n", Currency: "USD"})
	if obs.Outcome != OutcomeNoOffers {
		t.Fatalf("outcome = %q", obs.Outcome)
	}
}

// A variant without an availability flag must NOT inherit the listing's
// stock state — a sibling SKU in stock would false-fire a restock.
func TestObserveVariantUnknownStockStaysEmpty(t *testing.T) {
	p := variantProduct()
	p.Variants[0].Available = nil
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "5001", Currency: "USD"}
	obs := observeVariant(w, p)
	if obs.Availability != "" {
		t.Fatalf("availability = %q, want empty (listing state must not leak)", obs.Availability)
	}
}

// A watch URL carrying its own query must not grow a second "?".
func TestObserveVariantOfferURLQuerySafe(t *testing.T) {
	p := variantProduct()
	p.Variants[0].URL = "" // force the fallback branch
	w := Watch{URL: "https://shop.example.com/products/mbp?utm=x",
		VariantSel: "5001", Currency: "USD"}
	obs := observeVariant(w, p)
	if obs.OfferURL != "https://shop.example.com/products/mbp?variant=5001" {
		t.Fatalf("offer_url = %q", obs.OfferURL)
	}
}

// The condition evaluator receives the pinned configuration — the scoped
// product carries the variant's price/availability, never the listing's
// min-price head.
func TestObserveVariantScopesConditionProduct(t *testing.T) {
	p := variantProduct()
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "64GB", Currency: "USD"}
	obs := observeVariant(w, p)
	if obs.Product == nil || obs.Product == p {
		t.Fatal("condition product must be a scoped copy")
	}
	if obs.Product.PriceMinor == nil || *obs.Product.PriceMinor != 352900 {
		t.Fatalf("scoped price = %v, want 352900", obs.Product.PriceMinor)
	}
	if obs.Product.Availability != "out_of_stock" {
		t.Fatalf("scoped availability = %q", obs.Product.Availability)
	}
	// The parent stays untouched for other consumers of the extraction.
	if p.PriceMinor == nil || *p.PriceMinor != 238900 || len(p.Variants) != 2 {
		t.Fatal("parent product mutated by pinning")
	}
}

// A query watch must never alert on the 24h extraction cache's memory of
// a price (issue #120): the search leg caches the winner, then the live
// confirm re-reads the page and reports what it says NOW. The stub drops
// the price between the two reads — the observation must carry 199.99.
func TestObserveQueryConfirmsWinnerLive(t *testing.T) {
	for _, k := range []string{
		"EBAY_CLIENT_ID", "EBAY_CLIENT_SECRET",
		"ETSY_API_KEY", "ETSY_SHARED_SECRET",
		"SHOPIFY_SHOPS", "INTERNAL_SERVICE_SECRET", "REDIS_URL",
	} {
		t.Setenv(k, "")
	}
	var detailFetches atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		var req wowa.FetchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		body := rssDeal
		if strings.Contains(req.URL, "deal-9") {
			n := detailFetches.Add(1)
			price := "249.99"
			if n > 1 {
				price = "199.99" // the drop the alert must see
			}
			body = strings.ReplaceAll(dealPageHTML, "249.99", price)
		}
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: 200, Body: body})
	}))
	t.Cleanup(srv.Close)

	s, err := search.New(config.Config{WowaURL: srv.URL},
		sources.WithManifestGate(func(sources.Manifest, string) bool { return true }))
	if err != nil {
		t.Fatalf("search.New: %v", err)
	}
	obs := NewSearcherObserver(s).Observe(t.Context(), Watch{
		Kind: KindQuery, Query: "deal", Currency: "USD",
	})
	if obs.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q (%s)", obs.Outcome, obs.Detail)
	}
	if obs.PriceMinor == nil || *obs.PriceMinor != 19999 {
		t.Fatalf("price = %v — the live confirm must win over the cached 249.99", obs.PriceMinor)
	}
	if detailFetches.Load() != 2 {
		t.Fatalf("detail fetches = %d, want 2 (search + live confirm)", detailFetches.Load())
	}
}

// A cached price ordering can hide the live-cheapest offer (review on
// #120): the confirm leg must re-read the top few candidates, not only
// the cached winner. deal-a caches at 249.99 but is 399.99 live; deal-b
// was 299.99 cached, 219.99 live — the observation must pick deal-b.
func TestObserveQueryConfirmsReordered(t *testing.T) {
	for _, k := range []string{
		"EBAY_CLIENT_ID", "EBAY_CLIENT_SECRET",
		"ETSY_API_KEY", "ETSY_SHARED_SECRET",
		"SHOPIFY_SHOPS", "INTERNAL_SERVICE_SECRET", "REDIS_URL",
	} {
		t.Setenv(k, "")
	}
	var mu sync.Mutex
	perURL := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		var req wowa.FetchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		body := rssTwoDeals
		for _, key := range []string{"deal-a", "deal-b"} {
			if !strings.Contains(req.URL, key) {
				continue
			}
			mu.Lock()
			perURL[key]++ // first read = search leg, later = live confirm
			n := perURL[key]
			mu.Unlock()
			price := map[string][2]string{
				"deal-a": {"249.99", "399.99"},
				"deal-b": {"299.99", "219.99"},
			}[key][min(n-1, 1)]
			body = strings.ReplaceAll(dealPageHTML, "deal-9", key)
			body = strings.ReplaceAll(body, "249.99", price)
		}
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: 200, Body: body})
	}))
	t.Cleanup(srv.Close)

	s, err := search.New(config.Config{WowaURL: srv.URL},
		sources.WithManifestGate(func(sources.Manifest, string) bool { return true }))
	if err != nil {
		t.Fatalf("search.New: %v", err)
	}
	obs := NewSearcherObserver(s).Observe(t.Context(), Watch{
		Kind: KindQuery, Query: "deal", Currency: "USD",
	})
	if obs.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q (%s)", obs.Outcome, obs.Detail)
	}
	if obs.PriceMinor == nil || *obs.PriceMinor != 21999 {
		t.Fatalf("price = %v — must be deal-b's live 219.99, not the cached winner 249.99", obs.PriceMinor)
	}
	if !strings.Contains(obs.OfferURL, "deal-b") {
		t.Fatalf("offer_url = %q — must track the live-cheapest offer", obs.OfferURL)
	}
}

// rssTwoDeals feeds two price-less items; their detail pages fill the
// search-leg prices (deal-a 249.99, deal-b 299.99).
const rssTwoDeals = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel><title>Slickdeals</title>
    <item>
      <title>Acme 4K Monitor A — hot deal</title>
      <link>http://203.0.113.50/deal-a</link>
      <description><![CDATA[Merchant: Acme]]></description>
    </item>
    <item>
      <title>Acme 4K Monitor B — hot deal</title>
      <link>http://203.0.113.50/deal-b</link>
      <description><![CDATA[Merchant: Acme]]></description>
    </item>
  </channel>
</rss>`

// rssDeal + dealPageHTML mirror the search-package fixtures: a price-less
// SERP item whose detail page carries JSON-LD — the detail fetch fills
// (and caches) the product the confirm leg then re-reads.
const rssDeal = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel><title>Slickdeals</title>
    <item>
      <title>Acme 4K Monitor — hot deal</title>
      <link>http://203.0.113.50/deal-9</link>
      <description><![CDATA[Merchant: Acme]]></description>
    </item>
  </channel>
</rss>`

const dealPageHTML = `<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product",
 "name":"Acme 4K Monitor 27in",
 "url":"http://203.0.113.50/deal-9",
 "offers":{"@type":"Offer","price":"249.99","priceCurrency":"USD",
   "availability":"https://schema.org/InStock"}}
</script></head><body>monitor</body></html>`
