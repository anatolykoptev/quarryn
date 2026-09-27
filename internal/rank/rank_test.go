package rank

import (
	"testing"

	"github.com/anatolykoptev/go-kit/score"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/match"
)

var defaultWeights = Weights{Funnel: 0.3, Deal: 0.2, Jeff: 0.5}

func f64(v float64) *float64 { return &v }

func iminor(v int64) *int64 { return &v }

func i64(v int) *int { return &v }

// judged builds a judged candidate: funnel score + verdicts.
func judged(url string, funnel float64, verdicts map[string]float64) match.JudgedCandidate {
	var ec extract.EnrichedCandidate
	ec.Title = url
	ec.URL = url
	ec.Score = funnel
	ec.Product = extract.Product{
		Name: url, URL: url, PriceMinor: iminor(10000), Currency: "USD",
		Availability: "in_stock", Source: "shop.example",
	}
	return match.JudgedCandidate{EnrichedCandidate: ec, Verdicts: verdicts, Passed: true}
}

var oneCriterion = []match.Question{{ID: "c0", Criterion: "good sound", Instruction: "Does this product satisfy: good sound?"}}

// TestRankJeffShiftsOrdering — the jeff signal must be able to outrank a
// stronger funnel signal: B has the better verdict, A the better funnel
// score, and with the jeff share active B wins.
func TestRankJeffShiftsOrdering(t *testing.T) {
	cands := []match.JudgedCandidate{
		judged("http://a.example/1", 0.9, map[string]float64{"c0": 0.30}),
		judged("http://b.example/2", 0.5, map[string]float64{"c0": 0.95}),
	}
	res := Rank(cands, oneCriterion, false, defaultWeights, 0.55)
	if res[0].Judged.URL != "http://b.example/2" {
		t.Fatalf("jeff signal did not lift B: %+v", res)
	}
	// Explanation rides along on the winner.
	if len(res[0].MatchedCriteria) != 1 || res[0].MatchedCriteria[0].Criterion != "good sound" ||
		res[0].MatchedCriteria[0].Prob != 0.95 || !res[0].MatchedCriteria[0].Pass {
		t.Fatalf("explanation = %+v", res[0].MatchedCriteria)
	}
	if res[1].MatchedCriteria[0].Pass {
		t.Fatal("A's 0.30 verdict must not pass the 0.55 threshold")
	}
}

// TestRankDegradedDropsJeff — the same pair under a degraded batch: the
// jeff weight must go to 0 so ranking is feature-only and A (stronger
// funnel) wins.
func TestRankDegradedDropsJeff(t *testing.T) {
	cands := []match.JudgedCandidate{
		judged("http://a.example/1", 0.9, map[string]float64{"c0": 0.30}),
		judged("http://b.example/2", 0.5, map[string]float64{"c0": 0.95}),
	}
	res := Rank(cands, oneCriterion, true, defaultWeights, 0.55)
	if res[0].Judged.URL != "http://a.example/1" {
		t.Fatalf("degraded batch still ranked on jeff: %+v", res)
	}
}

// TestRankUnjudgedRenormalizes — with nobody judged (degrade-mode matcher)
// the funnel+deal shares renormalize onto the full scale: the top
// candidate can still reach ConfidenceHigh instead of capping at the
// missing jeff share.
func TestRankUnjudgedRenormalizes(t *testing.T) {
	a := judged("http://a.example/1", 1.0, nil)
	a.Thumbs = i64(500)
	b := judged("http://b.example/2", 0.1, nil)
	res := Rank([]match.JudgedCandidate{a, b}, nil, true, defaultWeights, 0.55)
	if res[0].Judged.URL != "http://a.example/1" {
		t.Fatalf("order = %+v", res)
	}
	if res[0].Score < 0.9 || res[0].Confidence != score.ConfidenceHigh {
		t.Fatalf("renormalized top score = %v (%s), want ~1.0 high", res[0].Score, res[0].Confidence)
	}
	if res[0].DealSignals == nil || *res[0].DealSignals.Thumbs != 500 {
		t.Fatalf("deal signals missing: %+v", res[0].DealSignals)
	}
}

