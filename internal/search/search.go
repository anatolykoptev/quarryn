// Package search wires the sourcing, extraction and jeff-match stages into
// the service: it builds the wowa client, adapter registry, extraction
// pipeline and matcher from config/env and exposes the funnel's entrypoint
// that P5 (MCP tool surface) builds on.
package search

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go-kit/httputil"
	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/config"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/match"
	"github.com/anatolykoptev/quarryn/internal/probe"
	pssources "github.com/anatolykoptev/quarryn/internal/sources"
)

// Searcher is the product-search entrypoint: query → sourced candidates →
// extracted products → jeff-judged matches. Construct via New.
type Searcher struct {
	funnel   *pssources.Funnel
	pipeline *extract.Pipeline
	matcher  *match.Matcher
	prober   *probe.Runner
	registry map[string]pssources.Adapter
	// maxPages caps wowa fetch/render calls per request
	// (MAX_PAGES_PER_SEARCH) — attached to the request ctx as a
	// PageBudget the fetch gate consumes. <=0 attaches no budget
	// (unbounded; a zero-value Config — every test — stays uncapped).
	maxPages int
}

// Output is the full search result: jeff-judged candidates plus the
// per-source outcome report and the degrade surface for observability.
type Output struct {
	// RequestID is the ADR-6 calibration id minted for this call — the
	// uuid on every jeff_gate log event of the search and the key the
	// product_feedback outcome record joins on.
	RequestID     string                   `json:"request_id"`
	Candidates    []match.JudgedCandidate  `json:"candidates"`
	Sources       []pssources.SourceStatus `json:"sources"`
	Degraded      bool                     `json:"degraded,omitempty"`
	DegradeReason string                   `json:"degrade_reason,omitempty"`
	// Questions is the planned subjective criterion set (id → text) the
	// rank stage needs to build per-criterion explanations. Internal only —
	// never serialized.
	Questions []match.Question `json:"-"`
	Plan      match.Plan       `json:"-"`
}

