// Package config loads go-product-search configuration from environment
// variables.
package config

import (
	"time"

	"github.com/anatolykoptev/go-kit/env"
)

// Config holds all service configuration.
type Config struct {
	Port     string // main MCP/REST listener (PORT)
	PromPort string // separate /metrics listener (PROM_PORT, PORT+1000 convention)

	// WowaURL is the go-wowa scrape endpoint used to collect product
	// candidates. JeffURL/JeffToken point at the jeff decision service that
	// scores/matches those candidates.
	WowaURL   string
	JeffURL   string
	JeffToken string

	// InternalSecret is the shared bearer token required on every inbound
	// request except GET /healthz (Authorization: Bearer <secret>). Empty
	// fails closed: every non-open route returns 401.
	InternalSecret string

	// RedisURL enables the optional L2 caches. Empty = L1-only.
	RedisURL string

	// JeffMatchMin is the minimum jeff score for a candidate to count as a
	// match. MaxJeffCandidates caps how many scraped candidates go to jeff;
	// JeffConcurrency bounds in-flight jeff calls.
	JeffMatchMin      float64
	MaxJeffCandidates int
	JeffConcurrency   int

	// Extraction stage (P3, ADR-2/7): ExtractLLMTopN gates the fenced wowa
	// /extract LLM fallback to the top-N funnel-ranked candidates;
	// ExtractMaxDetailFetches bounds product-page fetches per search;
	// ExtractConcurrency bounds parallel candidate enrichment;
	// ExtractFetchTimeoutSecs is the per-fetch wire timeout;
	// ExtractCandidateTimeout bounds the whole per-candidate chain;
	// ExtractCacheMaxItems bounds the L1 cache; ProdsearchRedisDB selects
	// the dedicated Redis DB index for the L2 extraction cache.
	ExtractLLMTopN          int
	ExtractMaxDetailFetches int
	ExtractConcurrency      int
	ExtractFetchTimeoutSecs int
	ExtractCandidateTimeout time.Duration
	ExtractCacheMaxItems    int
	ProdsearchRedisDB       int

	// ToolTimeout is the default per-tool deadline; SearchToolTimeout and
	// MatchToolTimeout override it for the product_search / product_match
	// tools registered in the next arc.
	ToolTimeout       time.Duration
	SearchToolTimeout time.Duration
	MatchToolTimeout  time.Duration
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		Port:              env.Str("PORT", "8922"),
		PromPort:          env.Str("PROM_PORT", "9922"),
		WowaURL:           env.Str("WOWA_URL", "http://127.0.0.1:8906"),
		JeffURL:           env.Str("JEFF_URL", "https://jeff.krolik.tools"),
		JeffToken:         env.Str("JEFF_TOKEN", ""),
		InternalSecret:    env.Str("INTERNAL_SERVICE_SECRET", ""),
		RedisURL:          env.Str("REDIS_URL", ""),
		JeffMatchMin:      env.Float("JEFF_MATCH_MIN", 0.55),
		MaxJeffCandidates: env.Int("MAX_JEFF_CANDIDATES", 20),
		JeffConcurrency:   env.Int("JEFF_CONCURRENCY", 3),
		// Extraction stage.
		ExtractLLMTopN:          env.Int("EXTRACT_LLM_TOP_N", 10),
		ExtractMaxDetailFetches: env.Int("EXTRACT_MAX_DETAIL_FETCHES", 15),
		ExtractConcurrency:      env.Int("EXTRACT_CONCURRENCY", 4),
		ExtractFetchTimeoutSecs: env.Int("EXTRACT_FETCH_TIMEOUT_SECS", 25),
		ExtractCandidateTimeout: env.Duration("EXTRACT_CANDIDATE_TIMEOUT", 45*time.Second),
		ExtractCacheMaxItems:    env.Int("EXTRACT_CACHE_ITEMS", 2000),
		ProdsearchRedisDB:       env.Int("PRODSEARCH_REDIS_DB", 7),
		ToolTimeout:             env.Duration("TOOL_TIMEOUT", 90*time.Second),
		// product_search runs a wowa scrape (page loads are slow) and then
		// jeff matching, so it gets the long tier.
		SearchToolTimeout: env.Duration("TOOL_TIMEOUT_SEARCH", 3*time.Minute),
		MatchToolTimeout:  env.Duration("TOOL_TIMEOUT_MATCH", time.Minute),
	}
}
