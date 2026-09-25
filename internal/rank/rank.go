// Package rank is the final ranking stage (P5) of go-product-search: it
// fuses the per-candidate signals the earlier stages produced — funnel
// consensus score, deterministic deal signals (ADR-17) and jeff noul
// verdicts — into one score via go-kit/rerank LinearMinMax, then emits the
// ranked list with a per-result explanation payload.
//
// Signal model:
//
//   - funnel: Candidate.Score, the FuseWRR cross-source consensus
//     (relative by construction — LinearMinMax normalization fits).
//   - deal: deterministic ADR-17 signals — discount_pct, slickdeals
//     thumbs and seller/product rating — composed into a [0,1] score over
//     whichever signals the candidate carries.
//   - jeff: mean noul probability across the plan's subjective criteria;
//     only judged candidates contribute — an unjudged candidate carries
//     no jeff evidence and competes on features alone.
//
// Weights are relative (normalized to sum 1 at fuse time). In degraded
// mode the jeff weight is forced to 0 — a partially-judged batch ranks on
// features uniformly rather than letting judged candidates win on
// evidence the others never got. Confidence buckets come from
// go-kit/score.ConfidenceFromScore on the normalized [0,1] fused score.
package rank

import (
	"math"
	"sort"
	"strconv"

	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/go-kit/score"
	"github.com/anatolykoptev/go-product-search/internal/match"
)

// Deal-signal sub-weights (ADR-17). They renormalize over the signals a
// candidate actually carries — a missing thumbs count does not zero the
// discount signal. Constants, not config: the caller tunes the deal
// signal's share of the final score via Weights.Deal.
const (
	wDiscount = 0.5
	wThumbs   = 0.3
	wRating   = 0.2

	// thumbsSaturation is the thumbs count treated as a perfect signal;
	// slickdeals front-page deals land in the low hundreds.
	thumbsSaturation = 300.0
	ratingScale      = 5.0 // schema.org 0-5 convention (extract.Product.Rating)
)

// Weights is the relative share each signal family takes in the fused
// score. Values are normalized to sum 1 at fuse time, so only their
// ratios matter. Zero-weight signals are dropped; if every weight is
// zero the funnel signal is used alone (ranking must never silently
// flatten to input order).
type Weights struct {
	Funnel float64
	Deal   float64
	Jeff   float64
}

// CriterionVerdict is the public per-criterion explanation: the criterion
// text, its noul probability and whether it cleared the pass threshold.
// Emitted only for judged candidates — an unjudged candidate has no
// verdicts to explain.
type CriterionVerdict struct {
	Criterion string  `json:"criterion"`
	Prob      float64 `json:"prob"`
	Pass      bool    `json:"pass"`
}

// DealSignals reports the raw ADR-17 deal inputs behind the deal score —
// emitted for transparency, never used to recompute anything downstream.
type DealSignals struct {
	DiscountPct *float64 `json:"discount_pct,omitempty"`
	Thumbs      *int     `json:"thumbs,omitempty"`
	Rating      *float64 `json:"rating,omitempty"`
}

// empty reports whether no deal signal is present (the field is then
// omitted from the payload entirely).
func (d DealSignals) empty() bool {
	return d.DiscountPct == nil && d.Thumbs == nil && d.Rating == nil
}

// Result is one ranked candidate: the judged candidate record plus its
// fused score, confidence bucket and explanation payload. Judged stays
// internal — the MCP surface projects through the egress allowlist, never
// serializing this struct raw.
type Result struct {
	Judged          match.JudgedCandidate `json:"-"`
	Score           float64               `json:"score"`
	Confidence      score.ConfidenceLevel `json:"confidence"`
	MatchedCriteria []CriterionVerdict    `json:"matched_criteria,omitempty"`
	DealSignals     *DealSignals          `json:"deal_signals,omitempty"`
	ExcludedReason  string                `json:"excluded_reason,omitempty"`
	UnjudgedReason  string                `json:"unjudged_reason,omitempty"`
}

// Rank fuses the signals and returns the ordered results: non-excluded
// candidates sorted by fused score, then the excluded tail in funnel-score
// order (an excluded candidate never outranks a scored one — mirrors the
// match stage's own ordering rule). passMin is the per-criterion noul
// threshold (JEFF_MATCH_MIN) used for the verdict booleans; it gets the
// same clamp match.New applies (out-of-range → 0.55) so the explanation's
// pass booleans agree with JudgedCandidate.Passed.
func Rank(cands []match.JudgedCandidate, questions []match.Question, degraded bool, w Weights, passMin float64) []Result {
	if passMin <= 0 || passMin > 1 {
		passMin = 0.55 // match.defaultMatchMin
	}
	out := make([]Result, len(cands))
	for i, jc := range cands {
		out[i] = Result{
			Judged:          jc,
			MatchedCriteria: explainCriteria(jc, questions, passMin),
			ExcludedReason:  jc.ExcludeReason,
			UnjudgedReason:  jc.UnjudgedReason,
		}
		if ds := dealSignalsOf(jc); !ds.empty() {
			out[i].DealSignals = &ds
		}
	}

	funnel, deal, jeff := signalLists(cands)
	w = normalize(w, degraded || len(jeff) == 0)

	fused := rerank.LinearMinMax(
		[]float64{w.Funnel, w.Deal, w.Jeff},
		funnel, deal, jeff,
	)
	scores := make(map[string]float64, len(fused))
	for _, f := range fused {
		scores[f.ID] = f.Score
	}

	ranked, excluded := splitRanked(out)
	for i := range ranked {
		s := scores[strconv.Itoa(ranked[i].idx)]
		ranked[i].res.Score = s
		ranked[i].res.Confidence = score.ConfidenceFromScore(s)
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		return ranked[a].res.Score > ranked[b].res.Score
	})
	sort.SliceStable(excluded, func(a, b int) bool {
		return excluded[a].res.Judged.Score > excluded[b].res.Judged.Score
	})

	final := make([]Result, 0, len(out))
	for _, r := range ranked {
		final = append(final, r.res)
	}
	for _, r := range excluded {
		final = append(final, r.res)
	}
	return final
}

