package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLivePG is the env-gated live smoke: PG_LIVE_DSN=postgres://… makes
// it connect, run migrations and round-trip one feedback row against the
// real shared instance. Skipped by default so unit runs never need a DB.
func TestLivePG(t *testing.T) {
	dsn := os.Getenv("PG_LIVE_DSN")
	if dsn == "" {
		t.Skip("PG_LIVE_DSN unset — live postgres smoke skipped")
	}
	ctx := context.Background()
	db, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	ts := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)
	if err := db.AppendFeedback(ctx, ts, "live-smoke", "https://example.com/p/1", "good"); err != nil {
		t.Fatalf("AppendFeedback: %v", err)
	}
	row := db.pool.QueryRow(ctx,
		`SELECT request_id, picked_url, verdict FROM feedback WHERE request_id = $1`, "live-smoke")
	var rid, url, verdict string
	if err := row.Scan(&rid, &url, &verdict); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rid != "live-smoke" || url != "https://example.com/p/1" || verdict != "good" {
		t.Fatalf("row = %q %q %q", rid, url, verdict)
	}
}
