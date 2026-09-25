package match

import (
	"bytes"
	"log/slog"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-product-search/internal/extract"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// captureSlog redirects the default logger — the sink jeff_gate events
// write to — into a buffer for the duration of the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestNewRequestIDFormat — the calibration id is a random RFC 4122 v4
// uuid, unique per call.
func TestNewRequestIDFormat(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if !uuidV4.MatchString(a) {
		t.Fatalf("not a v4 uuid: %q", a)
	}
	if a == b {
		t.Fatal("request ids must be unique")
	}
}

// TestJeffGateCarriesCallerRequestID — the ADR-6 pairing: a request id on
// ctx lands on every jeff_gate event of the Match call, joining log line
// to the id the caller received.
func TestJeffGateCarriesCallerRequestID(t *testing.T) {
	buf := captureSlog(t)
	var calls atomic.Int32
	m := testMatcher(fakeAsker{prob: 0.9, calls: &calls})
	plan, err := PlanCriteria([]string{"good value"})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	ctx := WithRequestID(t.Context(), "11111111-2222-4333-8444-555566667777")
	res := m.Match(ctx, []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
	}, plan)
	if len(res.Candidates) != 1 || res.Candidates[0].Verdicts == nil {
		t.Fatalf("candidate not judged: %+v", res.Candidates)
	}
	want := "request_id=11111111-2222-4333-8444-555566667777"
	if !bytes.Contains(buf.Bytes(), []byte("jeff_gate")) ||
		!bytes.Contains(buf.Bytes(), []byte(want)) {
		t.Fatalf("jeff_gate log lacks %q:\n%s", want, buf.String())
	}
}

// TestJeffGateFallbackRequestID — a direct Match call with no attached id
// still emits a correlatable request_id (the m<N> sequence), never an
// empty field.
func TestJeffGateFallbackRequestID(t *testing.T) {
	buf := captureSlog(t)
	var calls atomic.Int32
	m := testMatcher(fakeAsker{prob: 0.9, calls: &calls})
	plan, err := PlanCriteria([]string{"good value"})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	m.Match(t.Context(), []extract.EnrichedCandidate{
		enriched("A", "http://a.example/1", 100),
	}, plan)
	if !regexp.MustCompile(`request_id=m\d+`).Match(buf.Bytes()) {
		t.Fatalf("jeff_gate fallback id missing:\n%s", buf.String())
	}
}
