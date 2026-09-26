package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// P6 resilience surface (ADR-8 funnel bounds): every wowa fetch/render call
// passes a per-request page budget, per-domain pacing and throttle backoff.
var (
	// pagesFetchedTotal counts actual wowa fetch/render calls by pipeline
	// stage: serp (adapter SERP/catalog fetches inside the funnel) or
	// detail (extraction detail fetches, incl. render-tier escalations).
	pagesFetchedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "prodsearch",
			Name:      "pages_fetched_total",
			Help:      "wowa page fetch/render calls per search request by stage.",
		},
		[]string{"stage"},
	)
	// domainThrottledTotal counts upstream 403/429/503 responses seen by
	// the gate. No domain label — candidate URLs span arbitrary hosts and
	// the cardinality would be unbounded; the warn log carries the domain.
	domainThrottledTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "prodsearch",
			Name:      "domain_throttled_total",
			Help:      "Upstream 403/429/503 throttle responses observed by the fetch gate.",
		},
	)
)

const (
	// defaultDomainMinInterval paces repeat calls to one upstream domain
	// (DOMAIN_MIN_INTERVAL_MS).
	defaultDomainMinInterval = 2 * time.Second
	// DefaultDomainBackoff is the first throttle delay; it doubles per
	// consecutive throttle (2s → 4s → 8s, maxDomainRetries bounds the
	// retry count).
	DefaultDomainBackoff = 2 * time.Second
	// maxDomainRetries bounds throttle retries per call: initial attempt
	// plus up to 3 retries, then the domain is skipped for the request.
	maxDomainRetries = 3
	// maxBackoffShift caps the backoff exponent so a long-lived process
	// cannot accumulate absurd per-domain delays.
	maxBackoffShift = 5
)

// domainState tracks one upstream host: nextAllowed is the earliest call
// start (pacing) or the throttle backoff deadline, whichever is later;
// failures is the consecutive-throttle count driving the exponent.
type domainState struct {
	nextAllowed time.Time
	failures    int
}

// DomainPacer is the process-local per-domain pacing table (P6): a minimum
// interval between call starts to the same host plus exponential backoff
// while the domain returns throttle statuses. In-process only — no
// distributed state; a restart drops the table, which is fine at this
// scale.
type DomainPacer struct {
	mu       sync.Mutex
	domains  map[string]*domainState
	minWait  time.Duration // min interval between call starts; <=0 disables pacing waits
	backoff  time.Duration // first throttle delay; doubles per consecutive throttle
	maxRetry int           // throttle retries per call before the domain is skipped
}

// NewDomainPacer builds the shared pacer. minInterval is the configured
// DOMAIN_MIN_INTERVAL_MS value verbatim: negative → the 2s default, zero →
// pacing disabled (explicit off, the DOMAIN_MIN_INTERVAL_MS=0 escape), so a
// zero-value Config tests construct stays unpaced. backoff <=0 →
// DefaultDomainBackoff.
func NewDomainPacer(minInterval, backoff time.Duration) *DomainPacer {
	if minInterval < 0 {
		minInterval = defaultDomainMinInterval
	}
	if backoff <= 0 {
		backoff = DefaultDomainBackoff
	}
	return &DomainPacer{
		domains:  make(map[string]*domainState),
		minWait:  minInterval,
		backoff:  backoff,
		maxRetry: maxDomainRetries,
	}
}

// state returns the domain's slot, creating it on first touch.
func (p *DomainPacer) state(domain string) *domainState {
	st, ok := p.domains[domain]
	if !ok {
		st = &domainState{}
		p.domains[domain] = st
	}
	return st
}

