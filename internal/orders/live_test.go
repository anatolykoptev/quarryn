package orders

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go-product-search/internal/postgres"
)

func TestLivePGOrders(t *testing.T) {
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
	o, created, err := Ingest(ctx, st, []byte(amazonEML))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !created {
		t.Fatal("first ingest must create")
	}
	// Re-ingest the same email — dedupe must merge, not duplicate.
	o2, created2, err := Ingest(ctx, st, []byte(amazonShipEML))
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if created2 {
		t.Fatal("re-sent same order must merge, not create")
	}
	if o2.ID != o.ID {
		t.Errorf("merged onto different row: %d != %d", o2.ID, o.ID)
	}
	if o2.TrackingNo != "1Z999AA10123456784" {
		t.Errorf("merge did not add tracking: %q", o2.TrackingNo)
	}
	if o2.Status != StatusShipped {
		t.Errorf("status after tracking merge = %q, want shipped", o2.Status)
	}
	_, evs, err := st.Get(ctx, o.ID)
	if err != nil || len(evs) < 2 {
		t.Fatalf("events: %v %d", err, len(evs))
	}
	ok, err := st.Mark(ctx, o.ID, StatusDelivered)
	if err != nil || !ok {
		t.Fatalf("mark: ok=%v err=%v", ok, err)
	}
}
