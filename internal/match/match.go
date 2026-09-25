// Package match is the jeff decision stage (P4) of go-product-search:
// it turns extraction-enriched candidates into judged candidates.
//
// Design (ADRs 3/4/5/11/12):
//
//   - Criteria split (ADR-3): caller criteria are planned into
//     deterministic constraints applied in a prefilter BEFORE any jeff
//     call, plus subjective criteria asked as independent noul questions.
//     There is no overall-score question.
//   - Packed Ask (ADR-4): ONE jeff Ask per candidate carries every
//     criterion noul in Request.Questions (hard cap 64).
//   - Degrade-on-jeff-failure (ADR-5): transport errors, 429/529, queue
//     latency and missing answers downgrade candidates to
//     deterministic-only ranking — loudly (Degraded flag + counter +
//     warn log), never silently, and never fatal to the whole search.
//     Caller-ctx cancellations still mark candidates ctx_deadline but
//     are not jeff failures: no degrade flag, no counter.
//   - Typed CandidateState (ADR-11): the jeff boundary accepts only the
//     allowlisted, capped, control-stripped CandidateState built from
//     extract.ProductPublic. jeff wire types never leave this package.
//   - Dedicated client budget (ADR-12): JEFF_TOKEN auth, a concurrency
//     semaphore (JEFF_CONCURRENCY) and a per-call deadline.
package match

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"github.com/anatolykoptev/go-product-search/internal/extract"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Provisional fusion weights (P5 owns the real fusion — this weighted sum
// exists so results arrive pre-ordered). The jeff share dominates because
// the noul verdicts answer the user's actual criteria while the funnel
// score is only cross-source consensus.
const (
	detWeight  = 0.4
	jeffWeight = 0.6
)

var (
	// matchRequestsTotal counts Match calls by outcome:
	// ok | degraded (some candidates lost verdicts to jeff failure) | empty.
	matchRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "prodsearch",
			Name:      "match_requests_total",
			Help:      "jeff match calls partitioned by outcome.",
		},
		[]string{"outcome"},
	)
	// prefilterExcluded counts candidates the deterministic prefilter
	// rejected, by bounded reason label.
	prefilterExcluded = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "prodsearch",
			Name:      "prefilter_excluded_total",
			Help:      "Candidates excluded by deterministic prefilter constraints.",
		},
		[]string{"reason"},
	)
	// jeffDegradedTotal counts per-candidate degrade events by trigger:
	// saturated | unavailable | timeout | http_error | no_answer |
	// unconfigured. over_candidate_cap and ctx_deadline are budget/caller
	// data — they mark the candidate but never reach this counter.
	jeffDegradedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "prodsearch",
			Name:      "jeff_degraded_total",
			Help:      "Candidates downgraded to deterministic-only ranking by jeff failure.",
		},
		[]string{"reason"},
	)
	// jeffNoulProbability is the per-criterion noul distribution — the
	// ADR-6 calibration surface pairing with the jeff_gate log events.
	// The label is the criterion ID (c0..c63), a bounded vocabulary.
	jeffNoulProbability = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "prodsearch",
			Name:      "jeff_noul_probability",
			Help:      "Noul probability per criterion ID.",
			Buckets:   prometheus.LinearBuckets(0.05, 0.05, 19),
		},
		[]string{"criterion"},
	)
)

// JudgedCandidate is the stage output: the enriched candidate plus its
// per-criterion jeff verdicts and a provisional fused score.
type JudgedCandidate struct {
	extract.EnrichedCandidate

	// Verdicts maps criterion ID → noul probability for every judged
	// subjective criterion. Nil when the candidate was not judged —
	// UnjudgedReason then says why.
	Verdicts map[string]float64 `json:"verdicts,omitempty"`
	// Passed is the provisional gate: not excluded AND every verdict at or
	// above JeffMatchMin. A candidate owed a jeff verdict that never
	// arrived (jeff_* / ctx_deadline) reports false — an absent verdict
	// is not a pass. over_candidate_cap and jeff_unconfigured keep the
	// prefilter verdict: no jeff call was owed. With no subjective
	// criteria it reduces to the deterministic prefilter verdict.
	Passed bool `json:"passed"`
	// MatchScore is the provisional fusion: 0.4·normalized funnel score +
	// 0.6·mean noul probability. Zero on excluded candidates; the jeff
	// term contributes 0 for unjudged ones, so a degraded candidate can
	// never outrank a judged one on missing evidence.
	MatchScore float64 `json:"match_score"`
	// Excluded marks a deterministic-prefilter rejection (or an
	// unextractable/deferred candidate that never reached it).
	// ExcludeReason carries the bounded reason label.
	Excluded      bool   `json:"excluded,omitempty"`
	ExcludeReason string `json:"exclude_reason,omitempty"`
	// UnjudgedReason explains absent Verdicts on a non-excluded candidate:
	// a classified jeff failure ("jeff_*"), the candidate cap
	// ("over_candidate_cap"), or ctx deadline while queued.
	UnjudgedReason string `json:"unjudged_reason,omitempty"`
}

// Result is one Match call's output plus its degrade surface. Degraded is
// the loud flag ADR-5 requires: set whenever at least one candidate lost
// its jeff verdicts to a service-side failure.
type Result struct {
	Candidates    []JudgedCandidate `json:"candidates"`
	Degraded      bool              `json:"degraded,omitempty"`
	DegradeReason string            `json:"degrade_reason,omitempty"`
}

// Matcher runs the match stage: deterministic prefilter, then one packed
// jeff Ask per surviving candidate under a concurrency bound. Construct
// via New; safe for concurrent Match calls.
type Matcher struct {
	jeff        asker
	min         float64       // JeffMatchMin
	maxCands    int           // MaxJeffCandidates
	conc        int           // JeffConcurrency
	callTimeout time.Duration // per-call deadline (JEFF_TIMEOUT)
	reqSeq      atomic.Uint64 // jeff_gate request_id source
}

