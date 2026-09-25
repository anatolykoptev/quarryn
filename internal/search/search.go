// Package search wires the sourcing stage into the service: it builds the
// wowa client and adapter registry from config/env and exposes the funnel's
// entrypoint that P3 (matching) and P5 (MCP tool surface) build on.
package search

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/config"
	pssources "github.com/anatolykoptev/go-product-search/internal/sources"
)

// Searcher is the product-search entrypoint: query → sourced candidates.
// Construct via New.
type Searcher struct {
	funnel   *pssources.Funnel
	registry map[string]pssources.Adapter
}

// New builds the sourcing stage: a go-wowa client (fetch-class adapters,
// ADR-1), the env-resolved adapter registry (ADR-13/16), and the funnel
// (ADR-8). Adapter credentials resolve from env inside
// RegistryConfigFromEnv; missing creds leave adapters dark, not fatal.
func New(cfg config.Config) (*Searcher, error) {
	wc, err := wowa.NewClient(cfg.WowaURL)
	if err != nil {
		return nil, fmt.Errorf("search: wowa client: %w", err)
	}
	registry := pssources.NewRegistry(pssources.RegistryConfigFromEnv(wc, nil))
	s := &Searcher{
		funnel:   pssources.NewFunnel(registry),
		registry: registry,
	}
	slog.Info("sourcing stage ready", slog.Any("adapters", s.AdapterStatus()))
	return s, nil
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

// Search runs the sourcing funnel for query and returns typed candidates.
// limit bounds the per-adapter upstream page size; the funnel still caps
// the merged pool at its own limit (50, ADR-8).
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]pssources.Candidate, error) {
	out, err := s.SearchDetailed(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return out.Candidates, nil
}

// SearchDetailed additionally returns the per-source outcome report — the
// funnel's observability surface.
func (s *Searcher) SearchDetailed(ctx context.Context, query string, limit int) (pssources.SearchOutput, error) {
	q := sources.Query{Text: query, Limit: limit}
	return s.funnel.Search(ctx, q)
}
