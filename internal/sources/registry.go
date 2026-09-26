package sources

import (
	"net/http"
	"time"

	"github.com/anatolykoptev/go-kit/env"
)

// RegistryConfig bundles the credentials and shared clients adapters are
// built from. RegistryConfigFromEnv resolves it from process env; tests
// construct it directly.
type RegistryConfig struct {
	// Fetcher is the wowa client handed to fetch-class adapters.
	Fetcher Fetcher
	// HTTP is the direct-HTTPS client for api-class adapters (nil → a
	// shared 30s-timeout client). API endpoints are compile-time constants,
	// so this client needs no SSRF guard — the funnel SSRF-checks candidate
	// URLs, which is where attacker-controlled input lives (ADR-14).
	HTTP *http.Client

	EBayClientID     string // EBAY_CLIENT_ID
	EBayClientSecret string // EBAY_CLIENT_SECRET
	EBayBaseURL      string // test override; empty → api.ebay.com

	EtsyAPIKey       string // ETSY_API_KEY
	EtsySharedSecret string // ETSY_SHARED_SECRET
	EtsyBaseURL      string // test override; empty → openapi.etsy.com

	SlickdealsFeedURL string // test override; empty → slickdeals.net feed

	ShopifyShops []string // SHOPIFY_SHOPS, comma-separated shop domains
	// ShopifyUCPProfileURL enables the Shopify UCP MCP legs (SHOPIFY_UCP_PROFILE_URL).
	ShopifyUCPProfileURL string
	// ShopifyUCPGlobalURL overrides the Global Catalog endpoint (tests).
	ShopifyUCPGlobalURL string
	// ShopifyUCPGlobalOff disables the global-catalog leg (SHOPIFY_UCP_GLOBAL=0).
	ShopifyUCPGlobalOff bool
}

// RegistryConfigFromEnv resolves adapter configuration from the
// environment. fetcher/httpClient are the shared clients the registry hands
// to adapters.
func RegistryConfigFromEnv(fetcher Fetcher, httpClient *http.Client) RegistryConfig {
	return RegistryConfig{
		Fetcher:              fetcher,
		HTTP:                 httpClient,
		EBayClientID:         env.Str("EBAY_CLIENT_ID", ""),
		EBayClientSecret:     env.Str("EBAY_CLIENT_SECRET", ""),
		EtsyAPIKey:           env.Str("ETSY_API_KEY", ""),
		EtsySharedSecret:     env.Str("ETSY_SHARED_SECRET", ""),
		ShopifyShops:         env.List("SHOPIFY_SHOPS", ""),
		ShopifyUCPProfileURL: env.Str("SHOPIFY_UCP_PROFILE_URL", ""),
		ShopifyUCPGlobalURL:  env.Str("SHOPIFY_UCP_GLOBAL_URL", ""),
		ShopifyUCPGlobalOff:  !env.Bool("SHOPIFY_UCP_GLOBAL", true),
	}
}

// defaultHTTP is the shared direct-HTTPS client for api-class adapters.
var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// NewRegistry builds the ADR-13 source map — a Go map literal of source
// name → adapter for the v1 marketplaces (ADR-16). Adapters that lack
// credentials are still registered (ships dark); the funnel consults
// Enabled() at dispatch.
func NewRegistry(cfg RegistryConfig) map[string]Adapter {
	hc := cfg.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	return map[string]Adapter{
		"ebay":       NewEBay(cfg.EBayClientID, cfg.EBayClientSecret, cfg.EBayBaseURL, hc),
		"etsy":       NewEtsy(cfg.EtsyAPIKey, cfg.EtsySharedSecret, cfg.EtsyBaseURL, hc),
		"slickdeals": NewSlickdeals(cfg.Fetcher, cfg.SlickdealsFeedURL),
		"shopify": NewShopify(cfg.Fetcher, ShopifyConfig{
			Shops:      cfg.ShopifyShops,
			ProfileURL: cfg.ShopifyUCPProfileURL,
			GlobalURL:  cfg.ShopifyUCPGlobalURL,
			GlobalOff:  cfg.ShopifyUCPGlobalOff,
		}),
	}
}
