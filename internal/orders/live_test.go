package orders

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/quarryn/internal/postgres"
)

func pgConnect(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := os.Getenv("PG_LIVE_DSN")
	if dsn == "" {
		t.Skip("PG_LIVE_DSN unset — live postgres smoke skipped")
	}
	db, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestLivePGOrders(t *testing.T) {
	ctx := context.Background()
	db := pgConnect(t)

	st := NewStore(db.Pool())
	// Unique order_no per run — a rerun must not collide with a stale row.
	suffix := fmt.Sprintf("%03d-%07d-%07d",
		time.Now().UnixNano()%1000, time.Now().UnixNano()/1e3%1e7, time.Now().UnixNano()/1e10%1e7)
	amz := strings.Replace(amazonEML, "112-1234567-7654321", suffix, 1)
	amzShip := strings.Replace(amazonShipEML, "112-1234567-7654321", suffix, 1)
	o, created, err := Ingest(ctx, st, []byte(amz), "")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !created {
		t.Fatal("first ingest must create")
	}
	// Re-ingest the same email — dedupe must merge, not duplicate.
	o2, created2, err := Ingest(ctx, st, []byte(amzShip), "")
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
	_, evs, err := st.Get(ctx, "", o.ID)
	if err != nil || len(evs) < 2 {
		t.Fatalf("events: %v %d", err, len(evs))
	}
	ok, err := st.Mark(ctx, "", o.ID, StatusDelivered)
	if err != nil || !ok {
		t.Fatalf("mark: ok=%v err=%v", ok, err)
	}
}

// TestLivePGOrdersOwner — the tenant boundary on real PG: a bot-ingested
// order is invisible to other tenants (merge never overwrites owner).
func TestLivePGOrdersOwner(t *testing.T) {
	ctx := context.Background()
	db := pgConnect(t)
	st := NewStore(db.Pool())

	// Owner scoping (issue #100 arc): a bot-ingested order is invisible
	// to other tenants. Unique order_no — a rerun must not merge onto a
	// stale row, and merge intentionally never overwrites owner.
	owner := fmt.Sprintf("tg:live-%d", time.Now().UnixNano()%1e6)
	eml := []byte(strings.Replace(ebayEML, "03-12345-67890",
		fmt.Sprintf("99-%05d-%05d", time.Now().UnixNano()%1e5, time.Now().UnixNano()/1e5%1e5), 1))
	owned, _, err := Ingest(ctx, st, eml, owner)
	if err != nil {
		t.Fatalf("owned ingest: %v", err)
	}
	if owned.Owner != owner {
		t.Fatalf("owner = %q", owned.Owner)
	}
	if _, _, err := st.Get(ctx, "tg:other", owned.ID); err == nil {
		t.Fatal("foreign-owner Get must fail")
	}
	scoped, err := st.List(ctx, owner, false)
	if err != nil || len(scoped) != 1 || scoped[0].ID != owned.ID {
		t.Fatalf("scoped list: %v %+v", err, scoped)
	}
	if ok, _ := st.Mark(ctx, "tg:other", owned.ID, StatusCancelled); ok {
		t.Fatal("foreign-owner Mark must refuse")
	}
}
