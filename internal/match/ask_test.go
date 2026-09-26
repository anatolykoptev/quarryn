package match

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/jeff"
	"github.com/anatolykoptev/go-product-search/internal/extract"
	"github.com/anatolykoptev/go-product-search/internal/money"
)

func f64m(v float64) *int64 { m, _ := money.FromFloat(v, "USD"); return &m }

func enriched(name, url string, price float64) extract.EnrichedCandidate {
	var ec extract.EnrichedCandidate
	ec.Title = name
	ec.URL = url
	ec.Score = 0.8
	ec.Product = extract.Product{
		Name: name, URL: url, PriceMinor: f64m(price), Currency: "USD",
		Availability: "in_stock", Source: "shop.example",
	}
	return ec
}

// wireReq mirrors jeff.Request for shape assertions without importing the
// wire type back out of the adapter under test.
type wireReq struct {
	State     map[string]any          `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// packedAskStub is the fake /v1/systemone handler: it asserts the packed
// request shape (N noul questions + CandidateState-only state) on every
// call and answers a fixed verdict set.
func packedAskStub(t *testing.T, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer testkey" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var gotReq wireReq
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		assertWireShape(t, gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jeff.Response{
			Answers: map[string]jeff.Answer{
				"c0": {Type: "noul", Noul: 0.9},
				"c1": {Type: "noul", Noul: 0.4},
				"c2": {Type: "noul", Noul: 0.7},
			},
		})
	}
}

// assertWireShape checks the ADR-4/ADR-11 invariants on one request: 3
// noul questions with the fixed instruction template, and a state dict
// carrying only CandidateState fields.
func assertWireShape(t *testing.T, gotReq wireReq) {
	if len(gotReq.Questions) != 3 {
		t.Errorf("questions = %+v", gotReq.Questions)
	}
	for id, q := range gotReq.Questions {
		if q.Type != "noul" {
			t.Errorf("question %s type = %q, want noul", id, q.Type)
		}
		if !strings.HasPrefix(q.Instructions, "Does this product satisfy: ") {
			t.Errorf("question %s instruction = %q, want fixed template", id, q.Instructions)
		}
	}
	allowedState := map[string]bool{
		"name": true, "price": true, "currency": true, "availability": true,
		"condition": true, "rating": true, "source_domain": true, "blurb": true,
	}
	for k := range gotReq.State {
		if !allowedState[k] {
			t.Errorf("state field %q leaked to jeff", k)
		}
	}
	if gotReq.State["name"] == nil || gotReq.State["price"] == nil {
		t.Errorf("state missing expected fields: %v", gotReq.State)
	}
}

// TestPackedAskOneCallPerCandidate asserts ADR-4's core wire shape: ONE
// HTTP call per candidate, N criterion nouls in the questions dict, and a
// state carrying only CandidateState fields.
func TestPackedAskOneCallPerCandidate(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(packedAskStub(t, &calls))
	defer srv.Close()

	m, err := New(Config{URL: srv.URL, Token: "testkey", Min: 0.55})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plan, err := PlanCriteria([]string{"good battery", "light enough", "usb-c"})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
		enriched("B", "http://b.example/2", 200),
	}, plan)

	if calls.Load() != 2 {
		t.Fatalf("jeff calls = %d, want exactly 2 (one packed Ask per candidate)", calls.Load())
	}
	// Both candidates judged: verdicts carry all three criterion IDs.
	for _, jc := range res.Candidates {
		if jc.UnjudgedReason != "" || len(jc.Verdicts) != 3 {
			t.Fatalf("candidate not judged: %+v", jc)
		}
		if jc.Verdicts["c0"] != 0.9 {
			t.Fatalf("verdicts = %v", jc.Verdicts)
		}
	}
	// 0.4 noul on c1 is below min 0.55 → both fail the provisional gate.
	for _, jc := range res.Candidates {
		if jc.Passed {
			t.Fatal("candidate passed with a 0.4 verdict below threshold")
		}
	}
	if res.Degraded {
		t.Fatalf("clean run marked degraded: %s", res.DegradeReason)
	}
}

// fakeAsker injects jeff-layer outcomes without HTTP. failOn(i) marks
// request index i (1-based) as failing with err.
type fakeAsker struct {
	err    error
	prob   float64
	calls  *atomic.Int32
	failOn func(i int) bool
}

func (f fakeAsker) Ask(ctx context.Context, req jeff.Request) (*jeff.Response, error) {
	i := int(f.calls.Add(1))
	if f.failOn != nil {
		if !f.failOn(i) {
			return f.ok(req)
		}
		if f.err != nil {
			return nil, f.err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.ok(req)
}

func (f fakeAsker) ok(req jeff.Request) (*jeff.Response, error) {
	ans := make(map[string]jeff.Answer, len(req.Questions))
	for id := range req.Questions {
		ans[id] = jeff.Answer{Type: "noul", Noul: f.prob}
	}
	return &jeff.Response{Answers: ans}, nil
}

// noAnswerAsker returns a 200-shaped response missing the requested IDs —
// the ErrNoAnswer path.
type noAnswerAsker struct{}

func (noAnswerAsker) Ask(context.Context, jeff.Request) (*jeff.Response, error) {
	return &jeff.Response{Answers: map[string]jeff.Answer{}}, nil
}

func testMatcher(a Asker) *Matcher {
	return &Matcher{jeff: a, min: 0.55, maxCands: 20, conc: 3, callTimeout: 10 * time.Second}
}

// TestDegradeOn429 — a saturated jeff must flag Degraded and keep the
// deterministic ranking, never error the search.
func TestDegradeOn429(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{err: &jeff.StatusError{StatusCode: 429}, calls: &calls})
	plan, err := PlanCriteria([]string{"good battery"})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
		enriched("B", "http://b.example/2", 50),
	}, plan)
	if !res.Degraded {
		t.Fatal("429 flood did not degrade")
	}
	if !strings.Contains(res.DegradeReason, string(ReasonJeffSaturated)) {
		t.Fatalf("degrade reason = %q, want %s", res.DegradeReason, ReasonJeffSaturated)
	}
	for _, jc := range res.Candidates {
		if jc.UnjudgedReason != ReasonJeffSaturated {
			t.Fatalf("unjudged reason = %q", jc.UnjudgedReason)
		}
		if jc.Verdicts != nil {
			t.Fatal("degraded candidate carries verdicts")
		}
		if jc.MatchScore <= 0 {
			t.Fatal("degraded candidate lost deterministic score")
		}
		if jc.Passed {
			t.Fatal("unjudged candidate claims the gate")
		}
	}
	// Ranking still orders by deterministic strength.
	if res.Candidates[0].Product.Name != "A" {
		t.Fatalf("degraded order = %v", res.Candidates)
	}
}

// TestDegradeOnTimeout — per-call deadline exhausted → jeff_timeout.
func TestDegradeOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jeff.Response{Answers: map[string]jeff.Answer{}})
	}))
	defer srv.Close()

	m, err := New(Config{URL: srv.URL, Token: "t", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plan, _ := PlanCriteria([]string{"anything subjective"})
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
	}, plan)
	if !res.Degraded {
		t.Fatal("slow jeff did not degrade")
	}
	if res.Candidates[0].UnjudgedReason != ReasonJeffTimeout {
		t.Fatalf("unjudged reason = %q, want %s", res.Candidates[0].UnjudgedReason, ReasonJeffTimeout)
	}
}

// TestPartialDegrade — some candidates judged, some failed: judged keep
// verdicts, failed are flagged unjudged. Never hard-fail the whole search.
func TestPartialDegrade(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{
		err:    &jeff.StatusError{StatusCode: 529},
		prob:   0.9,
		calls:  &calls,
		failOn: func(i int) bool { return i%2 == 0 },
	})
	plan, _ := PlanCriteria([]string{"good battery"})
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 300),
		enriched("B", "http://b.example/2", 200),
		enriched("C", "http://c.example/3", 100),
	}, plan)
	if !res.Degraded {
		t.Fatal("partial failure did not flag degraded")
	}
	judged, unjudged := 0, 0
	for _, jc := range res.Candidates {
		if jc.UnjudgedReason == ReasonJeffSaturated {
			unjudged++
			if jc.Verdicts != nil {
				t.Fatal("failed candidate carries verdicts")
			}
		} else {
			judged++
			if len(jc.Verdicts) != 1 || !jc.Passed {
				t.Fatalf("judged candidate broken: %+v", jc)
			}
		}
	}
	if judged == 0 || unjudged == 0 {
		t.Fatalf("judged=%d unjudged=%d, want mixed", judged, unjudged)
	}
	// A judged candidate (fused score > 0.4·det alone) must outrank
	// unjudged ones.
	if res.Candidates[0].Verdicts == nil {
		t.Fatal("top-ranked candidate is unjudged — fusion ordering broken")
	}
}

// TestMissingAnswerDegrades — a 200 without an answer entry is a degrade,
// not a success.
func TestMissingAnswerDegrades(t *testing.T) {
	m := testMatcher(noAnswerAsker{})
	plan, _ := PlanCriteria([]string{"good battery"})
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
	}, plan)
	if !res.Degraded || res.Candidates[0].UnjudgedReason != ReasonJeffNoAnswer {
		t.Fatalf("missing answer not degraded: %+v", res.Candidates[0])
	}
}

// TestUnconfiguredMatcherDegrades — no JEFF_URL: everything deterministic.
func TestUnconfiguredMatcherDegrades(t *testing.T) {
	m, err := New(Config{Min: 0.55})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plan, _ := PlanCriteria([]string{"good battery"})
	res := m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
	}, plan)
	if !res.Degraded || res.DegradeReason == "" {
		t.Fatal("unconfigured jeff not flagged degraded")
	}
	if res.Candidates[0].UnjudgedReason != ReasonJeffUnconfigured {
		t.Fatalf("unjudged = %q", res.Candidates[0].UnjudgedReason)
	}
	// Degrade mode owes no jeff verdict — the prefilter pass stands.
	if !res.Candidates[0].Passed {
		t.Fatal("unconfigured candidate lost its prefilter pass verdict")
	}
}

// TestCallerCtxDeadlineWhileQueued — parent ctx death marks every
// straggler ctx_deadline instead of hanging. The fake asker blocks until
// ctx dies, so both the in-flight call and the queued candidate classify
// deterministically. Caller cancellation is not a jeff failure: the
// result is NOT Degraded and the candidates do not claim the gate.
func TestCallerCtxDeadlineWhileQueued(t *testing.T) {
	var calls atomic.Int32
	m := testMatcher(fakeAsker{
		calls:  &calls,
		failOn: func(int) bool { return true }, // block until ctx done, return ctx.Err
	})
	m.conc = 1 // serialize: one in flight, the rest queued
	m.callTimeout = 5 * time.Second
	plan, _ := PlanCriteria([]string{"good battery"})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		done <- m.Match(ctx, []extract.EnrichedCandidate{
			enriched("A", "http://a.example/1", 100),
			enriched("B", "http://b.example/2", 90),
		}, plan)
	}()
	time.Sleep(20 * time.Millisecond) // let the first candidate enter Ask
	cancel()
	select {
	case r := <-done:
		if r.Degraded {
			t.Fatal("caller ctx death flagged degraded — not a jeff failure")
		}
		for _, jc := range r.Candidates {
			if jc.UnjudgedReason != ReasonCtxDeadline {
				t.Fatalf("unjudged = %q, want %s", jc.UnjudgedReason, ReasonCtxDeadline)
			}
			if jc.Passed {
				t.Fatal("ctx-canceled candidate claims the gate")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Match hung on canceled ctx")
	}
}

// TestOutOfRangeNoulDegrades — a wire noul outside [0,1] (NaN included)
// is unusable: the candidate degrades to no_answer rather than poisoning
// MatchScore and the calibration histogram.
func TestOutOfRangeNoulDegrades(t *testing.T) {
	for _, prob := range []float64{-0.2, 1.7, math.NaN(), math.Inf(1)} {
		var calls atomic.Int32
		m := testMatcher(fakeAsker{prob: prob, calls: &calls})
		plan, _ := PlanCriteria([]string{"good battery"})
		res := m.Match(t.Context(), []extract.EnrichedCandidate{
			enriched("A", "http://a.example/1", 100),
		}, plan)
		if !res.Degraded || res.Candidates[0].UnjudgedReason != ReasonJeffNoAnswer {
			t.Fatalf("noul %v not degraded: %+v", prob, res.Candidates[0])
		}
		if res.Candidates[0].Verdicts != nil || res.Candidates[0].Passed {
			t.Fatalf("noul %v produced verdicts/pass: %+v", prob, res.Candidates[0])
		}
	}
}
