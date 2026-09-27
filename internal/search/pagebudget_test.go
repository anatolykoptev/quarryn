package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/config"
)

// rssThreePriceless is a slickdeals-shaped feed whose items carry no price
// — every one needs a detail fetch. Links are TEST-NET literals so the
// funnel SSRF guard passes without DNS.
const rssThreePriceless = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel><title>Slickdeals</title>
    <item>
      <title>Acme 4K Monitor — hot deal</title>
      <link>http://203.0.113.50/deal-a</link>
      <description><![CDATA[Merchant: Acme]]></description>
    </item>
    <item>
      <title>Gamma Mech Keyboard — hot deal</title>
      <link>http://203.0.113.51/deal-b</link>
      <description><![CDATA[Merchant: Gamma]]></description>
    </item>
    <item>
      <title>Delta USB-C Hub — hot deal</title>
      <link>http://203.0.113.52/deal-c</link>
      <description><![CDATA[Merchant: Delta]]></description>
    </item>
  </channel>
</rss>`

// TestSearchPageBudgetStopsDetailFetches — MAX_PAGES_PER_SEARCH counts the
// SERP feed fetch AND the detail fetches: with 3 price-less candidates and
// a cap of 2, one detail page is fetched, the rest rank on SERP data.
func TestSearchPageBudgetStopsDetailFetches(t *testing.T) {
	clearSourceEnv(t)
	var fetches atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		var req wowa.FetchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		fetches.Add(1)
		body := rssThreePriceless
		if strings.Contains(req.URL, "deal-") {
			body = detailPageHTML
		}
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: 200, Body: body})
	}))
	t.Cleanup(srv.Close)

	// Budget 2 = 1 serp feed fetch + 1 detail fetch.
	s, err := New(config.Config{WowaURL: srv.URL, MaxPagesPerSearch: 2}, allowAllHosts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := s.SearchDetailed(t.Context(), "deal", nil, 10)
	if err != nil {
		t.Fatalf("SearchDetailed: %v", err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("page budget breached: wowa fetches = %d, want 2", fetches.Load())
	}
	if len(out.Candidates) != 3 {
		t.Fatalf("budget cap must keep all candidates: %+v", out.Candidates)
	}
	var enriched, unenriched int
	for _, c := range out.Candidates {
		if c.Product.Method == "schema" && !c.ExtractionFailed {
			enriched++
		} else {
			unenriched++
		}
	}
	if enriched != 1 || unenriched != 2 {
		t.Fatalf("want 1 detail-enriched + 2 serp-only, got %d/%d", enriched, unenriched)
	}
}

// TestSearchNoPageBudgetIsUnbounded — a zero-value MaxPagesPerSearch
// attaches no budget: every detail fetch runs (pre-P6 behaviour).
func TestSearchNoPageBudgetIsUnbounded(t *testing.T) {
	clearSourceEnv(t)
	var fetches atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		fetches.Add(1)
		var req wowa.FetchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		body := rssThreePriceless
		if strings.Contains(req.URL, "deal-") {
			body = detailPageHTML
		}
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: 200, Body: body})
	}))
	t.Cleanup(srv.Close)

	s, err := New(config.Config{WowaURL: srv.URL}, allowAllHosts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := s.SearchDetailed(t.Context(), "deal", nil, 10)
	if err != nil {
		t.Fatalf("SearchDetailed: %v", err)
	}
	// 1 serp + 3 detail fetches.
	if fetches.Load() != 4 {
		t.Fatalf("unbounded run fetched %d pages, want 4", fetches.Load())
	}
	if len(out.Candidates) != 3 {
		t.Fatalf("candidates = %+v", out.Candidates)
	}
}