// wait blocks until the domain's next call slot — honouring both the
// min-interval pacing and any active backoff — then claims it (advances
// nextAllowed by minWait so a concurrent caller queues behind). The wait
// ends early on ctx cancellation.
func (p *DomainPacer) wait(ctx context.Context, domain string) error {
	p.mu.Lock()
	st := p.state(domain)
	now := time.Now()
	start := now
	if st.nextAllowed.After(now) {
		start = st.nextAllowed
	}
	st.nextAllowed = start.Add(p.minWait)
	wait := start.Sub(now)
	p.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ok resets the domain's throttle streak after a non-throttled call.
func (p *DomainPacer) ok(domain string) {
	p.mu.Lock()
	if st, exists := p.domains[domain]; exists {
		st.failures = 0
	}
	p.mu.Unlock()
}

// throttled records one throttle response: the domain's nextAllowed moves
// to now + backoff<<failures and the streak grows. The domain's already
// claimed pacing slot is never shortened.
func (p *DomainPacer) throttled(domain string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state(domain)
	until := time.Now().Add(p.backoff << min(st.failures, maxBackoffShift))
	if until.After(st.nextAllowed) {
		st.nextAllowed = until
	}
	st.failures++
}

// Interacter is the wowa /api/v1/chrome/interact surface — a challenge-
// clearing real-browser session (auto_bypass). *wowa.Client satisfies it.
type Interacter interface {
	Interact(ctx context.Context, req wowa.InteractRequest) (*wowa.InteractResponse, error)
}

// Renderer is the wowa /api/v1/render surface (stealth Chrome) the gate
// also paces — a render is a page fetch for budget purposes.
type Renderer interface {
	Render(ctx context.Context, req wowa.RenderRequest) (*wowa.RenderResponse, error)
}

// FetchGate wraps a wowa client surface with the P6 resilience policy:
// per-request page budget (ctx-carried PageBudget), per-domain min-interval
// pacing and exponential backoff on 403/429/503 — after maxDomainRetries
// throttles the domain is skipped for the remainder of the search request.
// The stage label ("serp" | "detail") feeds prodsearch_pages_fetched_total.
type FetchGate struct {
	fetch    Fetcher
	render   Renderer   // may be nil — Render then fails fast
	interact Interacter // may be nil — Interact then fails fast
	pacer    *DomainPacer
	stage    string
}

// NewFetchGate builds a gate over fetch (required), render and interact
// (both optional) sharing pacer. stage is the metric label for the calls
// it wraps.
func NewFetchGate(fetch Fetcher, render Renderer, interact Interacter, stage string, pacer *DomainPacer) *FetchGate {
	if pacer == nil {
		pacer = NewDomainPacer(0, 0)
	}
	return &FetchGate{fetch: fetch, render: render, interact: interact, pacer: pacer, stage: stage}
}

// Fetch runs one wowa /fetch through pacing → budget → call → throttle
// retry. A page-status throttle that survives retries is returned as the
// response itself (Status 403/429/503) so upstream-status semantics stay
// intact; transport-level throttles return the last error. A skipped
// domain returns ErrDomainThrottled without touching the wire.
func (g *FetchGate) Fetch(ctx context.Context, req wowa.FetchRequest) (*wowa.FetchResponse, error) {
	var resp *wowa.FetchResponse
	err := g.run(ctx, req.URL, true, func() (int, error) {
		r, err := g.fetch.Fetch(ctx, req)
		if r != nil {
			resp = r
		}
		if err != nil {
			return 0, err
		}
		if r == nil {
			return 0, errors.New("sources: nil response without error")
		}
		return r.Status, nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Interact runs one wowa /chrome/interact through the same gate — a live
// browser session is a page fetch for budget and pacing purposes.
func (g *FetchGate) Interact(ctx context.Context, req wowa.InteractRequest) (*wowa.InteractResponse, error) {
	if g.interact == nil {
		return nil, errors.New("sources: interact requested but no interacter wired")
	}
	var resp *wowa.InteractResponse
	err := g.run(ctx, req.URL, false, func() (int, error) {
		r, err := g.interact.Interact(ctx, req)
		if r != nil {
			resp = r
		}
		return 0, err
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Render runs one wowa /render through the same gate — renders are page
// fetches for budget and pacing purposes.
func (g *FetchGate) Render(ctx context.Context, req wowa.RenderRequest) (*wowa.RenderResponse, error) {
	if g.render == nil {
		return nil, errors.New("sources: render requested but no renderer wired")
	}
	var resp *wowa.RenderResponse
	err := g.run(ctx, req.URL, true, func() (int, error) {
		r, err := g.render.Render(ctx, req)
		if r != nil {
			resp = r
		}
		if err != nil {
			return 0, err
		}
		if r == nil {
			return 0, errors.New("sources: nil response without error")
		}
		return r.Status, nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// run is the shared gate loop: skip → pace → budget → call → classify →
// back off or return. call reports the upstream page status (0 when the
// call errored before a page status existed) plus the call error.
// charge=false keeps pacing + backoff but skips the page budget — used
// by interact sessions, which are bounded by their own browser budget
// (an expensive 30-60s Chrome session is not a page fetch).
func (g *FetchGate) run(ctx context.Context, rawURL string, charge bool, call func() (int, error)) error {
	domain := gateDomain(rawURL)
	budget := PageBudgetFrom(ctx)
	if budget.domainSkipped(domain) {
		return fmt.Errorf("%w: %s", ErrDomainThrottled, domain)
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		if domain != "" {
			if err := g.pacer.wait(ctx, domain); err != nil {
				return err
			}
		}
		if charge && !budget.TryConsume() {
			// The page budget is the true stop reason — even mid-retry —
			// because "rank what exists" is the required disposition.
			return ErrPageBudgetExhausted
		}
		pagesFetchedTotal.WithLabelValues(g.stage).Inc()
		status, err := call()
		if !throttledStatus(status) && !throttledErr(err) {
			if domain != "" {
				g.pacer.ok(domain)
			}
			return err
		}
		domainThrottledTotal.Inc()
		lastErr = err
		if domain != "" {
			g.pacer.throttled(domain)
		}
		if attempt >= g.pacer.maxRetry {
			budget.skipDomain(domain)
			slog.Warn("source: domain throttled past retries — skipping for this search",
				slog.String("domain", domain), slog.String("stage", g.stage))
			// A page-status throttle leaves the response in the caller's
			// hands (resp.Status is the verdict); a transport-level one
			// surfaces as the error.
			return lastErr
		}
	}
}

// throttledStatus reports whether an upstream page status means the domain
// is pushing back — 403 (bot wall), 429 (rate limit) or 503 (shedding).
func throttledStatus(status int) bool {
	return status == 403 || status == 429 || status == 503
}

// throttledErr reports whether a wowa call error is a throttle signal —
// StatusError/RemoteError carrying 403/429/503 (proxy or go-wowa layer).
// Plain transport errors and timeouts are not throttles.
func throttledErr(err error) bool {
	var se *wowa.StatusError
	if errors.As(err, &se) {
		return throttledStatus(se.StatusCode)
	}
	var re *wowa.RemoteError
	if errors.As(err, &re) {
		return throttledStatus(re.StatusCode)
	}
	return false
}

// gateDomain returns the lowercased host (www. stripped) the pacer keys
// on; "" for unparseable URLs — those calls skip pacing but still count
// against the page budget.
func gateDomain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}
