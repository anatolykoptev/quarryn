package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/config"
	"github.com/anatolykoptev/go-product-search/internal/extract"
	pssources "github.com/anatolykoptev/go-product-search/internal/sources"
)

// clearSourceEnv pins every adapter credential env var to empty so the test
// is hermetic regardless of the host's real environment.
func clearSourceEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EBAY_CLIENT_ID", "EBAY_CLIENT_SECRET",
		"ETSY_API_KEY", "ETSY_SHARED_SECRET",
		"SHOPIFY_SHOPS", "INTERNAL_SERVICE_SECRET",
		"REDIS_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestNewBuildsRegistry(t *testing.T) {
	clearSourceEnv(t)
	s, err := New(config.Config{WowaURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	status := s.AdapterStatus()
	if len(status) != 4 {
		t.Fatalf("adapters = %v", status)
	}
	// Only slickdeals is enabled (wowa fetcher wired, no creds needed);
	// ebay/etsy/shopify ship dark.
	if !status["slickdeals"] {
		t.Fatalf("slickdeals should be enabled with a wowa client: %v", status)
	}
	for _, dark := range []string{"ebay", "etsy", "shopify"} {
		if status[dark] {
			t.Fatalf("%s should be disabled without creds: %v", dark, status)
		}
	}
}

func TestSearchSurfacesAdapterFailure(t *testing.T) {
	clearSourceEnv(t)
	// Port 1 refuses instantly — the one enabled adapter (slickdeals) fails
	// its fetch, so the funnel reports the all-failed error path.
	s, err := New(config.Config{WowaURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = s.Search(t.Context(), "anything", nil)
	if err == nil {
		t.Fatal("Search must error when every enabled adapter fails")
	}
	out, outErr := s.SearchDetailed(t.Context(), "anything", nil, 5)
	if outErr == nil {
		t.Fatal("SearchDetailed must error when every enabled adapter fails")
	}
	found := false
	for _, st := range out.Sources {
		if st.Name == "slickdeals" && st.Outcome == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("slickdeals outcome missing/failed not reported: %+v", out.Sources)
	}
}

// rssWithoutPrice is a slickdeals-shaped feed whose item carries no
// "Price:" marker — the candidate lands with a title but no price, which
// must trigger the P3 detail-fetch tier. The link is a TEST-NET literal IP
// so the funnel's SSRF guard passes without DNS.
const rssWithoutPrice = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel><title>Slickdeals</title>
    <item>
      <title>Acme 4K Monitor — hot deal</title>
      <link>http://203.0.113.50/deal1</link>
      <description><![CDATA[Merchant: Acme<br/>Thumbs: 41]]></description>
    </item>
  </channel>
</rss>`

// detailPageHTML is the product page the extraction stage fetches for the
// price-less candidate: JSON-LD Product+Offer fills the gap.
const detailPageHTML = `<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product",
 "name":"Acme 4K Monitor 27in",
 "url":"http://203.0.113.50/deal1",
 "offers":{"@type":"Offer","price":"249.99","priceCurrency":"USD",
   "availability":"https://schema.org/InStock"}}
</script></head><body>monitor</body></html>`

// newWowaStub fakes go-wowa /api/v1/fetch: the feed URL answers the RSS
// fixture, any URL containing "deal1" answers the product detail page.
// Every fetched URL is recorded into the returned slice.
func newWowaStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	fetchedURLs := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		var req wowa.FetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		*fetchedURLs = append(*fetchedURLs, req.URL)
		body := rssWithoutPrice
		if strings.Contains(req.URL, "deal1") {
			body = detailPageHTML
		}
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: 200, Body: body})
	}))
	t.Cleanup(srv.Close)
	return srv, fetchedURLs
}

// TestSearchAppliesExtraction drives the wired pipeline end to end: the
// funnel emits a candidate missing price, the P3 stage detail-fetches the
// page through the same wowa client and schema.org-fills the product.
func TestSearchAppliesExtraction(t *testing.T) {
	clearSourceEnv(t)
	srv, fetchedURLs := newWowaStub(t)

	s, err := New(config.Config{WowaURL: srv.URL}, allowAllHosts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := s.SearchDetailed(t.Context(), "monitor", nil, 5)
	if err != nil {
		t.Fatalf("SearchDetailed: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %+v", out.Candidates)
	}
	assertSchemaEnriched(t, out.Candidates[0].EnrichedCandidate)
	if !urlFetched(*fetchedURLs, "deal1") {
		t.Fatalf("detail page never fetched: %v", *fetchedURLs)
	}
}

// assertSchemaEnriched checks the schema.org-filled product shape: price
// and currency came from the detail page; the name keeps the SERP title.
func assertSchemaEnriched(t *testing.T, got extract.EnrichedCandidate) {
	t.Helper()
	if got.ExtractionFailed {
		t.Fatalf("extraction failed: %s", got.FailureReason)
	}
	if got.Product.PriceMinor == nil || *got.Product.PriceMinor != 24999 {
		t.Fatalf("schema fill missing: %+v", got.Product)
	}
	if got.Product.Currency != "USD" || got.Product.Method != "schema" {
		t.Fatalf("product = %+v", got.Product)
	}
	if got.Product.Name == "" {
		t.Fatal("product name empty")
	}
}

func urlFetched(urls []string, needle string) bool {
	for _, u := range urls {
		if strings.Contains(u, needle) {
			return true
		}
	}
	return false
}

// allowAllHosts widens the manifest gate so TEST-NET fixture URLs keep
// flowing through the real adapters — the SSRF check stays live.
var allowAllHosts = pssources.WithManifestGate(func(pssources.Manifest, string) bool { return true })
