package sources

import (
	"testing"
)

func TestRegistryHasV1SourceMap(t *testing.T) {
	reg := NewRegistry(RegistryConfig{})
	want := map[string]FetchClass{
		"ebay":       FetchClassAPI,
		"etsy":       FetchClassAPI,
		"slickdeals": FetchClassFetch,
		"shopify":    FetchClassFetch,
	}
	if len(reg) != len(want) {
		t.Fatalf("registry size = %d, want %d", len(reg), len(want))
	}
	for name, class := range want {
		a, ok := reg[name]
		if !ok {
			t.Fatalf("registry missing %q", name)
		}
		if a.Name() != name {
			t.Errorf("%s: Name() = %q", name, a.Name())
		}
		if a.Spec().FetchClass != class {
			t.Errorf("%s: FetchClass = %q, want %q", name, a.Spec().FetchClass, class)
		}
	}
}

func TestRegistryAdaptersShipDark(t *testing.T) {
	// No creds, no fetcher, no shops — every adapter registered but disabled.
	reg := NewRegistry(RegistryConfig{})
	for name, a := range reg {
		if a.Enabled() {
			t.Errorf("%s enabled without credentials/config", name)
		}
	}
}

func TestRegistryEnablement(t *testing.T) {
	reg := NewRegistry(RegistryConfig{
		Fetcher:      stubFetcher{body: "{}"},
		EBayClientID: "id", EBayClientSecret: "secret",
		EtsyAPIKey:   "key",
		ShopifyShops: []string{"a.example.com"},
	})
	for name, a := range reg {
		if !a.Enabled() {
			t.Errorf("%s should be enabled", name)
		}
	}
}

func TestRegistryConfigFromEnv(t *testing.T) {
	t.Setenv("EBAY_CLIENT_ID", "env-id")
	t.Setenv("EBAY_CLIENT_SECRET", "env-secret")
	t.Setenv("ETSY_API_KEY", "env-etsy")
	t.Setenv("ETSY_SHARED_SECRET", "")
	t.Setenv("SHOPIFY_SHOPS", "a.example.com,b.example.com")

	cfg := RegistryConfigFromEnv(nil, nil)
	if cfg.EBayClientID != "env-id" || cfg.EBayClientSecret != "env-secret" {
		t.Fatalf("ebay creds = %q/%q", cfg.EBayClientID, cfg.EBayClientSecret)
	}
	if cfg.EtsyAPIKey != "env-etsy" {
		t.Fatalf("etsy key = %q", cfg.EtsyAPIKey)
	}
	if len(cfg.ShopifyShops) != 2 {
		t.Fatalf("shops = %v", cfg.ShopifyShops)
	}
}
