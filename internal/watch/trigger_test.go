package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anatolykoptev/quarryn/internal/extract"
)

// Multi-trigger watches (issues #93/#94/#96): restock transitions,
// percent-drop baselines and the jeff condition gate. Each boundary is
// pinned on both sides — a one-unit drift must go red.

func restockWatch() Watch {
	w := baseWatch()
	w.TargetPriceMinor = nil
	w.NotifyOn = NotifyRestock
	return w
}

func availObs(avail string, price int64) Observation {
	o := okObs(price)
	o.Availability = avail
	return o
}

func TestRestockTransitionNotifies(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Notify: nf}
	c.Observer = fakeObserver{availObs("in_stock", 9_900)}

	w := restockWatch()
	w.LastAvailability = "out_of_stock"
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 1 {
		t.Fatalf("notify calls = %d, want 1 (unbuyable→buyable)", nf.calls)
	}
}

func TestRestockBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		notifyOn  string
		prevAvail string
		obsAvail  string
		wantCalls int
	}{
		{"first observation never fires", NotifyRestock, "", "in_stock", 0},
		{"in→in is not a transition", NotifyRestock, "in_stock", "in_stock", 0},
		{"discontinued→in_stock fires", NotifyRestock, "discontinued", "in_stock", 1},
		{"out→pre_order fires (orderable)", NotifyRestock, "out_of_stock", "pre_order", 1},
		{"out→out stays quiet", NotifyRestock, "out_of_stock", "out_of_stock", 0},
		{"in→out is a loss, not a restock", NotifyRestock, "in_stock", "out_of_stock", 0},
		{"price mode ignores restock", NotifyPrice, "out_of_stock", "in_stock", 0},
		{"any mode takes the transition", NotifyAny, "out_of_stock", "in_stock", 1},
	} {
		st := &fakeStore{}
		nf := &fakeNotifier{}
		c := &Checker{Store: st, Notify: nf}
		c.Observer = fakeObserver{availObs(tc.obsAvail, 9_900)}
		w := restockWatch()
		w.NotifyOn = tc.notifyOn
		w.LastAvailability = tc.prevAvail
		if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		if nf.calls != tc.wantCalls {
			t.Errorf("%s: notify calls = %d, want %d", tc.name, nf.calls, tc.wantCalls)
		}
	}
}

