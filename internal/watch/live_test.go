package watch

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/quarryn/internal/postgres"
)

// TestLivePGWatches is the env-gated smoke against the real PG18:
// migration 0002 applied → create/list/get/cancel round-trip.
func TestLivePGWatches(t *testing.T) {
	dsn := os.Getenv("PG_LIVE_DSN")
	if dsn == "" {
		t.Skip("PG_LIVE_DSN unset — live postgres smoke skipped")
	}
	ctx := context.Background()
	db, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	st := NewStore(db.Pool())
	w := &Watch{
		Kind:             KindOffer,
		URL:              "https://example.com/deal/1",
		OfferID:          "url|deadbeef",
		Label:            "live smoke",
		TargetPriceMinor: 999,
		Currency:         "USD",
		Interval:         time.Hour,
		ExpiresAt:        time.Now().Add(time.Hour),
		Status:           StatusActive,
	}
	if err := st.Create(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetPriceMinor != 999 || got.Kind != KindOffer {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := st.Record(ctx, &got, Observation{
		Outcome: OutcomeOK, Currency: "USD", Detail: "smoke",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	ok, err := st.Cancel(ctx, w.ID)
	if err != nil || !ok {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
}
