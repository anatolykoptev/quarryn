package match

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/jeff"
	"github.com/anatolykoptev/go-product-search/internal/extract"
)

// perStateAsker returns per-criterion nouls keyed on the candidate state's
// name field — drives deterministic per-candidate verdicts.
type perStateAsker struct {
	calls *atomic.Int32
	probs map[string]float64
}

func (p perStateAsker) Ask(_ context.Context, req jeff.Request) (*jeff.Response, error) {
	p.calls.Add(1)
	st, _ := req.State.(CandidateState)
	ans := make(map[string]jeff.Answer, len(req.Questions))
	for id := range req.Questions {
		ans[id] = jeff.Answer{Type: "noul", Noul: p.probs[st.Name]}
	}
	return &jeff.Response{Answers: ans}, nil
}

// TestMatchDeterministicOnly — a plan with no subjective criteria never
// touches jeff at all.
func TestMatchDeterministicOnly(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{prob: 0.9, calls: &calls})
	plan, err := PlanCriteria([]string{"price_max:150", "keyword:sony"})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("Sony A", "http://a.example/1", 100),
		enriched("Sony B", "http://b.example/2", 300), // over price_max
	}, plan)
	if calls.Load() != 0 {
		t.Fatalf("deterministic plan hit jeff %d times", calls.Load())
	}
	if res.Degraded {
		t.Fatal("deterministic-only run flagged degraded")
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %d", len(res.Candidates))
	}
	// The in-budget candidate ranks first; the rejected one is flagged.
	if res.Candidates[0].Product.Name != "Sony A" || !res.Candidates[0].Passed {
		t.Fatalf("top = %+v", res.Candidates[0])
	}
	tail := res.Candidates[1]
	if !tail.Excluded || tail.ExcludeReason != ExclPriceAboveMax || tail.Passed {
		t.Fatalf("excluded candidate = %+v", tail)
	}
}

// TestMatchExclusionPaths — extraction failures and render-deferred
// candidates are flagged Excluded without consulting constraints.
func TestMatchExclusionPaths(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{prob: 0.9, calls: &calls})
	plan, _ := PlanCriteria([]string{"good battery"})

	failed := enriched("Broken", "http://x.example/1", 10)
	failed.ExtractionFailed = true
	failed.FailureReason = "missing price"
	deferred := enriched("Deferred", "http://x.example/2", 20)
	deferred.NeedsRender = true

	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		failed, deferred, enriched("Good", "http://x.example/3", 30),
	}, plan)
	if calls.Load() != 1 {
		t.Fatalf("jeff calls = %d, want 1 (only the healthy candidate)", calls.Load())
	}
	var seenExcluded, seenJudged int
	for _, jc := range res.Candidates {
		switch {
		case jc.Excluded && jc.ExcludeReason == ExclExtractionFailed:
			seenExcluded++
		case jc.Excluded && jc.ExcludeReason == ExclDeferredRender:
			seenExcluded++
		case !jc.Excluded && len(jc.Verdicts) == 1:
			seenJudged++
		}
	}
	if seenExcluded != 2 || seenJudged != 1 {
		t.Fatalf("excluded=%d judged=%d", seenExcluded, seenJudged)
	}
	// Excluded never outranks the judged candidate.
	if res.Candidates[len(res.Candidates)-1].Excluded != true {
		t.Fatal("excluded tail ordering broken")
	}
}

// TestMatchCandidateCap — eligible beyond MaxJeffCandidates are marked
// over_candidate_cap: visible, not degraded, deterministically scored.
func TestMatchCandidateCap(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{prob: 0.9, calls: &calls})
	m.maxCands = 1
	plan, _ := PlanCriteria([]string{"good battery"})

	weak := enriched("Weak", "http://a.example/1", 100)
	weak.Score = 0.1
	strong := enriched("Strong", "http://b.example/2", 200)
	strong.Score = 0.9

	res := m.Match(t.Context(), []extract.EnrichedCandidate{weak, strong}, plan)
	if calls.Load() != 1 {
		t.Fatalf("jeff calls = %d, want 1 (cap)", calls.Load())
	}
	if res.Degraded {
		t.Fatal("cap overflow flagged degraded — a budget decision is not a failure")
	}
	// Strong (higher funnel score) won the single jeff slot.
	byName := map[string]JudgedCandidate{}
	for _, jc := range res.Candidates {
		byName[jc.Product.Name] = jc
	}
	if len(byName["Strong"].Verdicts) != 1 {
		t.Fatalf("strong not judged: %+v", byName["Strong"])
	}
	if byName["Weak"].UnjudgedReason != "over_candidate_cap" {
		t.Fatalf("weak unjudged = %q", byName["Weak"].UnjudgedReason)
	}
	if !byName["Weak"].Passed {
		t.Fatal("over-cap candidate lost its prefilter pass verdict")
	}
	if byName["Weak"].MatchScore <= 0 {
		t.Fatal("over-cap candidate lost deterministic score")
	}
	if res.Candidates[0].Product.Name != "Strong" {
		t.Fatalf("fused ranking put %q first", res.Candidates[0].Product.Name)
	}
}

// TestMatchEmptyInput — zero candidates is an empty result, not an error
// and not a degrade.
func TestMatchEmptyInput(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{calls: &calls})
	plan, _ := PlanCriteria([]string{"anything"})
	res := m.Match(t.Context(), nil, plan)
	if len(res.Candidates) != 0 || res.Degraded || calls.Load() != 0 {
		t.Fatalf("empty input produced output: %+v", res)
	}
}

// TestMatchFusedOrdering — the provisional weighted sum orders a judged
// strong-verdict candidate above a deterministic-only one even when the
// funnel score favored the latter slightly.
func TestMatchFusedOrdering(t *testing.T) {
	var calls atomic.Int32
	// per-call verdicts keyed on the request's state name.
	m := &Matcher{jeff: perStateAsker{calls: &calls, probs: map[string]float64{
		"Winner": 0.95, "Loser": 0.1,
	}}, min: 0.55, maxCands: 20, conc: 4, callTimeout: 10 * time.Second}

	w := enriched("Winner", "http://a.example/1", 100)
	w.Score = 0.5
	l := enriched("Loser", "http://b.example/2", 100)
	l.Score = 1.0 // higher funnel score, terrible verdict

	plan, _ := PlanCriteria([]string{"good battery"})
	res := m.Match(t.Context(), []extract.EnrichedCandidate{w, l}, plan)
	if res.Candidates[0].Product.Name != "Winner" {
		t.Fatalf("fusion order = %q, %q", res.Candidates[0].Product.Name, res.Candidates[1].Product.Name)
	}
	if !res.Candidates[0].Passed {
		t.Fatal("winner must pass the provisional gate")
	}
	if res.Candidates[1].Passed {
		t.Fatal("loser passed a 0.1 verdict")
	}
}
