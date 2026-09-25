package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/anatolykoptev/go-engine/search"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/httputil"
	"golang.org/x/sync/errgroup"
)

const (
	// DefaultMaxCandidates caps the funnel output (ADR-8: ≤~50 links feed
	// the top-K downstream scoring).
	DefaultMaxCandidates = 50
	// defaultDedupThreshold is the BoW cosine-similarity cut passed to
	// DedupSnippets — the value go-search's pipeline converged on.
	defaultDedupThreshold = 0.85
	// defaultMaxParallel bounds concurrent adapter dispatches.
	defaultMaxParallel = 4
)

// ErrNoEnabledSources is returned when every registered adapter is
// disabled — a misconfiguration (no credentials anywhere), not an empty
// result.
var ErrNoEnabledSources = errors.New("sources: no enabled adapters — set EBAY_CLIENT_ID/EBAY_CLIENT_SECRET, ETSY_API_KEY, SHOPIFY_SHOPS, or WOWA_URL-backed fetchers")

// Per-source outcome vocabulary (go-job convention).
const (
	OutcomeOK      = "ok"
	OutcomeEmpty   = "empty"
	OutcomeSkipped = "skipped" // adapter registered but disabled (no creds)
	OutcomeFailed  = "failed"
)

// SourceStatus reports what one adapter contributed to a Search call.
type SourceStatus struct {
	Name     string `json:"name"`
	Outcome  string `json:"outcome"`
	Count    int    `json:"count"`              // results surviving SSRF screening
	Rejected int    `json:"rejected,omitempty"` // URLs dropped by the SSRF guard
	Reason   string `json:"reason,omitempty"`
}

// SearchOutput is the funnel's full result: typed candidates plus the
// per-source outcome report for observability.
type SearchOutput struct {
	Candidates []Candidate    `json:"candidates"`
	Sources    []SourceStatus `json:"sources"`
}

// Funnel is the ADR-8 merge/dedup stage: bounded fan-out to every enabled
// adapter, SSRF screening of each candidate URL the moment it enters the
// pipeline (ADR-14), FuseWRR merge, snippet dedup, cap, typed decode.
type Funnel struct {
	adapters       map[string]Adapter
	limit          int
	maxParallel    int
	dedupThreshold float64
	checkURL       func(ctx context.Context, rawURL string) error
}

// FunnelOption configures a Funnel.
type FunnelOption func(*Funnel)

// WithLimit overrides the funnel output cap (default 50, ADR-8).
func WithLimit(n int) FunnelOption {
	return func(f *Funnel) {
		if n > 0 {
			f.limit = n
		}
	}
}

// WithMaxParallel overrides the adapter dispatch bound (default 4).
func WithMaxParallel(n int) FunnelOption {
	return func(f *Funnel) {
		if n > 0 {
			f.maxParallel = n
		}
	}
}

// WithURLChecker overrides the per-candidate-URL guard. The production
// default is httputil.CheckRawURL — the option exists so tests can probe
// the funnel with private-range fixtures without real DNS.
func WithURLChecker(fn func(ctx context.Context, rawURL string) error) FunnelOption {
	return func(f *Funnel) {
		if fn != nil {
			f.checkURL = fn
		}
	}
}