// scoredPair carries a result plus its original candidate index — the
// fusion ID space is the input index, so sorting needs the back-reference.
type scoredPair struct {
	idx int
	res Result
}

// splitRanked partitions results into the scoreable (non-excluded) and
// excluded sets, each preserving input index for the fusion ID mapping.
func splitRanked(out []Result) (ranked, excluded []scoredPair) {
	for i, r := range out {
		if r.Judged.Excluded {
			excluded = append(excluded, scoredPair{i, r})
		} else {
			ranked = append(ranked, scoredPair{i, r})
		}
	}
	return ranked, excluded
}

// signalLists builds the three ScoredIDLists for LinearMinMax, keyed by
// input index. Excluded candidates join no list — they are never ranked.
// The jeff list carries only candidates with verdicts (mean noul prob);
// unjudged candidates have no entry and so no jeff contribution.
func signalLists(cands []match.JudgedCandidate) (funnel, deal, jeff rerank.ScoredIDList) {
	for i, jc := range cands {
		if jc.Excluded {
			continue
		}
		id := strconv.Itoa(i)
		funnel = append(funnel, rerank.ScoredID{ID: id, Score: jc.Score})
		deal = append(deal, rerank.ScoredID{ID: id, Score: dealScore(jc)})
		if len(jc.Verdicts) > 0 {
			jeff = append(jeff, rerank.ScoredID{ID: id, Score: meanProb(jc.Verdicts)})
		}
	}
	return funnel, deal, jeff
}

// normalize rescales weights to sum 1 over the active signals. jeffOff
// (degraded batch or zero judged candidates) drops the jeff term so the
// feature signals renormalize onto the full [0,1] scale — otherwise every
// score would top out below the jeff share and read artificially low.
// All-zero or invalid (negative/NaN/Inf) input falls back to the funnel
// signal alone.
func normalize(w Weights, jeffOff bool) Weights {
	if jeffOff {
		w.Jeff = 0
	}
	for _, v := range []float64{w.Funnel, w.Deal, w.Jeff} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return Weights{Funnel: 1}
		}
	}
	total := w.Funnel + w.Deal + w.Jeff
	if total <= 0 {
		return Weights{Funnel: 1}
	}
	return Weights{Funnel: w.Funnel / total, Deal: w.Deal / total, Jeff: w.Jeff / total}
}

// dealScore composes the ADR-17 signals into [0,1] over the signals the
// candidate carries: discount_pct /100, thumbs on a saturating log scale,
// rating /5. A candidate with no deal signals scores 0 — the fused score
// then rests on funnel + jeff alone.
func dealScore(jc match.JudgedCandidate) float64 {
	var sum, wsum float64
	if jc.DiscountPct != nil {
		sum += wDiscount * clamp01(*jc.DiscountPct/100)
		wsum += wDiscount
	}
	if jc.Thumbs != nil && *jc.Thumbs > 0 {
		sum += wThumbs * clamp01(math.Log1p(float64(*jc.Thumbs))/math.Log1p(thumbsSaturation))
		wsum += wThumbs
	}
	if jc.Product.Rating != nil {
		sum += wRating * clamp01(*jc.Product.Rating/ratingScale)
		wsum += wRating
	}
	if wsum == 0 {
		return 0
	}
	return sum / wsum
}

// dealSignalsOf lifts the raw deal inputs off the candidate for the
// explanation payload.
func dealSignalsOf(jc match.JudgedCandidate) DealSignals {
	return DealSignals{
		DiscountPct: jc.DiscountPct,
		Thumbs:      jc.Thumbs,
		Rating:      jc.Product.Rating,
	}
}

// explainCriteria pairs each plan question with the candidate's verdict,
// in plan order. Unjudged candidates yield nil — there are no verdicts to
// explain.
func explainCriteria(jc match.JudgedCandidate, questions []match.Question, passMin float64) []CriterionVerdict {
	if len(jc.Verdicts) == 0 {
		return nil
	}
	out := make([]CriterionVerdict, 0, len(questions))
	for _, q := range questions {
		p, ok := jc.Verdicts[q.ID]
		if !ok {
			continue
		}
		out = append(out, CriterionVerdict{
			Criterion: q.Criterion,
			Prob:      p,
			Pass:      p >= passMin,
		})
	}
	return out
}

// meanProb averages the noul verdicts — the jeff fusion input.
func meanProb(verdicts map[string]float64) float64 {
	var sum float64
	for _, p := range verdicts {
		sum += p
	}
	return sum / float64(len(verdicts))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
