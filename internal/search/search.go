// Package search wires the sourcing and extraction stages into the
// service: it builds the wowa client, adapter registry and extraction
// pipeline from config/env and exposes the funnel's entrypoint that P4
// (jeff matching) and P5 (MCP tool surface) build on.
package search

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/config"
	"github.com/anatolykoptev/go-product-search/internal/extract"
	pssources "github.com/anatolykoptev/go-product-search/internal/sources"
)

// Searcher is the product-search entrypoint: query → sourced candidates →
// extracted products. Construct via New.
type Searcher struct {
	funnel   *pssources.Funnel
	pipeline *extract.Pipeline
	registry map[string]pssources.Adapter
}

// Output is the full search result: extraction-enriched candidates plus
// the per-source outcome report for observability.
type Output struct {
	Candidates []extract.EnrichedCandidate `json:"candidates"`
	Sources    []pssources.SourceStatus    `json:"sources"`
}

// New builds the pipeline: a go-wowa client (all third-party egress,
// ADR-1), the env-resolved adapter registry (ADR-13/16), the sourcing
// funnel (ADR-8) and the extraction stage (ADR-2/7/14). Adapter
// credentials resolve from env inside RegistryConfigFromEnv; missing creds
// leave adapters dark, not fatal.
func New(cfg config.Config) (*Searcher, error) {
	wc, err := wowa.NewClient(cfg.WowaURL)
	if err != nil {
		return nil, fmt.Errorf("search: wowa client: %w", err)
	}
	registry := pssources.NewRegistry(pssources.RegistryConfigFromEnv(wc, nil))
	s := &Searcher{
		funnel:   pssources.NewFunnel(registry),
		pipeline: newPipeline(cfg, wc, registry),
		registry: registry,
	}
	slog.Info("search stage ready", slog.Any("adapters", s.AdapterStatus()))
	return s, nil
}

// newPipeline wires the P3 extraction stage: the wowa client backs both
// detail fetches and the fenced LLM fallback; FetchClasses come from each
// adapter's declared Spec so render-class sources defer correctly.
func newPipeline(cfg config.Config, wc *wowa.Client, registry map[string]pssources.Adapter) *extract.Pipeline {
	classes := make(map[string]pssources.FetchClass, len(registry))
	for name, a := range registry {
		classes[name] = a.Spec().FetchClass
	}
	return extract.New(wc, wc, extract.Config{
		LLMTopN:          cfg.ExtractLLMTopN,
		MaxDetailFetches: cfg.ExtractMaxDetailFetches,
		Concurrency:      cfg.ExtractConcurrency,
		CandidateTimeout: cfg.ExtractCandidateTimeout,
		FetchTimeoutSecs: cfg.ExtractFetchTimeoutSecs,
		FetchClasses:     classes,
		Cache:            newExtractCache(cfg),
	})
}

// newExtractCache builds the ADR-7 extraction cache: L1 S3-FIFO always,
// Redis L2 on the dedicated PRODSEARCH_REDIS_DB when REDIS_URL is set.
// Keys carry the "prodsearch:" prefix + extractor version; TTL is 24h and
// L1 is item- and weight-bounded (64MB).
func newExtractCache(cfg config.Config) *cache.Cache {
	return cache.New(cache.Config{
		RedisURL:   cfg.RedisURL,
		RedisDB:    cfg.ProdsearchRedisDB,
		Prefix:     "prodsearch:",
		L1MaxItems: cfg.ExtractCacheMaxItems,
		L1TTL:      24 * time.Hour,
		L2TTL:      24 * time.Hour,
		MaxWeight:  64 << 20,
		Weigher:    func(_ string, d []byte) int64 { return int64(len(d)) },
	})
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

// Search runs funnel + extraction for query and returns enriched
// candidates. limit bounds the per-adapter upstream page size; the funnel
// still caps the merged pool at its own limit (50, ADR-8) and extraction
// caps detail fetches/LLM calls at its own budgets.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]extract.EnrichedCandidate, error) {
	out, err := s.SearchDetailed(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return out.Candidates, nil
}

// SearchDetailed additionally returns the per-source outcome report — the
// funnel's observability surface.
func (s *Searcher) SearchDetailed(ctx context.Context, query string, limit int) (Output, error) {
	q := sources.Query{Text: query, Limit: limit}
	out, err := s.funnel.Search(ctx, q)
	if err != nil {
		return Output{Sources: out.Sources}, err
	}
	return Output{
		Candidates: s.pipeline.Enrich(ctx, out.Candidates),
		Sources:    out.Sources,
	}, nil
}