// NewFunnel builds the sourcing funnel over the registry's adapters.
func NewFunnel(adapters map[string]Adapter, opts ...FunnelOption) *Funnel {
	f := &Funnel{
		adapters:       adapters,
		limit:          DefaultMaxCandidates,
		maxParallel:    defaultMaxParallel,
		dedupThreshold: defaultDedupThreshold,
		checkURL:       httputil.CheckRawURL,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Search runs the funnel: fan out to enabled adapters (bounded
// concurrency), SSRF-screen each candidate URL, merge via FuseWRR, dedup
// near-identical snippets, cap at the funnel limit, decode to Candidates.
//
// A single failing adapter never sinks the search — its outcome lands in
// Sources and the rest still merge. The call only errors when no adapter is
// enabled (ErrNoEnabledSources) or every enabled adapter failed.
func (f *Funnel) Search(ctx context.Context, q sources.Query) (SearchOutput, error) {
	enabled, statuses := f.partitionEnabled()
	if len(enabled) == 0 {
		return SearchOutput{Sources: statuses}, ErrNoEnabledSources
	}

	sets, stats, errs := f.fanOut(ctx, q, enabled)
	statuses = append(statuses, stats...)
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	candidates := f.mergeDedupCap(sets)
	out := SearchOutput{Candidates: candidates, Sources: statuses}

	// Every enabled adapter failing is an error, not an empty result —
	// otherwise a fleet-wide outage would masquerade as "no deals today".
	failed := 0
	for _, st := range stats {
		if st.Outcome == OutcomeFailed {
			failed++
		}
	}
	if failed == len(enabled) {
		return out, fmt.Errorf("sources: all %d enabled adapters failed: %w",
			failed, errors.Join(errs...))
	}
	return out, nil
}

// partitionEnabled splits the registry into dispatchable adapters and the
// skip-statuses of dark ones (logged, never an error — ADR-16).
func (f *Funnel) partitionEnabled() ([]Adapter, []SourceStatus) {
	names := make([]string, 0, len(f.adapters))
	for name := range f.adapters {
		names = append(names, name)
	}
	sort.Strings(names)

	var enabled []Adapter
	var statuses []SourceStatus
	for _, name := range names {
		a := f.adapters[name]
		if !a.Enabled() {
			slog.Info("source skipped: adapter disabled",
				slog.String("source", name))
			statuses = append(statuses, SourceStatus{
				Name:    name,
				Outcome: OutcomeSkipped,
				Reason:  "disabled (missing credentials/config)",
			})
			continue
		}
		enabled = append(enabled, a)
	}
	return enabled, statuses
}

// fanOut dispatches Search to every enabled adapter with bounded
// concurrency. Each goroutine owns its slots in the returned slices — no
// mutex. Per-source failure is data (SourceStatus + errs slot), not fatal.
func (f *Funnel) fanOut(ctx context.Context, q sources.Query, enabled []Adapter) ([][]sources.Result, []SourceStatus, []error) {
	sets := make([][]sources.Result, len(enabled))
	stats := make([]SourceStatus, len(enabled))
	errs := make([]error, len(enabled))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(f.maxParallel)
	for i, a := range enabled {
		g.Go(func() error {
			stats[i], sets[i], errs[i] = f.collect(gctx, a, q)
			return nil
		})
	}
	_ = g.Wait() // goroutines never return non-nil
	return sets, stats, errs
}

// collect runs one adapter and SSRF-screens its results (ADR-14: every
// candidate URL passes the guard before anything downstream can see it).
func (f *Funnel) collect(ctx context.Context, a Adapter, q sources.Query) (SourceStatus, []sources.Result, error) {
	name := a.Name()
	res, err := a.Search(ctx, q)
	if err != nil {
		slog.Warn("source failed", slog.String("source", name), slog.Any("error", err))
		return SourceStatus{Name: name, Outcome: OutcomeFailed, Reason: err.Error()}, nil, err
	}

	kept := make([]sources.Result, 0, len(res))
	rejected := 0
	for _, r := range res {
		if r.URL == "" {
			rejected++
			continue
		}
		if cerr := f.checkURL(ctx, r.URL); cerr != nil {
			rejected++
			slog.Info("candidate rejected by SSRF guard",
				slog.String("source", name), slog.String("url", r.URL))
			continue
		}
		if r.Metadata == nil {
			r.Metadata = make(map[string]string)
		}
		r.Metadata[MetaSource] = name
		kept = append(kept, r)
	}

	outcome := OutcomeOK
	if len(res) == 0 {
		outcome = OutcomeEmpty
	}
	return SourceStatus{Name: name, Outcome: outcome, Count: len(kept), Rejected: rejected}, kept, nil
}

// mergeDedupCap fuses per-source rank lists (FuseWRR), drops near-duplicate
// snippets, caps at the funnel limit, and decodes to typed Candidates.
func (f *Funnel) mergeDedupCap(sets [][]sources.Result) []Candidate {
	merged := search.FuseWRR(sets, nil)
	deduped := search.DedupSnippets(merged, f.dedupThreshold)
	if len(deduped) > f.limit {
		deduped = deduped[:f.limit]
	}
	candidates := make([]Candidate, 0, len(deduped))
	for _, r := range deduped {
		candidates = append(candidates, candidateFromResult(r))
	}
	return candidates
}
