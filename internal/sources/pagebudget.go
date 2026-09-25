package sources

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrPageBudgetExhausted is returned by the fetch gate when the request's
// page budget (MAX_PAGES_PER_SEARCH) is spent. Detail-stage callers map it
// to the "over_budget" outcome — the search keeps what it already fetched
// instead of erroring.
var ErrPageBudgetExhausted = errors.New("sources: page fetch budget exhausted")

// ErrDomainThrottled is returned by the fetch gate for calls to a domain
// that exhausted its throttle retries earlier in this same search request.
// The domain is skipped for the remainder of the request only — the
// pacer's backoff window still paces the next request.
var ErrDomainThrottled = errors.New("sources: domain throttled — skipped for this search")

// budgetKey is the context key carrying the per-request *PageBudget.
type budgetKey struct{}

// PageBudget is the per-search-request cap on wowa page fetches/renders
// (MAX_PAGES_PER_SEARCH, ADR-8 funnel bounds hardened in P6). It travels on
// the request context so every wowa call site — SERP adapter fetches inside
// the funnel and detail fetch/render calls inside extraction — counts
// against one budget with no signature changes. A request with no budget
// attached is unbounded (the gate only reads it).
type PageBudget struct {
	left atomic.Int64

	mu      sync.Mutex
	skipped map[string]struct{} // domains that exhausted throttle retries this request
}

// NewPageBudget returns a budget allowing n page fetches. n<=0 means no
// pages at all — callers wanting "unbounded" simply don't attach one.
func NewPageBudget(n int) *PageBudget {
	b := &PageBudget{}
	b.left.Store(int64(n))
	return b
}

// WithPageBudget attaches b to ctx; the fetch gate consults it on every
// wowa fetch/render call made under that context.
func WithPageBudget(ctx context.Context, b *PageBudget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

// PageBudgetFrom returns the request's budget, or nil when unattached.
func PageBudgetFrom(ctx context.Context) *PageBudget {
	b, _ := ctx.Value(budgetKey{}).(*PageBudget)
	return b
}

// TryConsume reserves one page fetch; false means the budget is spent.
// A nil budget is unbounded.
func (b *PageBudget) TryConsume() bool {
	return b == nil || b.left.Add(-1) >= 0
}

// skipDomain records domain as throttled-out for the rest of this request.
func (b *PageBudget) skipDomain(domain string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.skipped == nil {
		b.skipped = make(map[string]struct{})
	}
	b.skipped[domain] = struct{}{}
}

// domainSkipped reports whether domain was throttled out this request.
func (b *PageBudget) domainSkipped(domain string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.skipped[domain]
	return ok
}
