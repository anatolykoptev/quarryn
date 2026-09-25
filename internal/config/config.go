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
	// match (values <=0 or >1 fall back to the 0.55 default in match.New —
	// JEFF_MATCH_MIN=0 cannot express "accept every verdict").
	// MaxJeffCandidates caps how many scraped candidates go to jeff;
	// JeffConcurrency bounds in-flight jeff calls; JeffTimeout is the
	// per-call deadline on each packed Ask (ADR-12).
	JeffMatchMin      float64
	MaxJeffCandidates int
	JeffConcurrency   int
	JeffTimeout       time.Duration

	// Extraction stage (P3, ADR-2/7): ExtractLLMTopN gates the fenced wowa
	// /extract LLM fallback to the top-N funnel-ranked candidates;
	// ExtractMaxDetailFetches bounds product-page fetches per search;
	// ExtractConcurrency bounds parallel candidate enrichment;
	// ExtractFetchTimeoutSecs is the per-fetch wire timeout;
	// ExtractCandidateTimeout bounds the whole per-candidate chain;
	// ExtractCacheMaxItems bounds the L1 cache; ProdsearchRedisDB selects
	// the dedicated Redis DB index for the L2 extraction cache;
	// ExtractLLMDailyMax (P6, ADR-12) caps /extract calls per UTC day —
	// process-local, resets on restart.
	ExtractLLMTopN          int
	ExtractMaxDetailFetches int
	ExtractConcurrency      int
	ExtractFetchTimeoutSecs int
	ExtractCandidateTimeout time.Duration
	ExtractCacheMaxItems    int
	ProdsearchRedisDB       int
	ExtractLLMDailyMax      int

	// Resilience bounds on the wowa page-fetch path (P6):
	// MaxPagesPerSearch caps the total SERP + detail fetch/render calls a
	// single search request may place; DomainMinInterval paces repeat
	// calls to one upstream host (throttle responses back off
	// exponentially on top — 2s→4s→8s, max 3 retries, then the domain is
	// skipped for that request).
	MaxPagesPerSearch int
	DomainMinInterval time.Duration

	// FeedbackFile is the append-only JSONL outcome log (ADR-10/P6): the
	// product_feedback tool and POST /api/v1/feedback append picked-listing
	// records that join the jeff_gate calibration events on request_id.
	// An unwritable path degrades to log-only (records logged, not
	// persisted).
	FeedbackFile string

	// ToolTimeout is the default per-tool deadline; SearchToolTimeout and
	// MatchToolTimeout override it for the product_search / product_match
	// tools registered in the next arc.
	ToolTimeout       time.Duration
	SearchToolTimeout time.Duration
	MatchToolTimeout  time.Duration

	// Rank weights (P5): the relative shares the funnel consensus score,
	// the ADR-17 deal signals and the jeff noul verdicts take in the fused
	// score. They renormalize to sum 1 — only their ratios matter — and
	// the jeff share drops to 0 automatically whenever a batch runs
	// degraded (rank.normalize).
	RankFunnelWeight float64
	RankDealWeight   float64
	RankJeffWeight   float64
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
		JeffTimeout:       env.Duration("JEFF_TIMEOUT", 10*time.Second),
		// Extraction stage.
		ExtractLLMTopN:          env.Int("EXTRACT_LLM_TOP_N", 10),
		ExtractMaxDetailFetches: env.Int("EXTRACT_MAX_DETAIL_FETCHES", 15),
		ExtractConcurrency:      env.Int("EXTRACT_CONCURRENCY", 4),
		ExtractFetchTimeoutSecs: env.Int("EXTRACT_FETCH_TIMEOUT_SECS", 25),
		ExtractCandidateTimeout: env.Duration("EXTRACT_CANDIDATE_TIMEOUT", 45*time.Second),
		ExtractCacheMaxItems:    env.Int("EXTRACT_CACHE_ITEMS", 2000),
		ProdsearchRedisDB:       env.Int("PRODSEARCH_REDIS_DB", 7),
		ExtractLLMDailyMax:      env.Int("EXTRACT_LLM_DAILY_MAX", 50),
		// P6 page-path bounds. DOMAIN_MIN_INTERVAL_MS=0 disables pacing.
		MaxPagesPerSearch: env.Int("MAX_PAGES_PER_SEARCH", 30),
		DomainMinInterval: time.Duration(env.Int("DOMAIN_MIN_INTERVAL_MS", 2000)) * time.Millisecond,
		FeedbackFile:      env.Str("FEEDBACK_FILE", "/var/lib/go-product-search/feedback.jsonl"),
		ToolTimeout:       env.Duration("TOOL_TIMEOUT", 90*time.Second),
		// product_search runs a wowa scrape (page loads are slow) and then
		// jeff matching, so it gets the long tier.
		SearchToolTimeout: env.Duration("TOOL_TIMEOUT_SEARCH", 3*time.Minute),
		MatchToolTimeout:  env.Duration("TOOL_TIMEOUT_MATCH", time.Minute),
		// Fusion weights — jeff dominates because the noul verdicts answer
		// the user's actual criteria (mirrors the match stage's provisional
		// 0.4/0.6 split, now sharing the deterministic half with deals).
		RankFunnelWeight: env.Float("RANK_FUNNEL_WEIGHT", 0.3),
		RankDealWeight:   env.Float("RANK_DEAL_WEIGHT", 0.2),
		RankJeffWeight:   env.Float("RANK_JEFF_WEIGHT", 0.5),
	}
}
