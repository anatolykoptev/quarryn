package match

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/go-kit/jeff"
)

// Defaults for the jeff call budget (ADR-12). Sized against the observed
// jeff latency envelope (~1s/question on the reference deployment, growing
// with label count) — 10s covers a packed 64-question Ask with headroom.
const (
	defaultJeffTimeout     = 10 * time.Second
	defaultJeffConcurrency = 3
	defaultMaxCandidates   = 20
	defaultMatchMin        = 0.55
)

// Config bundles the Matcher's dependencies and limits. Values mirror the
// config.Config jeff fields; zero values get package defaults.
type Config struct {
	// URL is the jeff service base URL. Empty builds a degrade-mode
	// matcher: every candidate is ranked deterministically and flagged
	// unconfigured rather than erroring the search (ADR-5).
	URL string
	// Token is the jeff bearer token (JEFF_TOKEN).
	Token string
	// Min is the per-criterion noul pass threshold (JEFF_MATCH_MIN,
	// default 0.55).
	Min float64
	// MaxCandidates caps how many prefilter survivors consume a jeff Ask
	// (MAX_JEFF_CANDIDATES, default 20).
	MaxCandidates int
	// Concurrency bounds in-flight jeff calls (JEFF_CONCURRENCY,
	// default 3).
	Concurrency int
	// Timeout is the per-call deadline on each packed Ask
	// (JEFF_TIMEOUT, default 10s).
	Timeout time.Duration
}

// Asker is the narrow slice of *jeff.Client this stage uses — kept behind
// an interface so tests and the injection probe can substitute the Ask
// boundary without a live service. jeff wire types stay inside package
// match (ADR-11).
type Asker interface {
	Ask(ctx context.Context, req jeff.Request) (*jeff.Response, error)
}

// New builds the Matcher. An empty URL yields a degrade-mode matcher
// (jeff=nil) — present and callable, just never judging. A missing token
// warns loudly at startup (the go-wowa newJeffGate pattern): the client
// still builds and per-call 401s degrade instead of silently failing.
func New(cfg Config) (*Matcher, error) {
	m := newMatcher(cfg)
	if cfg.URL == "" {
		slog.Warn("match: no JEFF_URL — matcher runs in degrade mode, deterministic ranking only")
		return m, nil
	}
	if cfg.Token == "" {
		slog.Warn("match: jeff configured without JEFF_TOKEN; every Ask will degrade on 401")
	}
	jc, err := jeff.NewClient(cfg.URL, jeff.WithToken(cfg.Token), jeff.WithTimeout(m.callTimeout))
	if err != nil {
		return nil, err
	}
	m.jeff = jc
	return m, nil
}

// newMatcher applies the Config defaults and returns a Matcher without an
// Ask backend — the shared constructor New (real client) and NewWithAsker
// (injected boundary) build on.
func newMatcher(cfg Config) *Matcher {
	m := &Matcher{
		min:         cfg.Min,
		maxCands:    cfg.MaxCandidates,
		conc:        cfg.Concurrency,
		callTimeout: cfg.Timeout,
	}
	if m.min <= 0 || m.min > 1 {
		m.min = defaultMatchMin
	}
	if m.maxCands <= 0 {
		m.maxCands = defaultMaxCandidates
	}
	if m.conc <= 0 {
		m.conc = defaultJeffConcurrency
	}
	if m.callTimeout <= 0 {
		m.callTimeout = defaultJeffTimeout
	}
	return m
}

// callDeadline applies the per-call bound (min of the caller ctx deadline
// and the configured timeout).
func (m *Matcher) callDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.callTimeout)
}

// askAll fans the packed Ask out over the eligible indexes under a
// ctx-aware semaphore (ADR-12). A candidate that cannot acquire a slot
// before the caller ctx dies is marked ctx_deadline — queue latency is a
// degrade trigger, never a panic or a hang.
func (m *Matcher) askAll(ctx context.Context, res *Result, eligible []int, qs []Question, maxScore float64) {
	questions := make(map[string]jeff.Question, len(qs))
	for _, q := range qs {
		questions[q.ID] = jeff.NoulQuestion(q.Instruction)
	}

	sem := make(chan struct{}, m.conc)
	var wg sync.WaitGroup
	reqID := m.requestID(ctx)
	for _, i := range eligible {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			m.markUnjudged(res, i, ReasonCtxDeadline)
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			m.judgeOne(ctx, res, idx, questions, maxScore, reqID)
		}(i)
	}
	wg.Wait()
}

// judgeOne performs the single packed Ask for one candidate (ADR-4) and
// writes back either Verdicts+fused score or an unjudged marking. The
// jeff_gate log event (ADR-6) pairs each call with its verdicts and
// latency for later calibration joins.
func (m *Matcher) judgeOne(ctx context.Context, res *Result, idx int, questions map[string]jeff.Question, maxScore float64, reqID string) {
	jc := &res.Candidates[idx]
	state := NewCandidateState(jc.ProductPublic())

	cctx, cancel := m.callDeadline(ctx)
	defer cancel()
	start := time.Now()
	resp, err := m.jeff.Ask(cctx, jeff.Request{State: state, Questions: questions})
	latencyMs := time.Since(start).Milliseconds()

	var verdicts map[string]float64
	outcome := ReasonCode("ok")
	if err != nil {
		outcome = classifyJeffError(err, ctx)
	} else {
		verdicts = make(map[string]float64, len(questions))
		for id := range questions {
			a, ok := resp.Answers[id]
			// A missing answer or an out-of-range noul (NaN fails the
			// range check too) is unusable — degrade rather than feed
			// wire garbage into MatchScore and the calibration histogram.
			if !ok || !(a.Noul >= 0 && a.Noul <= 1) {
				outcome = ReasonJeffNoAnswer
				verdicts = nil
				break
			}
			verdicts[id] = a.Noul
		}
	}

	stateJSON, sErr := json.Marshal(state)
	verdictsJSON, vErr := json.Marshal(verdicts)
	if sErr != nil || vErr != nil {
		// Instrumentation only — a marshal failure here never fails the Ask.
		slog.Debug("jeff_gate: marshal failed", "state_err", sErr, "verdicts_err", vErr)
	}
	attrs := []any{
		"request_id", reqID, "candidate", jc.URL,
		"questions", len(questions), "verdicts", string(verdictsJSON),
		"latency_ms", latencyMs, "outcome", outcome,
		"state", string(stateJSON),
	}
	if err != nil {
		attrs = append(attrs, "err", err.Error())
	}
	if outcome != "ok" {
		slog.Warn("jeff_gate", attrs...)
	} else {
		slog.Info("jeff_gate", attrs...)
	}

	if verdicts == nil {
		m.markUnjudged(res, idx, outcome)
		return
	}
	jc.Verdicts = verdicts
	for id, p := range verdicts {
		jeffNoulProbability.WithLabelValues(id).Observe(p)
	}
	m.fuse(jc, maxScore)
}
