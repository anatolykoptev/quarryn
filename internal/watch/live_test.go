package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// TestLivePGWatches is the env-gated smoke against the real PG18:
// migration 0002 applied → create/list/get/cancel round-trip.
func TestLivePGWatches(t *testing.T) {
	ctx := context.Background()
	db := pgConnect(t)

	st := NewStore(db.Pool())
	target := int64(999)
	w := &Watch{
		Kind:             KindOffer,
		URL:              "https://example.com/deal/1",
		OfferID:          "url|deadbeef",
		Label:            "live smoke",
		NotifyOn:         NotifyPrice,
		TargetPriceMinor: &target,
		Currency:         "USD",
		Interval:         time.Hour,
		ExpiresAt:        time.Now().Add(time.Hour),
		Status:           StatusActive,
	}
	if err := st.Create(ctx, w, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.Get(ctx, "", w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetPriceMinor == nil || *got.TargetPriceMinor != 999 ||
		got.Kind != KindOffer || got.NotifyOn != NotifyPrice {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// History is the observation ledger read-back (issue #95).
	if _, err := st.History(ctx, w.ID, 10); err != nil {
		t.Fatalf("history: %v", err)
	}
	if err := st.Record(ctx, &got, Observation{
		Outcome: OutcomeOK, Currency: "USD", Detail: "smoke",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	ok, err := st.Cancel(ctx, "", w.ID)
	if err != nil || !ok {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
}

func mustLiveWatch(t *testing.T, st *Store, owner, slug string) *Watch {
	t.Helper()
	w := &Watch{
		Kind: KindOffer, URL: "https://example.com/deal/" + slug,
		OfferID: "url|" + slug, NotifyOn: NotifyRestock,
		Currency: "USD", Interval: time.Hour,
		ExpiresAt: time.Now().Add(time.Hour), Status: StatusActive,
		Owner: owner,
	}
	if err := st.Create(context.Background(), w, 0); err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return w
}

// TestLivePGWatchOwnerScope — the read boundary on real PG (issue #100
// arc): a tg-owned row is invisible to another tenant — Get reports
// not-found, List filters, the unscoped fleet list still sees everyone.
func TestLivePGWatchOwnerScope(t *testing.T) {
	ctx := context.Background()
	st := NewStore(pgConnect(t).Pool())

	// Unique owners per run — leftover rows from previous runs must not
	// pollute the scoped-list assertion on a shared live DB.
	owner := fmt.Sprintf("tg:live-%d", time.Now().UnixNano()%1e6)
	owned := mustLiveWatch(t, st, owner, "owned")
	alien := mustLiveWatch(t, st, fmt.Sprintf("tg:alien-%d", time.Now().UnixNano()%1e6), "alien")
	t.Cleanup(func() { _, _ = st.Cancel(context.Background(), owned.Owner, owned.ID) })
	t.Cleanup(func() { _, _ = st.Cancel(context.Background(), alien.Owner, alien.ID) })

	if got, err := st.Get(ctx, owner, owned.ID); err != nil || got.Owner != owner {
		t.Fatalf("owner get: %v %+v", err, got)
	}
	if _, err := st.Get(ctx, "tg:other", owned.ID); err == nil {
		t.Fatal("foreign-owner Get must fail")
	}
	scoped, err := st.List(ctx, owner, true)
	if err != nil || len(scoped) != 1 || scoped[0].ID != owned.ID {
		t.Fatalf("scoped list: %v %+v", err, scoped)
	}
	all, err := st.List(ctx, "", true)
	found := map[int64]bool{}
	for _, w := range all {
		found[w.ID] = true
	}
	if err != nil || !found[owned.ID] || !found[alien.ID] {
		t.Fatalf("unscoped list must hold both tenants: %v %v", err, found)
	}
}

// TestLivePGWatchOwnerMutate — the write boundary: foreign Cancel
// refuses, same-owner Cancel succeeds, CountByOwner tracks actives.
func TestLivePGWatchOwnerMutate(t *testing.T) {
	ctx := context.Background()
	st := NewStore(pgConnect(t).Pool())
	owner := fmt.Sprintf("tg:live-%d", time.Now().UnixNano()%1e6)
	owned := mustLiveWatch(t, st, owner, "owned")

	if ok, _ := st.Cancel(ctx, "tg:other", owned.ID); ok {
		t.Fatal("foreign-owner Cancel must refuse")
	}
	if n, err := st.CountByOwner(ctx, owner); err != nil || n != 1 {
		t.Fatalf("count by owner: %v %d", err, n)
	}
	if ok, err := st.Cancel(ctx, owner, owned.ID); err != nil || !ok {
		t.Fatalf("owner cancel: ok=%v err=%v", ok, err)
	}
	if n, err := st.CountByOwner(ctx, owner); err != nil || n != 0 {
		t.Fatalf("count after cancel: %v %d", err, n)
	}
}

// TestLivePGWatchOwnerCap — the cap is atomic at the store: max=1 admits
// the first add, refuses the second with ErrWatchCap, and an expired
// row does not count (Review #106 — expiry must not pin the cap).
func TestLivePGWatchOwnerCap(t *testing.T) {
	ctx := context.Background()
	st := NewStore(pgConnect(t).Pool())
	owner := fmt.Sprintf("tg:cap-%d", time.Now().UnixNano()%1e6)

	// An already-expired row: capped count must ignore it.
	dead := mustLiveWatch(t, st, owner, "capdead")
	if _, err := st.pool.Exec(ctx,
		`UPDATE watches SET expires_at = now() - interval '1h' WHERE id=$1`, dead.ID); err != nil {
		t.Fatalf("expire fixture: %v", err)
	}
	live := mustLiveWatch(t, st, owner, "caplive")
	t.Cleanup(func() { _, _ = st.Cancel(context.Background(), owner, live.ID) })
	t.Cleanup(func() { _, _ = st.Cancel(context.Background(), owner, dead.ID) })

	next := &Watch{
		Kind: KindOffer, URL: "https://example.com/deal/cap2",
		OfferID: "url|cap2", NotifyOn: NotifyRestock,
		Currency: "USD", Interval: time.Hour,
		ExpiresAt: time.Now().Add(time.Hour), Status: StatusActive,
		Owner: owner,
	}
	if err := st.Create(ctx, next, 1); !errors.Is(err, ErrWatchCap) {
		t.Fatalf("second add with max=1: err=%v, want ErrWatchCap", err)
	}
}
