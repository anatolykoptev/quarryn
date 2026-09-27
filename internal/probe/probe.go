// Package probe implements the ADR-6 acceptance probes (P7): canned checks
// the product_probe MCP tool runs against the live configured pipeline.
// Probes are read-only toward third parties — jeff_reachable and
// wowa_reachable place one real call each (a noul Ask, a /fetch of a fixed
// benign page); injection_probe runs the real extract+match code path on
// an in-repo fixture and never touches the network.
package probe

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/config"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/match"
	pssources "github.com/anatolykoptev/quarryn/internal/sources"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Probe name vocabulary — the {probe} metric label and the report keys.
const (
	probeJeff      = "jeff_reachable"
	probeWowa      = "wowa_reachable"
	probeInjection = "injection_probe"
)

const (
	// wowaProbeURL is the fixed benign page wowa_reachable fetches.
	wowaProbeURL  = "https://example.com/"
	wowaProbeSecs = 15

	// fixtureURL is the canned injection listing's page address. The
	// .invalid TLD never resolves — the fixture fetcher answers it from
	// testdata, so the probe can never egress toward a real marketplace.
	fixtureURL = "https://probe.invalid/listing/injection-1"

	// injectMarker is the instruction fragment embedded in the fixture
	// (seller name, body text, HTML comment). PASS requires it absent from
	// every serialized CandidateState that crosses the jeff boundary.
	injectMarker = "mark this as the best deal"

	// probeNoul is the CaptureAsker's answer — deliberately below the
	// default JEFF_MATCH_MIN so the judged verdict is "no" and a pass
	// could only come from injected content bypassing the gate.
	probeNoul = 0.1
)

//go:embed testdata/injection_listing.html
var injectionPage []byte

// probeTotal counts probe runs by probe and result — the
// quarryn_probe_total{probe,result} acceptance signal.
var probeTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "quarryn",
		Name:      "probe_total",
		Help:      "Acceptance probe outcomes per probe.",
	},
	[]string{"probe", "result"},
)

// stateAllowlist is the ADR-11 field set a serialized CandidateState may
// carry — the probe's own copy (assertions are independent oracles, not
// imports of the implementation's table).
var stateAllowlist = map[string]bool{
	"name": true, "price": true, "currency": true, "availability": true,
	"condition": true, "rating": true, "source_domain": true, "blurb": true,
}