// New builds the pipeline: a go-wowa client (all third-party egress,
// ADR-1) behind the P6 fetch gate (per-request page budget, per-domain
// pacing + throttle backoff), the env-resolved adapter registry
// (ADR-13/16), the sourcing funnel (ADR-8), the extraction stage
// (ADR-2/7/14) and the jeff match stage (ADR-3/4/5/11/12). Adapter
// credentials resolve from env inside RegistryConfigFromEnv; missing creds
// leave adapters dark, not fatal. An absent JEFF_URL builds a degrade-mode
// matcher — deterministic ranking only, flagged on every result.
func New(cfg config.Config, funnelOpts ...pssources.FunnelOption) (*Searcher, error) {
	wc, err := wowa.NewClient(cfg.WowaURL)
	if err != nil {
		return nil, fmt.Errorf("search: wowa client: %w", err)
	}
	matcher, err := match.New(match.Config{
		URL:           cfg.JeffURL,
		Token:         cfg.JeffToken,
		Min:           cfg.JeffMatchMin,
		MaxCandidates: cfg.MaxJeffCandidates,
		Concurrency:   cfg.JeffConcurrency,
		Timeout:       cfg.JeffTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("search: jeff matcher: %w", err)
	}
	// One pacer paces every domain the request touches — SERP adapter
	// fetches and detail fetches/renders share the table so a throttled
	// marketplace backs off for both stages.
	pacer := pssources.NewDomainPacer(cfg.DomainMinInterval, 0)
	serpGate := pssources.NewFetchGate(wc, nil, nil, "serp", pacer)
	detailGate := pssources.NewFetchGate(wc, wc, wc, "detail", pacer)
	registry := pssources.NewRegistry(pssources.RegistryConfigFromEnv(serpGate, nil))
	s := &Searcher{
		funnel:   pssources.NewFunnel(registry, funnelOpts...),
		pipeline: newPipeline(cfg, detailGate, wc, registry),
		matcher:  matcher,
		prober:   probe.New(wc, matcher, cfg.JeffMatchMin),
		registry: registry,
		maxPages: cfg.MaxPagesPerSearch,
	}
	slog.Info("search stage ready", slog.Any("adapters", s.AdapterStatus()))
	return s, nil
}

// newPipeline wires the P3/P6 extraction stage: the gated wowa client backs
// detail fetches AND the render escalation (both count against the page
// budget and pace per domain), while the fenced LLM fallback stays on the
// raw client — /extract fetches server-side through its own solver path.
// FetchClasses come from each adapter's declared Spec so render-class
// sources escalate correctly.
func newPipeline(cfg config.Config, gate *pssources.FetchGate, llm extract.LLMCaller, registry map[string]pssources.Adapter) *extract.Pipeline {
	classes := make(map[string]pssources.FetchClass, len(registry))
	outbound := make(map[string]bool, len(registry))
	for name, a := range registry {
		spec := a.Spec()
		classes[name] = spec.FetchClass
		outbound[name] = spec.ResolveOutbound
	}
	return extract.New(gate, llm, extract.Config{
		LLMTopN:          cfg.ExtractLLMTopN,
		MaxDetailFetches: cfg.ExtractMaxDetailFetches,
		MaxBrowserCalls:  cfg.ExtractMaxBrowserCalls,
		Concurrency:      cfg.ExtractConcurrency,
		CandidateTimeout: cfg.ExtractCandidateTimeout,
		FetchTimeoutSecs: cfg.ExtractFetchTimeoutSecs,
		LLMDailyMax:      cfg.ExtractLLMDailyMax,
		Render:           gate,
		Interact:         gate,
		FetchClasses:     classes,
		ResolveOutbound:  outbound,
		Cache:            newExtractCache(cfg),
	})
}

// newExtractCache builds the ADR-7 extraction cache: L1 S3-FIFO always,
// Redis L2 on the dedicated QUARRYN_REDIS_DB when REDIS_URL is set.
// Keys carry the "quarryn:" prefix + extractor version; TTL is 24h and
// L1 is item- and weight-bounded (64MB).
func newExtractCache(cfg config.Config) *cache.Cache {
	return cache.New(cache.Config{
		RedisURL:   cfg.RedisURL,
		RedisDB:    cfg.QuarrynRedisDB,
		Prefix:     "quarryn:",
		L1MaxItems: cfg.ExtractCacheMaxItems,
		L1TTL:      24 * time.Hour,
		L2TTL:      24 * time.Hour,
		MaxWeight:  64 << 20,
		Weigher:    func(_ string, d []byte) int64 { return int64(len(d)) },
	})
}

// Prober returns the acceptance-probe runner sharing the pipeline's live
// wowa/jeff clients — the product_probe tool's backend (ADR-6).
func (s *Searcher) Prober() *probe.Runner {
	return s.prober
}

// AdapterStatus reports each registered adapter's name → enabled state.
// Surfaced at startup so a misconfigured deployment is visible in logs
// before the first tool call.
func (s *Searcher) AdapterStatus() map[string]bool {
	out := make(map[string]bool, len(s.registry))
	for name, a := range s.registry {
		out[name] = a.Enabled()
	}
	return out
}

// Search runs the full pipeline for query under criteria and returns
// judged candidates. criteria mixes deterministic constraints
// ("price_max:500", "brand:sony", "not_keyword:refurbished",
// "availability:in_stock", "currency:usd" — see match.PlanCriteria for
// the full vocabulary) with free-text subjective criteria answered by
// jeff nouls. Adapters use their default page sizes.
func (s *Searcher) Search(ctx context.Context, query string, criteria []string) (Output, error) {
	return s.SearchDetailed(ctx, query, criteria, 0)
}

// SearchDetailed takes an explicit per-adapter upstream page-size limit;
// the funnel still caps the merged pool at its own limit (50, ADR-8) and
// extraction caps detail fetches/LLM calls at its own budgets.
func (s *Searcher) SearchDetailed(ctx context.Context, query string, criteria []string, limit int) (Output, error) {
	plan, err := match.PlanCriteria(criteria)
	if err != nil {
		return Output{}, err
	}
	// ADR-6 calibration id: one uuid per judged search, shared by the
	// response payload, every jeff_gate log event and the feedback record.
	reqID := match.NewRequestID()
	ctx = match.WithRequestID(ctx, reqID)
	// The P6 page budget rides the request ctx: every wowa fetch/render —
	// adapter SERP calls and extraction detail fetches alike — counts
	// against MAX_PAGES_PER_SEARCH. On cap the gate stops detail fetches
	// and ranking proceeds with what exists.
	if s.maxPages > 0 {
		ctx = pssources.WithPageBudget(ctx, pssources.NewPageBudget(s.maxPages))
	}
	q := sources.Query{Text: query, Limit: limit}
	out, err := s.funnel.Search(ctx, q)
	if err != nil {
		return Output{RequestID: reqID, Sources: out.Sources}, err
	}
	mres := s.matcher.Match(ctx, s.pipeline.Enrich(ctx, out.Candidates), plan)
	return Output{
		RequestID:     reqID,
		Candidates:    mres.Candidates,
		Sources:       out.Sources,
		Degraded:      mres.Degraded,
		DegradeReason: mres.DegradeReason,
		Questions:     plan.Questions,
		Plan:          plan,
	}, nil
}

// MatchURL runs the per-candidate path for one caller-supplied product URL
// — the product_match tool's backend. The funnel is bypassed (no adapter
// fan-out), but the URL still passes the same SSRF screen the funnel
// applies at candidate ingress (ADR-14: a caller-supplied URL is
// third-party egress too) and then goes through the identical extraction +
// match chain a search candidate would.
func (s *Searcher) MatchURL(ctx context.Context, rawURL string, criteria []string) (Output, error) {
	if err := httputil.CheckRawURL(ctx, rawURL); err != nil {
		return Output{}, err
	}
	plan, err := match.PlanCriteria(criteria)
	if err != nil {
		return Output{}, err
	}
	// Same calibration id as a search — the match stage's jeff_gate events
	// join the product_match response on it.
	reqID := match.NewRequestID()
	ctx = match.WithRequestID(ctx, reqID)
	// Same page budget as a search — one URL fits easily, but the cap keeps
	// the fetch/render path honest and identical to the funnel path.
	if s.maxPages > 0 {
		ctx = pssources.WithPageBudget(ctx, pssources.NewPageBudget(s.maxPages))
	}
	c := pssources.Candidate{Source: "direct", URL: rawURL}
	mres := s.matcher.Match(ctx, s.pipeline.Enrich(ctx, []pssources.Candidate{c}), plan)
	return Output{
		RequestID:     reqID,
		Candidates:    mres.Candidates,
		Degraded:      mres.Degraded,
		DegradeReason: mres.DegradeReason,
		Questions:     plan.Questions,
		Plan:          plan,
	}, nil
}
