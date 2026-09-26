package api

import (
	"testing"

	"github.com/anatolykoptev/go-product-search/internal/extract"
	"github.com/anatolykoptev/go-product-search/internal/match"
	"github.com/anatolykoptev/go-product-search/internal/rank"
	pssources "github.com/anatolykoptev/go-product-search/internal/sources"
)

func i64p(v int64) *int64 { return &v }

func rankedPassed(url string, score float64) rank.Result {
	var ec extract.EnrichedCandidate
	ec.Title = url
	ec.URL = url
	ec.Product = extract.Product{
		Name: url, URL: url, PriceMinor: i64p(2999), Currency: "USD",
		Availability: "in_stock", Source: "shop.example",
	}
	return rank.Result{
		Judged: match.JudgedCandidate{EnrichedCandidate: ec, Passed: true},
		Score:  score,
	}
}

// TestBriefGroupsEveryCandidate — the four pipelines exits must partition
// the input exactly: every ranked result lands in finalists, excluded,
// rejected or unjudged, and no candidate appears in two groups.
func TestBriefGroupsEveryCandidate(t *testing.T) {
	var excluded rank.Result
	excluded.Judged.URL = "http://x.example/excluded"
	excluded.Judged.Excluded = true
	excluded.Judged.ExcludeReason = match.ExclPriceAboveMax
	excluded.Judged.ExcludeDetail = "price 500.00 > max 100.00"

	var unjudged rank.Result
	unjudged.Judged.URL = "http://x.example/unjudged"
	unjudged.Judged.UnjudgedReason = match.ReasonOverCandidateCap

	var rejected rank.Result
	rejected.Judged.URL = "http://x.example/rejected"
	rejected.MatchedCriteria = []rank.CriterionVerdict{
		{Criterion: "good battery", Pass: false, Prob: 0.2},
		{Criterion: "lightweight", Pass: true, Prob: 0.9},
	}

	ranked := append([]rank.Result{rankedPassed("http://x.example/win", 0.9)},
		excluded, unjudged, rejected)
	b := composeBrief("q", match.Plan{}, ranked, nil)

	if len(b.Finalists) != 1 || b.Finalists[0].URL != "http://x.example/win" {
		t.Fatalf("finalists: %+v", b.Finalists)
	}
	if len(b.Excluded) != 1 || b.Excluded[0].Code != "price_above_max" ||
		b.Excluded[0].Detail != "price 500.00 > max 100.00" {
		t.Fatalf("excluded must carry code+detail verbatim: %+v", b.Excluded)
	}
	if len(b.Unjudged) != 1 || b.Unjudged[0].Code != "over_candidate_cap" {
		t.Fatalf("unjudged: %+v", b.Unjudged)
	}
	if len(b.Rejected) != 1 || len(b.Rejected[0].FailedCriteria) != 1 ||
		b.Rejected[0].FailedCriteria[0] != "good battery" {
		t.Fatalf("rejected must name only the failed criterion: %+v", b.Rejected)
	}
}

// TestBriefFinalistCapDoesNotReject — a passed candidate beyond the top-5
// cap is simply not in the brief; it must not leak into `rejected` — that
// group is for judge failures only.
func TestBriefFinalistCapDoesNotReject(t *testing.T) {
	var ranked []rank.Result
	for i := 0; i < 7; i++ {
		ranked = append(ranked, rankedPassed("http://x.example/w"+string(rune('a'+i)), 0.9))
	}
	b := composeBrief("q", match.Plan{}, ranked, nil)
	if len(b.Finalists) != 5 || len(b.Rejected) != 0 {
		t.Fatalf("cap must clip finalists silently, not rebrand them: f=%d r=%d",
			len(b.Finalists), len(b.Rejected))
	}
}

// TestBriefEchoesInterpretation — the query, parsed constraints and
// subjective criteria text must ride the brief so a consumer never
// re-parses the criteria syntax to learn what was actually asked.
func TestBriefEchoesInterpretation(t *testing.T) {
	price := 100.0
	plan := match.Plan{
		Constraints: match.Constraints{PriceMax: &price, Currency: "USD"},
		Questions:   []match.Question{{ID: "c0", Criterion: "good sound"}},
	}
	cov := []pssources.SourceStatus{{Name: "ebay", Outcome: "ok", Count: 3}}
	b := composeBrief("jbl speaker", plan, nil, cov)
	if b.Query != "jbl speaker" || b.Constraints.Currency != "USD" ||
		len(b.Subjective) != 1 || b.Subjective[0] != "good sound" ||
		len(b.Coverage) != 1 || b.Coverage[0].Name != "ebay" {
		t.Fatalf("brief must echo interpretation+coverage: %+v", b)
	}
}
