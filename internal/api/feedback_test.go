package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestFeedbackStoreAppendsJSONL — one tool call = one JSONL line carrying
// the calibration-pair fields (request_id joins the jeff_gate events).
func TestFeedbackStoreAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	s := NewFeedbackStore(path)
	t.Cleanup(s.Close)

	if !s.persisted() {
		t.Fatal("store should persist to a writable path")
	}
	if err := s.appendValidated(productFeedbackInput{
		RequestID: "req-42",
		PickedURL: "https://www.ebay.com/itm/123",
		Verdict:   "bought",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	s.Close() // flush before read

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rec map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(raw), &rec); err != nil {
		t.Fatalf("line is not JSONL: %q", raw)
	}
	if rec["request_id"] != "req-42" || rec["picked_url"] != "https://www.ebay.com/itm/123" ||
		rec["verdict"] != "bought" || rec["ts"] == "" {
		t.Fatalf("record = %v", rec)
	}
}

// TestFeedbackStoreBadDirLogOnly — an unwritable FEEDBACK_FILE degrades to
// log-only: Append still succeeds (the record is logged, not persisted).
func TestFeedbackStoreBadDirLogOnly(t *testing.T) {
	// A path whose parent is a regular FILE makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewFeedbackStore(filepath.Join(blocker, "feedback.jsonl"))
	t.Cleanup(s.Close)
	if s.persisted() {
		t.Fatal("unwritable path must degrade to log-only")
	}
	if err := s.appendValidated(productFeedbackInput{
		RequestID: "req-1", PickedURL: "https://x.example.com/p",
	}); err != nil {
		t.Fatalf("log-only append must not error: %v", err)
	}
}

// TestProductFeedbackTool — the MCP path validates input and appends.
func TestProductFeedbackTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fb.jsonl")
	d := deps{feedback: NewFeedbackStore(path)}
	t.Cleanup(d.feedback.Close)

	res, err := handleProductFeedback(d, productFeedbackInput{})
	if err != nil || !res.IsError {
		t.Fatalf("empty input must be a tool error: %v %+v", err, res)
	}
	res, err = handleProductFeedback(d, productFeedbackInput{
		RequestID: "req-7", PickedURL: "https://x.example.com/p/1",
	})
	if err != nil || res.IsError {
		t.Fatalf("valid call failed: %v %+v", err, res)
	}
	var out feedbackResponse
	decodeResult(t, res, &out)
	if !out.OK || !out.Persisted {
		t.Fatalf("response = %+v", out)
	}
}

// TestProductFeedbackNilStore — deps without a store (unit tests, wiring
// gaps) degrade to log-only and still ack.
func TestProductFeedbackNilStore(t *testing.T) {
	res, err := handleProductFeedback(deps{}, productFeedbackInput{
		RequestID: "req-9", PickedURL: "https://x.example.com/p/1",
	})
	if err != nil || res.IsError {
		t.Fatalf("nil store must ack: %v %+v", err, res)
	}
	var out feedbackResponse
	decodeResult(t, res, &out)
	if !out.OK || out.Persisted {
		t.Fatalf("response = %+v", out)
	}
}

// TestFeedbackHTTPHandler — POST /api/v1/feedback shares the store.
func TestFeedbackHTTPHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fb.jsonl")
	s := NewFeedbackStore(path)
	t.Cleanup(s.Close)
	h := s.FeedbackHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback",
		bytes.NewReader([]byte(`{"request_id":"req-http","picked_url":"https://x.example.com/p","verdict":"bad_match"}`)))
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d body %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feedback",
		bytes.NewReader([]byte(`{"picked_url":"https://x.example.com/p"}`)))
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing request_id status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feedback",
		bytes.NewReader([]byte(`not json`)))
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", rec.Code)
	}
}
