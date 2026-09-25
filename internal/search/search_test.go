package search

import (
	"testing"

	"github.com/anatolykoptev/go-product-search/internal/config"
)

// clearSourceEnv pins every adapter credential env var to empty so the test
// is hermetic regardless of the host's real environment.
func clearSourceEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EBAY_CLIENT_ID", "EBAY_CLIENT_SECRET",
		"ETSY_API_KEY", "ETSY_SHARED_SECRET",
		"SHOPIFY_SHOPS", "INTERNAL_SERVICE_SECRET",
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
	_, err = s.Search(t.Context(), "anything", 5)
	if err == nil {
		t.Fatal("Search must error when every enabled adapter fails")
	}
	out, outErr := s.SearchDetailed(t.Context(), "anything", 5)
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