// TestRankDealSignalsShift — equal funnel scores, no jeff: the
// discount/thumbs-rich candidate must win on the deal signal alone.
func TestRankDealSignalsShift(t *testing.T) {
	a := judged("http://a.example/1", 0.8, nil)
	b := judged("http://b.example/2", 0.8, nil)
	b.DiscountPct = f64(45)
	b.Thumbs = i64(180)
	b.Product.Rating = f64(4.8)
	res := Rank([]match.JudgedCandidate{a, b}, nil, false, defaultWeights, 0.55)
	if res[0].Judged.URL != "http://b.example/2" {
		t.Fatalf("deal signal did not lift B: %+v", res)
	}
	ds := res[0].DealSignals
	if ds == nil || *ds.DiscountPct != 45 || *ds.Thumbs != 180 {
		t.Fatalf("deal signals = %+v", ds)
	}
}

// TestRankExcludedTail — an excluded candidate keeps its reason and sorts
// below every scored candidate regardless of funnel score.
func TestRankExcludedTail(t *testing.T) {
	ex := judged("http://x.example/9", 99.0, nil)
	ex.Excluded = true
	ex.ExcludeReason = "price_above_max"
	ex.Passed = false
	res := Rank([]match.JudgedCandidate{
		judged("http://a.example/1", 0.5, nil),
		ex,
	}, nil, false, defaultWeights, 0.55)
	if res[len(res)-1].Judged.URL != "http://x.example/9" {
		t.Fatalf("excluded candidate outranked a scored one: %+v", res)
	}
	if res[1].ExcludedReason != "price_above_max" {
		t.Fatalf("excluded_reason = %+v", res[1])
	}
	if res[1].Score != 0 {
		t.Fatal("excluded candidate must carry no fused score")
	}
}

// TestRankUnjudgedExplanation — an unjudged non-excluded candidate gets no
// matched_criteria but keeps its unjudged reason visible.
func TestRankUnjudgedExplanation(t *testing.T) {
	c := judged("http://a.example/1", 0.8, nil)
	c.UnjudgedReason = "jeff_saturated"
	res := Rank([]match.JudgedCandidate{c}, oneCriterion, true, defaultWeights, 0.55)
	if len(res[0].MatchedCriteria) != 0 {
		t.Fatalf("unjudged candidate gained criteria: %+v", res[0].MatchedCriteria)
	}
	if res[0].UnjudgedReason != "jeff_saturated" {
		t.Fatalf("unjudged_reason = %+v", res[0])
	}
}

// TestRankZeroWeightFallback — a degenerate all-zero weight config must not
// flatten ranking to input order; the funnel signal takes over alone.
func TestRankZeroWeightFallback(t *testing.T) {
	res := Rank([]match.JudgedCandidate{
		judged("http://a.example/1", 0.2, nil),
		judged("http://b.example/2", 0.9, nil),
	}, nil, false, Weights{}, 0.55)
	if res[0].Judged.URL != "http://b.example/2" {
		t.Fatalf("zero weights did not fall back to funnel order: %+v", res)
	}
}

// TestRankSingleCandidate — a one-candidate batch must not panic or lose
// the result (single-item fusion lists contribute 0 — the score is flat by
// construction and that's correct).
func TestRankSingleCandidate(t *testing.T) {
	res := Rank([]match.JudgedCandidate{
		judged("http://a.example/1", 0.8, map[string]float64{"c0": 0.9}),
	}, oneCriterion, false, defaultWeights, 0.55)
	if len(res) != 1 || res[0].Judged.URL != "http://a.example/1" {
		t.Fatalf("res = %+v", res)
	}
}
