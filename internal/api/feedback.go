package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-product-search/internal/postgres"
)

// feedback field caps bound the log line a single call can emit — feedback
// is append-only and unsanitized input must not grow unbounded.
const (
	feedbackMaxRequestID = 200
	feedbackMaxURL       = 2048
	feedbackMaxVerdict   = 100
	feedbackMaxBody      = 8 << 10
)

// FeedbackStore is the ADR-10 outcome sink: one JSONL line per picked
// listing — the outcome half of the ADR-6 calibration pair that joins the
// jeff_gate log events on request_id. Append-only; no rotation in v1 (the
// file grows by one short line per human feedback action — trivially
// small). When FEEDBACK_FILE's directory is unwritable the store degrades
// to log-only: records are logged via slog and the API still acks.
type FeedbackStore struct {
	mu   sync.Mutex
	path string
	f    *os.File     // nil → log-only mode
	pg   *postgres.DB // non-nil → Postgres is the primary sink
	now  func() time.Time
}

// feedbackRecord is one JSONL line.
type feedbackRecord struct {
	TS        string `json:"ts"` // RFC3339 UTC
	RequestID string `json:"request_id"`
	PickedURL string `json:"picked_url"`
	Verdict   string `json:"verdict,omitempty"`
}

// NewFeedbackStore opens path for append (creating parent dir + file). An
// unwritable path is not fatal — the store returns in log-only mode and
// warns once at startup. An empty path selects log-only directly.
func NewFeedbackStore(path string) *FeedbackStore {
	s := &FeedbackStore{path: path, now: time.Now}
	if strings.TrimSpace(path) == "" {
		slog.Warn("feedback: FEEDBACK_FILE empty — records will be logged, not persisted")
		return s
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("feedback: directory unavailable — running log-only",
			slog.String("path", path), slog.Any("error", err))
		return s
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("feedback: file unavailable — running log-only",
			slog.String("path", path), slog.Any("error", err))
		return s
	}
	s.f = f
	slog.Info("feedback: appending outcomes", slog.String("path", path))
	return s
}

// NewFeedbackStorePG is NewFeedbackStore with a Postgres primary sink
// (DATABASE_URL wired). File/log remains as the loud fallback when a pg
// write fails — outcome data is calibration input, better duplicated
// than lost.
func NewFeedbackStorePG(path string, db *postgres.DB) *FeedbackStore {
	s := NewFeedbackStore(path)
	s.pg = db
	slog.Info("feedback: primary sink is postgres")
	return s
}

// Append writes one record. A write failure is logged loudly AND returned —
// "log write failures, never silent". In log-only mode the record is an
// INFO log line and nil is returned (the feedback is captured, just not on
// disk).
func (s *FeedbackStore) Append(rec feedbackRecord) error {
	ts := s.now().UTC()
	rec.TS = ts.Format(time.RFC3339)
	line, err := json.Marshal(rec)
	if err != nil {
		return err // impossible for this shape; still reported
	}
	if s.pg != nil {
		// Bounded: feedback is a sink, not the request's critical path.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.pg.AppendFeedback(ctx, ts, rec.RequestID, rec.PickedURL, rec.Verdict)
		cancel()
		if err == nil {
			return nil
		}
		// Loud fallback: record the failure AND keep the row on disk.
		slog.Error("feedback: pg write failed — falling back to file",
			slog.Any("error", err))
	}
	if s.f == nil {
		slog.Info("feedback",
			slog.String("request_id", rec.RequestID),
			slog.String("picked_url", rec.PickedURL),
			slog.String("verdict", rec.Verdict))
		return nil
	}
	s.mu.Lock()
	_, err = s.f.Write(append(line, '\n'))
	s.mu.Unlock()
	if err != nil {
		slog.Error("feedback: write failed",
			slog.String("path", s.path), slog.Any("error", err))
	}
	return err
}

// Close releases the file handle. The process-lifetime store in the server
// never calls it; tests do.
func (s *FeedbackStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
}

// clampFeedback trims caller strings to the bounded field caps.
func clampFeedback(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// FeedbackHandler serves POST /api/v1/feedback — the REST twin of the
// product_feedback tool, same store. Mounted on the bearer-authed mux.
func (s *FeedbackStore) FeedbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in productFeedbackInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, feedbackMaxBody)).Decode(&in); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if err := s.appendValidated(in); err != nil {
			writeFeedbackError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

// appendValidated is the shared validation + append for the tool and the
// REST route.
func (s *FeedbackStore) appendValidated(in productFeedbackInput) error {
	if strings.TrimSpace(in.RequestID) == "" {
		return errFeedback("request_id is required")
	}
	if strings.TrimSpace(in.PickedURL) == "" {
		return errFeedback("picked_url is required")
	}
	rec := feedbackRecord{
		RequestID: clampFeedback(strings.TrimSpace(in.RequestID), feedbackMaxRequestID),
		PickedURL: clampFeedback(strings.TrimSpace(in.PickedURL), feedbackMaxURL),
		Verdict:   clampFeedback(strings.TrimSpace(in.Verdict), feedbackMaxVerdict),
	}
	return s.Append(rec)
}

// feedbackError is a caller-facing validation failure.
type feedbackError struct{ msg string }

func errFeedback(msg string) error    { return feedbackError{msg} }
func (e feedbackError) Error() string { return e.msg }

func writeFeedbackError(w http.ResponseWriter, err error) {
	var fe feedbackError
	if errors.As(err, &fe) {
		body, _ := json.Marshal(map[string]string{"error": fe.msg})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
		return
	}
	slog.Error("feedback: append failed", slog.Any("error", err))
	http.Error(w, `{"error":"feedback write failed"}`, http.StatusInternalServerError)
}

// persisted reports whether the store writes to disk.
func (s *FeedbackStore) persisted() bool { return s.f != nil }