// At-least-once for restock: a failed send retries via notify_pending —
// the settled (non-transition) observation must still deliver.
func TestRestockPendingRetry(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{err: errors.New("down")}
	c := &Checker{Store: st, Notify: nf}
	c.Observer = fakeObserver{availObs("in_stock", 100)}

	w := restockWatch()
	w.LastAvailability = "out_of_stock"
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	got := st.recorded[len(st.recorded)-1]
	if !got.NotifyPending {
		t.Fatal("failed restock notify must leave pending set")
	}

	nf.err = nil
	// Next check: still in_stock — no fresh transition. Pending overrides.
	if _, _, err := c.CheckOnce(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 2 {
		t.Fatalf("notify calls = %d, want 2 (pending retry delivered)", nf.calls)
	}
}

// target_pct (issue #94): the first ok observation anchors the baseline;
// drops ≥ pct% of it fire, smaller drops don't.
func TestPctDropBaselineAndTrigger(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Notify: nf}

	pct := 20
	w := restockWatch()
	w.NotifyOn = NotifyPrice
	w.TargetPct = &pct // no absolute target — pct-only watch

	// First check anchors baseline at 10000; no drop yet → silent.
	c.Observer = fakeObserver{okObs(10_000)}
	updated, _, err := c.CheckOnce(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if updated.BaselineMinor == nil || *updated.BaselineMinor != 10_000 {
		t.Fatalf("baseline = %v, want 10000 anchored on first obs", updated.BaselineMinor)
	}
	if nf.calls != 0 {
		t.Fatal("baseline anchor must not notify")
	}

	// -19% (8100 > 8000 threshold) → silent. Boundary pin.
	c.Observer = fakeObserver{okObs(8_100)}
	if _, _, err := c.CheckOnce(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 0 {
		t.Fatal("19% drop notified — boundary must be ≥20%")
	}

	// -20% (8000 ≤ 8000) → fires.
	c.Observer = fakeObserver{okObs(8_000)}
	if _, _, err := c.CheckOnce(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 1 {
		t.Fatalf("notify calls = %d, want 1 at exactly -20%%", nf.calls)
	}
}

// When both targets exist the LOOSER one wins (whichever fires first):
// absolute 5000 vs pct-20%→8000 threshold → 7000 fires via pct.
func TestPctAndAbsoluteTakeLooser(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{}
	c := &Checker{Store: st, Notify: nf, Observer: fakeObserver{okObs(7_000)}}

	target := int64(5_000)
	pct := 20
	base := int64(10_000)
	w := baseWatch()
	w.TargetPriceMinor = &target
	w.TargetPct = &pct
	w.BaselineMinor = &base

	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 1 {
		t.Fatal("7000 must fire: looser threshold (pct→8000) applies")
	}
}

// The condition gate (issue #96): a fired trigger is vetoed when the
// evaluator rejects the product, and evaluation errors fail closed.
func TestConditionGate(t *testing.T) {
	prod := &extract.Product{Name: "Widget"}
	mkObs := func() Observation {
		o := okObs(9_000)
		o.Product = prod
		return o
	}
	for _, tc := range []struct {
		name       string
		eval       ConditionEvaluator
		wantCalls  int
		wantDetail string
	}{
		{"pass notifies", fakeEvaluator{pass: true}, 1, ""},
		{"fail suppresses", fakeEvaluator{pass: false}, 0, "condition not met"},
		{"eval error fails closed", fakeEvaluator{err: errors.New("jeff down")}, 0, "condition unevaluated"},
		{"nil evaluator fails closed", nil, 0, "condition unevaluated"},
	} {
		st := &fakeStore{}
		nf := &fakeNotifier{}
		c := &Checker{Store: st, Notify: nf, Evaluator: tc.eval}
		c.Observer = fakeObserver{mkObs()}
		w := baseWatch()
		w.ConditionText = "sold by the brand store"
		if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		if nf.calls != tc.wantCalls {
			t.Errorf("%s: notify calls = %d, want %d", tc.name, nf.calls, tc.wantCalls)
		}
		if tc.wantDetail != "" {
			last := st.obs[len(st.obs)-1]
			if !strings.Contains(last.Detail, tc.wantDetail) {
				t.Errorf("%s: detail %q lacks %q", tc.name, last.Detail, tc.wantDetail)
			}
		}
	}
}

// Pending retries skip the condition gate — at-least-once was already
// owed when the trigger fired; re-judging would silently drop the debt.
func TestConditionSkippedOnPendingRetry(t *testing.T) {
	st := &fakeStore{}
	nf := &fakeNotifier{err: errors.New("down")}
	prod := &extract.Product{Name: "W"}
	c := &Checker{Store: st, Notify: nf, Evaluator: fakeEvaluator{pass: true}}
	c.Observer = fakeObserver{Observation{Outcome: OutcomeOK, PriceMinor: ptr(9_000), Currency: "USD", Product: prod}}

	w := baseWatch()
	w.ConditionText = "any"
	if _, _, err := c.CheckOnce(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	got := st.recorded[len(st.recorded)-1]
	if !got.NotifyPending {
		t.Fatal("pending must be set after failed send")
	}

	// Retry with a FAILING evaluator — pending still delivers.
	c.Evaluator = fakeEvaluator{pass: false}
	nf.err = nil
	if _, _, err := c.CheckOnce(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if nf.calls != 2 {
		t.Fatalf("notify calls = %d, want 2 — pending overrides the gate", nf.calls)
	}
}

func ptr(v int64) *int64 { return &v }