// Match applies the plan to candidates: deterministic prefilter → jeff
// budget cap → one packed Ask per survivor → provisional fusion and
// ranking. Candidates that fail any stage stay in the output flagged with
// their reason — nothing is dropped silently. Match never errors on jeff
// failure (ADR-5): it degrades.
func (m *Matcher) Match(ctx context.Context, cands []extract.EnrichedCandidate, plan Plan) Result {
	res := Result{Candidates: make([]JudgedCandidate, len(cands))}
	if len(cands) == 0 {
		matchRequestsTotal.WithLabelValues("empty").Inc()
		return res
	}

	eligible, maxScore := m.prefilterAll(&res, cands, plan.Constraints)

	// Deterministic share for everyone that survived the prefilter; the
	// jeff term lands on top inside fuse() for judged candidates.
	for _, i := range eligible {
		res.Candidates[i].MatchScore = detWeight * detScore(res.Candidates[i], maxScore)
	}
	eligible = m.capEligible(&res, cands, eligible)

	switch {
	case len(plan.Questions) == 0:
		// Deterministic-only plan — nothing for jeff to answer. Not a
		// degrade: there were no questions to lose.
	case m.jeff == nil:
		// No jeff configured while subjective criteria exist — every
		// eligible candidate degrades loudly.
		for _, i := range eligible {
			m.markUnjudged(&res, i, reasonUnconfigured)
		}
	default:
		m.askAll(ctx, &res, eligible, plan.Questions, maxScore)
	}

	summarizeDegrade(&res)
	orderAndFinish(&res)
	if res.Degraded {
		matchRequestsTotal.WithLabelValues("degraded").Inc()
	} else {
		matchRequestsTotal.WithLabelValues("ok").Inc()
	}
	return res
}

// prefilterAll applies the deterministic constraints to every candidate
// before any jeff call (ADR-3) and fills res.Candidates. Extraction
// failures and render-deferred candidates never reach the constraints:
// their products are untrusted or empty — they are excluded here so the
// output stays complete and flaggable. Returns the eligible candidate
// indexes plus the max funnel score among them (for detScore).
func (m *Matcher) prefilterAll(res *Result, cands []extract.EnrichedCandidate, cons Constraints) (eligible []int, maxScore float64) {
	for i, c := range cands {
		jc := JudgedCandidate{EnrichedCandidate: c, Passed: true}
		switch {
		case c.ExtractionFailed:
			jc.Excluded, jc.ExcludeReason = true, exclExtractionFailed
		case c.NeedsRender:
			jc.Excluded, jc.ExcludeReason = true, exclDeferredRender
		default:
			if reason := checkCandidate(c.Product, cons); reason != "" {
				jc.Excluded, jc.ExcludeReason = true, reason
				prefilterExcluded.WithLabelValues(reason).Inc()
			}
		}
		if jc.Excluded {
			jc.Passed = false
		} else {
			eligible = append(eligible, i)
			if c.Score > maxScore {
				maxScore = c.Score
			}
		}
		res.Candidates[i] = jc
	}
	return eligible, maxScore
}

// capEligible trims the jeff-bound set to MaxCandidates by funnel score —
// the budget for packed Asks is finite. Overflow candidates are marked
// over_candidate_cap: visible on the output but NOT a degrade — a cap is
// a budget decision, not a service failure.
func (m *Matcher) capEligible(res *Result, cands []extract.EnrichedCandidate, eligible []int) []int {
	if len(eligible) <= m.maxCands {
		return eligible
	}
	// Stable so equal funnel scores keep the caller's candidate order.
	sort.SliceStable(eligible, func(a, b int) bool {
		return cands[eligible[a]].Score > cands[eligible[b]].Score
	})
	for _, i := range eligible[m.maxCands:] {
		res.Candidates[i].UnjudgedReason = reasonOverCap
	}
	return eligible[:m.maxCands]
}

// detScore normalizes the funnel score to 0..1 against the batch max —
// FuseWRR scores are relative, so the scale is per-call by construction.
func detScore(jc JudgedCandidate, maxScore float64) float64 {
	if maxScore <= 0 || jc.Score <= 0 {
		return 0
	}
	return jc.Score / maxScore
}

// fuse upgrades a judged candidate's deterministic-only score into the
// provisional weighted sum and resolves the pass verdict against the
// configured threshold.
func (m *Matcher) fuse(jc *JudgedCandidate, maxScore float64) {
	sum := 0.0
	passed := true
	for _, p := range jc.Verdicts {
		sum += p
		if p < m.min {
			passed = false
		}
	}
	jc.MatchScore = detWeight*detScore(*jc, maxScore) + jeffWeight*(sum/float64(len(jc.Verdicts)))
	jc.Passed = passed
}

// orderAndFinish ranks non-excluded candidates by MatchScore and appends
// the excluded tail (funnel-score order) — an excluded candidate must
// never outrank a scored one on the flat list.
func orderAndFinish(res *Result) {
	ranked := make([]JudgedCandidate, 0, len(res.Candidates))
	excluded := make([]JudgedCandidate, 0, len(res.Candidates))
	for _, jc := range res.Candidates {
		if jc.Excluded {
			excluded = append(excluded, jc)
		} else {
			ranked = append(ranked, jc)
		}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		return ranked[a].MatchScore > ranked[b].MatchScore
	})
	sort.SliceStable(excluded, func(a, b int) bool {
		return excluded[a].Score > excluded[b].Score
	})
	res.Candidates = append(ranked, excluded...)
}