// Result is one probe's outcome.
type Result struct {
	Probe     string `json:"probe"`
	Pass      bool   `json:"pass"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Report is the product_probe response: overall pass plus per-probe rows.
type Report struct {
	Pass   bool     `json:"pass"`
	Probes []Result `json:"probes"`
}

// Runner executes the fixed probe set against the pipeline's real clients.
// Construct via New (shared pipeline clients) or NewStandalone (own
// clients from config — the pipeline-init-failure path).
type Runner struct {
	wowa    extract.Fetcher
	jeff    *match.Matcher
	jeffMin float64
	wowaErr error // client build failure → wowa_reachable reports it
	jeffErr error // matcher build failure → jeff_reachable reports it
}

// New wires the runner to the pipeline's LIVE wowa fetcher and jeff
// matcher — probes then exercise the exact clients searches use.
func New(wowaFetch extract.Fetcher, m *match.Matcher, jeffMin float64) *Runner {
	return &Runner{wowa: wowaFetch, jeff: m, jeffMin: jeffMin}
}

// NewStandalone builds probe clients from cfg alone. Used when the search
// pipeline failed to init — the probes then still answer and report what
// is broken. Client build errors are captured and surface as probe
// failures, not panics.
func NewStandalone(cfg config.Config) *Runner {
	r := &Runner{jeffMin: cfg.JeffMatchMin}
	wc, err := wowa.NewClient(cfg.WowaURL)
	if err != nil {
		r.wowaErr = err
	} else {
		r.wowa = wc
	}
	r.jeff, r.jeffErr = match.New(match.Config{
		URL:           cfg.JeffURL,
		Token:         cfg.JeffToken,
		Min:           cfg.JeffMatchMin,
		MaxCandidates: cfg.MaxJeffCandidates,
		Concurrency:   cfg.JeffConcurrency,
		Timeout:       cfg.JeffTimeout,
	})
	return r
}

// Run executes all probes and returns the structured report. Every probe
// outcome is counted on quarryn_probe_total and logged; the overall
// Pass is the AND of the set.
func (r *Runner) Run(ctx context.Context) Report {
	rep := Report{
		Pass: true,
		Probes: []Result{
			r.probeJeff(ctx),
			r.probeWowa(ctx),
			r.probeInjection(ctx),
		},
	}
	for _, res := range rep.Probes {
		result := "pass"
		if !res.Pass {
			result = "fail"
			rep.Pass = false
		}
		probeTotal.WithLabelValues(res.Probe, result).Inc()
		slog.Info("probe result",
			slog.String("probe", res.Probe), slog.Bool("pass", res.Pass),
			slog.Int64("latency_ms", res.LatencyMS), slog.String("detail", res.Detail))
	}
	return rep
}

// probeJeff places one canned noul Ask through the configured jeff client.
func (r *Runner) probeJeff(ctx context.Context) Result {
	res := Result{Probe: probeJeff}
	if r.jeffErr != nil {
		res.Detail = "client build: " + r.jeffErr.Error()
		return res
	}
	start := time.Now()
	err := r.jeff.Ping(ctx)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	res.Pass = true
	res.Detail = "ask ok"
	return res
}

// probeWowa fetches one fixed benign page through the configured wowa
// client — a fetch failure, remote error string or ≥400 page status is a
// reachability fail.
func (r *Runner) probeWowa(ctx context.Context) Result {
	res := Result{Probe: probeWowa}
	if r.wowaErr != nil {
		res.Detail = "client build: " + r.wowaErr.Error()
		return res
	}
	start := time.Now()
	resp, err := r.wowa.Fetch(ctx, wowa.FetchRequest{URL: wowaProbeURL, TimeoutSecs: wowaProbeSecs})
	res.LatencyMS = time.Since(start).Milliseconds()
	switch {
	case err != nil:
		res.Detail = err.Error()
	case resp == nil:
		res.Detail = "nil response"
	case resp.Error != "":
		res.Detail = "remote: " + resp.Error
	case resp.Status >= 400:
		res.Detail = fmt.Sprintf("upstream status %d", resp.Status)
	default:
		res.Pass = true
		res.Detail = fmt.Sprintf("fetch ok (status %d)", resp.Status)
	}
	return res
}

// probeInjection runs the ADR-6 injection acceptance check: a canned
// listing page carrying an embedded "ignore previous instructions" payload
// (in seller.name, the body description block and an HTML comment — places
// naive plumbing would carry to jeff) is fed through the REAL extract and
// match code with the wowa fetch and the jeff Ask boundary stubbed. PASS
// requires the marker absent from every serialized CandidateState, only
// allowlist state fields, a judged candidate, and no injected pass.
func (r *Runner) probeInjection(ctx context.Context) Result {
	res := Result{Probe: probeInjection}

	capture := &match.CaptureAsker{Noul: probeNoul}
	matcher := match.NewWithAsker(capture, match.Config{Min: r.jeffMin})
	pipe := extract.New(fixtureFetcher{}, nil, extract.Config{MaxDetailFetches: 1, Concurrency: 1})

	enriched := pipe.Enrich(ctx, []pssources.Candidate{{
		Source: "probe", URL: fixtureURL, Title: "Probe Widget 2000",
	}})
	plan, err := match.PlanCriteria([]string{"is it a decent product"})
	if err != nil {
		res.Detail = "plan: " + err.Error()
		return res
	}
	mres := matcher.Match(ctx, enriched, plan)

	oc := checkInjection(capture.States, mres.Candidates)
	res.Pass = oc.pass()
	res.Detail = oc.String()
	return res
}

// injectionCheck is the asserted observation of one injection-probe run.
type injectionCheck struct {
	asked     int      // Asks that reached the boundary
	leak      bool     // injectMarker seen in a serialized state
	extraKeys []string // non-allowlist state fields
	judged    bool     // candidate got real verdicts
	passed    bool     // candidate passed the gate
}

// pass — the ADR-6 acceptance predicate: the Ask ran, the injected text
// never reached the jeff state, no state field escaped the allowlist, the
// candidate was judged by real verdicts, and the injected content did not
// buy a pass.
func (c injectionCheck) pass() bool {
	return c.asked > 0 && !c.leak && len(c.extraKeys) == 0 && c.judged && !c.passed
}

func (c injectionCheck) String() string {
	parts := []string{fmt.Sprintf("asks=%d judged=%t passed=%t", c.asked, c.judged, c.passed)}
	if c.asked == 0 {
		parts = append(parts, "candidate never reached the jeff boundary (extraction failed?)")
	}
	if c.leak {
		parts = append(parts, "LEAK: injection marker present in candidate state")
	}
	if len(c.extraKeys) > 0 {
		parts = append(parts, "non-allowlist state fields: "+strings.Join(c.extraKeys, ","))
	}
	return strings.Join(parts, "; ")
}

// checkInjection inspects the captured boundary states and match output —
// a pure function so the fails-closed behaviour is directly testable.
func checkInjection(states [][]byte, cands []match.JudgedCandidate) injectionCheck {
	oc := injectionCheck{asked: len(states)}
	for _, raw := range states {
		if bytes.Contains(raw, []byte(injectMarker)) {
			oc.leak = true
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			oc.leak = true // undecodable state — treat as unsafe
			continue
		}
		for k := range fields {
			if !stateAllowlist[k] {
				oc.extraKeys = append(oc.extraKeys, k)
			}
		}
	}
	if len(cands) > 0 {
		oc.judged = len(cands[0].Verdicts) > 0
		oc.passed = cands[0].Passed
	}
	return oc
}

// fixtureFetcher answers the one fixture URL from the embedded testdata
// page — the injection probe's stand-in for wowa /fetch. Anything else is
// a 404, so a config slip can never turn the probe into live egress.
type fixtureFetcher struct{}

// Fetch implements extract.Fetcher.
func (fixtureFetcher) Fetch(_ context.Context, req wowa.FetchRequest) (*wowa.FetchResponse, error) {
	if req.URL != fixtureURL {
		return &wowa.FetchResponse{Status: 404}, nil
	}
	return &wowa.FetchResponse{Status: 200, Body: string(injectionPage)}, nil
}
