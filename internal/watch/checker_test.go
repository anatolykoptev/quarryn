package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/money"
)

// fakeStore records Record calls; the checker's state transitions are the
// thing under test, not SQL (pg round-trip is covered by the env-gated
// live test).
type fakeStore struct {
	recorded []Watch
	obs      []Observation
}

func (f *fakeStore) Due(context.Context, int, time.Time) ([]Watch, error) {
	return nil, nil
}

func (f *fakeStore) Record(_ context.Context, w *Watch, obs Observation) error {
	f.recorded = append(f.recorded, *w)
	f.obs = append(f.obs, obs)
	return nil
}

type fakeObserver struct{ obs Observation }

func (f fakeObserver) Observe(context.Context, Watch) Observation { return f.obs }

type fakeNotifier struct {
	calls      int
	kinds      []string
	retryAfter time.Duration
	err        error
}

func (f *fakeNotifier) Notify(_ context.Context, _ Watch, _ Observation, kind string) (time.Duration, error) {
	f.calls++
	f.kinds = append(f.kinds, kind)
	return f.retryAfter, f.err
}

type fakeEvaluator struct {
	pass bool
	err  error
}

func (f fakeEvaluator) EvaluateCondition(context.Context, string, extract.Product) (bool, error) {
	return f.pass, f.err
}

func baseWatch() Watch {
	target := int64(10_000) // $100.00
	return Watch{
		ID:               1,
		Kind:             KindOffer,
		URL:              "https://shop.example/p/1",
		OfferID:          "shopify|shop.example|1",
		NativeID:         true,
		TargetPriceMinor: &target,
		NotifyOn:         NotifyPrice,
		Currency:         "USD",
		Interval:         time.Hour,
		Status:           StatusActive,
		ExpiresAt:        time.Now().Add(24 * time.Hour),
		NextCheckAfter:   time.Now(),
	}
}

func okObs(price int64) Observation {
	return Observation{Outcome: OutcomeOK, PriceMinor: &price, Currency: "USD", OfferURL: "https://shop.example/buy"}
}

func TestNotifyOnTargetHit(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Observer: fakeObserver{okObs(9_000)}, Notify: nf}
	w := baseWatch()
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 1 {
		t.Fatalf("notify calls = %d, want 1", nf.calls)
	}
	got := st.recorded[len(st.recorded)-1]
	if got.NotifyPending {
		t.Error("notify_pending should be cleared after success")
	}
	if got.NotifiedPriceMinor == nil || *got.NotifiedPriceMinor != 9_000 {
		t.Errorf("notified_price_minor = %v, want 9000", got.NotifiedPriceMinor)
	}
	if got.NotifyCount != 1 {
		t.Errorf("notify_count = %d, want 1", got.NotifyCount)
	}
}

func TestNoNotifyAboveTarget(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Observer: fakeObserver{okObs(10_001)}, Notify: nf}
	if _, _, err := c.CheckOnce(context.Background(), baseWatch()); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 0 {
		t.Fatalf("notify calls = %d, want 0 (price above target)", nf.calls)
	}
}

// The 1%-of-target dedupe bucket: a drop inside the bucket stays silent;
// a drop exceeding it re-notifies. Both sides of the boundary mutate to
// red if the bucket math drifts by one unit.
func TestDedupeBucket(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Observer: fakeObserver{}, Notify: nf}

	w := baseWatch()
	prev := int64(9_000)
	w.NotifiedPriceMinor = &prev

	// $99 drop on $100 target → exactly 99 = not > 100 (bucket = 1% of
	// target = 100 minor units). Boundary: 99 must NOT notify.
	c.Observer = fakeObserver{okObs(prev - 99)}
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 0 {
		t.Fatal("sub-bucket drop notified — jitter must stay silent")
	}

	// 101 drop > 100 → re-notify.
	c.Observer = fakeObserver{okObs(prev - 101)}
	w.NotifiedPriceMinor = &prev
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 1 {
		t.Fatal("cross-bucket drop did not re-notify")
	}
}

// At-least-once: a failed send leaves notify_pending set, and the pending
// flag overrides dedupe on the next check — even with the same price.
func TestNotifyRetryAtLeastOnce(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{err: errors.New("notifier down"), retryAfter: 7 * time.Minute}
	c := &Checker{Store: st, Observer: fakeObserver{okObs(9_000)}, Notify: nf}

	w := baseWatch()
	before := time.Now()
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	got := st.recorded[len(st.recorded)-1]
	if !got.NotifyPending {
		t.Fatal("failed notify must leave notify_pending set")
	}
	// retryAfter honored: next_check_after = now + 7m, not now + interval.
	want := before.Add(7 * time.Minute)
	if got.NextCheckAfter.Before(want) {
		t.Errorf("retryAfter not honored: next_check_after %v < %v", got.NextCheckAfter, want)
	}

	// Retry succeeds; same price, pending overrides dedupe.
	nf.err = nil
	nf.retryAfter = 0
	if _, _, err := c.CheckOnce(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 2 {
		t.Fatalf("notify calls = %d, want 2 (retry)", nf.calls)
	}
	fin := st.recorded[len(st.recorded)-1]
	if fin.NotifyPending {
		t.Fatal("pending not cleared after retry success")
	}
	if fin.NotifyCount != 1 {
		t.Errorf("notify_count = %d, want 1 (pending retries count once)", fin.NotifyCount)
	}
}

func TestExpiredWatchIsFinal(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Observer: fakeObserver{okObs(1)}, Notify: nf}
	w := baseWatch()
	w.ExpiresAt = time.Now().Add(-time.Second)
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if st.recorded[0].Status != StatusExpired {
		t.Fatalf("status = %q, want expired", st.recorded[0].Status)
	}
	if nf.calls != 0 {
		t.Fatal("expired watch notified — no fetch, no alert")
	}
}

// Two consecutive extract_empty on an offer watch → unverifiable (spec:
// report unverifiable instead of silently watching a stale listing).
func TestUnverifiableAfterTwoEmpty(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{
		Store:    st,
		Observer: fakeObserver{Observation{Outcome: OutcomeExtractEmpty, Detail: "no product"}},
		Notify:   nf,
	}
	w := baseWatch()
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if st.recorded[0].Status == StatusUnverifiable {
		t.Fatal("unverifiable after one empty — needs two consecutive")
	}
	w2 := st.recorded[0]
	if _, _, err := c.CheckOnce(context.Background(), w2); err != nil {
		t.Fatal(err)
	}
	if st.recorded[1].Status != StatusUnverifiable {
		t.Fatalf("status = %q, want unverifiable after 2 empty checks", st.recorded[1].Status)
	}
}

// A healthy observation resets the failure counter and revives an
// unverifiable watch (a listing that came back gets watched again).
func TestOKResetsFailures(t *testing.T) {
	st := &fakeStore{}
	c := &Checker{Store: st, Observer: fakeObserver{okObs(99_000)}, Notify: &fakeNotifier{}}
	w := baseWatch()
	w.ConsecFailures = 1
	w.Status = StatusUnverifiable
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	got := st.recorded[0]
	if got.ConsecFailures != 0 {
		t.Errorf("consec_failures = %d, want 0", got.ConsecFailures)
	}
	if got.Status != StatusActive {
		t.Errorf("status = %q, want active (revived)", got.Status)
	}
}

func TestMoneyFormatInSummary(t *testing.T) {
	// Pin the format the alert uses — a format change is a UX regression
	// nobody logs about.
	if got := money.Format(9_000, "USD"); got != "90.00" {
		t.Fatalf("money.Format(9000,USD) = %q", got)
	}
}
