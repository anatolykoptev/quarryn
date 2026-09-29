package api

import (
	"context"
	"strings"
	"testing"

	"github.com/anatolykoptev/quarryn/internal/watch"
)

// fakeWatchStore satisfies watchStorer for validation tests — pg itself
// is covered by the env-gated live test in internal/postgres.
type fakeWatchStore struct {
	created    *watch.Watch
	cancelled  int64
	ownerSeen  []string // every owner arg a scoped call carried
	maxSeen    int      // cap arg Create received
	ownerCount int      // rows the fake pretends already exist
}

// Create mirrors the store contract: the cap travels in via max, and the
// refusal is the sentinel — the api layer maps it, nothing else counts.
func (f *fakeWatchStore) Create(_ context.Context, w *watch.Watch, max int) error {
	f.maxSeen = max
	if max > 0 && w.Owner != "" && f.ownerCount >= max {
		return watch.ErrWatchCap
	}
	w.ID = 7
	f.created = w
	return nil
}

func (f *fakeWatchStore) List(_ context.Context, owner string, _ bool) ([]watch.Watch, error) {
	f.ownerSeen = append(f.ownerSeen, owner)
	return nil, nil
}

func (f *fakeWatchStore) Get(_ context.Context, owner string, _ int64) (watch.Watch, error) {
	f.ownerSeen = append(f.ownerSeen, owner)
	return watch.Watch{}, nil
}

func (f *fakeWatchStore) Cancel(_ context.Context, owner string, id int64) (bool, error) {
	f.ownerSeen = append(f.ownerSeen, owner)
	f.cancelled = id
	return true, nil
}

func (f *fakeWatchStore) History(context.Context, int64, int) ([]watch.Observation, error) {
	return nil, nil
}

// Owner scoping (issue #100 arc): every action must carry the caller's
// owner into the store — a missed arg silently leaks another tenant's
// watches to a bot user, and a missing stamp silently orphans the row.
func TestWatchOwnerScoping(t *testing.T) {
	d := watchDeps()
	st := d.watchStore.(*fakeWatchStore)
	ctx := context.Background()

	a := watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		TargetPrice: 100, Currency: "USD", Owner: "tg:42"}
	if out := d.watchAdd(ctx, a); !out.OK {
		t.Fatalf("owned add rejected: %s", out.Error)
	}
	if st.created.Owner != "tg:42" {
		t.Fatalf("created owner = %q", st.created.Owner)
	}

	d.watchList(ctx, watchArgs{Action: "list", Owner: "tg:42"})
	d.watchGet(ctx, watchArgs{Action: "get", WatchID: 7, Owner: "tg:42"})
	d.watchCancel(ctx, watchArgs{Action: "cancel", WatchID: 7, Owner: "tg:42"})
	for i, o := range st.ownerSeen {
		if o != "tg:42" {
			t.Errorf("call %d carried owner %q, want tg:42", i, o)
		}
	}
	if len(st.ownerSeen) != 3 { // list + get + cancel — cap rides Create
		t.Fatalf("owner path seen %d calls, want 3", len(st.ownerSeen))
	}
}

// The per-owner cap is the public-bot abuse bound — the api hands the
// configured max to Create, which enforces it atomically in the store.
func TestWatchOwnerCap(t *testing.T) {
	d := watchDeps()
	d.watchOwnerMax = 2
	st := d.watchStore.(*fakeWatchStore)
	st.ownerCount = 2

	a := watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		TargetPrice: 100, Currency: "USD", Owner: "tg:42"}
	out := d.watchAdd(context.Background(), a)
	if out.OK {
		t.Fatal("add past owner cap accepted")
	}
	if !strings.Contains(out.Error, "cap") {
		t.Fatalf("cap error %q lacks 'cap'", out.Error)
	}
	if st.maxSeen != 2 {
		t.Fatalf("Create received max=%d, want 2", st.maxSeen)
	}

	// Fleet (ownerless) adds are uncapped — the store skips the lock.
	a.Owner = ""
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("ownerless add rejected: %s", out.Error)
	}
}

func watchDeps() deps {
	return deps{watchStore: &fakeWatchStore{}}
}

// fakeGroupLookup satisfies watch.GroupLookup for kind=group tests.
type fakeGroupLookup struct {
	urls []string
}

func (f fakeGroupLookup) MemberURLs(_ context.Context, _ int64, limit int) ([]string, error) {
	if len(f.urls) > limit {
		return f.urls[:limit], nil
	}
	return f.urls, nil
}

func (f fakeGroupLookup) GroupByURL(context.Context, string) (int64, error) { return 0, nil }

func TestWatchAddGroup(t *testing.T) {
	d := deps{watchStore: &fakeWatchStore{},
		groupLookup: fakeGroupLookup{urls: []string{"http://a.com/p"}}}
	base := watchArgs{Action: "add", Kind: "group", GroupID: 42,
		TargetPrice: 100, Currency: "USD"}
	out := d.watchAdd(context.Background(), base)
	if !out.OK {
		t.Fatalf("valid group add rejected: %s", out.Error)
	}
	st := d.watchStore.(*fakeWatchStore)
	if st.created.GroupID != 42 || st.created.Kind != watch.KindGroup {
		t.Fatalf("stored watch = %+v", st.created)
	}
	if st.created.Interval != watch.MinGroupInterval {
		t.Fatalf("default interval = %v, want group floor %v", st.created.Interval, watch.MinGroupInterval)
	}

	for _, tc := range []struct {
		name string
		mut  func(*watchArgs)
		deps deps
		want string
	}{
		{"no group_id", func(a *watchArgs) { a.GroupID = 0 }, d, "group_id"},
		{"restock rejected", func(a *watchArgs) { a.NotifyOn = "restock" }, d, "kind=offer"},
		{"variant rejected", func(a *watchArgs) { a.Variant = "64GB" }, d, "kind=offer"},
		{"no registry", func(*watchArgs) {},
			deps{watchStore: &fakeWatchStore{}}, "registry"},
		{"empty group", func(*watchArgs) {},
			deps{watchStore: &fakeWatchStore{}, groupLookup: fakeGroupLookup{}}, "not found"},
		{"bad criteria", func(a *watchArgs) { a.Criteria = []string{"price_max:abc"} }, d, "criteria"},
	} {
		a := base
		tc.mut(&a)
		out := tc.deps.watchAdd(context.Background(), a)
		if out.OK {
			t.Errorf("%s: invalid group add accepted", tc.name)
		} else if !strings.Contains(out.Error, tc.want) {
			t.Errorf("%s: error %q lacks %q", tc.name, out.Error, tc.want)
		}
	}
}

func TestWatchAddValidation(t *testing.T) {
	d := watchDeps()
	base := watchArgs{Action: "add", Kind: "offer",
		URL: "http://192.0.2.10/p", TargetPrice: 100, Currency: "USD"}

	// Literal IP passes SSRF without DNS — same trick as funnel tests.
	if out := d.watchAdd(context.Background(), base); !out.OK {
		t.Fatalf("valid offer add rejected: %s", out.Error)
	}

	for _, tc := range []struct {
		name string
		mut  func(*watchArgs)
		want string
	}{
		{"no target", func(a *watchArgs) { a.TargetPrice = 0 }, "target_price"},
		{"no currency", func(a *watchArgs) { a.Currency = "" }, "currency"},
		{"bad currency shape", func(a *watchArgs) { a.Currency = "US1" }, "currency"},
		{"no url", func(a *watchArgs) { a.URL = "" }, "url required"},
		{"no kind", func(a *watchArgs) { a.Kind = "" }, "kind"},
		{"ttl over cap", func(a *watchArgs) { a.TTLHours = 99999 }, "ttl"},
		{"interval under floor", func(a *watchArgs) {
			a.IntervalMinutes = 5
		}, "floor"},
		{"bad offer_id", func(a *watchArgs) { a.OfferID = "garbage" }, "offer_id"},
		{"bad notify_on", func(a *watchArgs) { a.NotifyOn = "hourly" }, "notify_on"},
		{"pct out of range", func(a *watchArgs) { a.TargetPct = 150 }, "target_pct"},
		{"pct negative", func(a *watchArgs) { a.TargetPct = -5 }, "target_pct"},
		{"any mode needs a target", func(a *watchArgs) {
			a.NotifyOn = "any"
			a.TargetPrice = 0
		}, "target_price"},
		{"condition over cap", func(a *watchArgs) {
			a.Condition = strings.Repeat("x", 501)
		}, "condition"},
	} {
		a := base
		tc.mut(&a)
		out := d.watchAdd(context.Background(), a)
		if out.OK {
			t.Errorf("%s: invalid add accepted", tc.name)
		} else if !strings.Contains(out.Error, tc.want) {
			t.Errorf("%s: error %q lacks %q", tc.name, out.Error, tc.want)
		}
	}
}

func TestWatchAddQueryValidatesCriteria(t *testing.T) {
	d := watchDeps()
	a := watchArgs{Action: "add", Kind: "query", Query: "jbl speaker",
		TargetPrice: 30, Currency: "USD",
		Criteria: []string{"price_max:abc"}}
	out := d.watchAdd(context.Background(), a)
	if out.OK {
		t.Fatal("malformed criteria accepted — would observe forever")
	}
	a.Criteria = []string{"price_max:50"}
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("valid query add rejected: %s", out.Error)
	}
}

// Restock-only and pct-only watches need no absolute target — the
// trigger set is what matters, not a price field (issues #93/#94).
func TestWatchAddTriggerShapes(t *testing.T) {
	d := watchDeps()
	st := d.watchStore.(*fakeWatchStore)

	// Pure restock: no price target at all.
	a := watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		Currency: "USD", NotifyOn: "restock"}
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("restock-only add rejected: %s", out.Error)
	}
	if st.created.NotifyOn != watch.NotifyRestock || st.created.TargetPriceMinor != nil {
		t.Errorf("restock watch: notify_on=%q target=%v", st.created.NotifyOn, st.created.TargetPriceMinor)
	}

	// Pure pct: 20% drop from first observed price.
	a = watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		Currency: "USD", TargetPct: 20}
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("pct-only add rejected: %s", out.Error)
	}
	if st.created.TargetPct == nil || *st.created.TargetPct != 20 {
		t.Errorf("pct watch: target_pct=%v", st.created.TargetPct)
	}

	// Combined: restock + absolute target under "any".
	a = watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		Currency: "USD", NotifyOn: "any", TargetPrice: 100,
		Condition: "sold by the brand store"}
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("any+condition add rejected: %s", out.Error)
	}
	if st.created.NotifyOn != watch.NotifyAny || st.created.ConditionText == "" {
		t.Errorf("any watch: notify_on=%q condition=%q", st.created.NotifyOn, st.created.ConditionText)
	}
}

// Restock on a query watch is rejected: the cheapest offer changes
// between checks, and a cross-listing availability flip would report a
// false restock (Devin Review #101). Query watches take price triggers.
func TestWatchAddQueryRejectsRestock(t *testing.T) {
	d := watchDeps()
	for _, mode := range []string{"restock", "any"} {
		a := watchArgs{Action: "add", Kind: "query", Query: "jbl speaker",
			Currency: "USD", NotifyOn: mode, TargetPrice: 30}
		out := d.watchAdd(context.Background(), a)
		if out.OK {
			t.Errorf("query+notify_on=%s accepted — restock needs a stable listing", mode)
		}
	}
	// Price-mode query watch still fine.
	a := watchArgs{Action: "add", Kind: "query", Query: "jbl speaker",
		Currency: "USD", NotifyOn: "price", TargetPrice: 30}
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("price-mode query add rejected: %s", out.Error)
	}
}

// native_id is the offer-watch honesty flag: url| fallbacks must not pose
// as listing-pinned.
func TestWatchAddNativeID(t *testing.T) {
	d := watchDeps()
	a := watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		TargetPrice: 100, Currency: "USD",
		OfferID: "shopify|shop.example|gid://x/1"}
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatalf("native offer_id rejected: %s", out.Error)
	}
	st := d.watchStore.(*fakeWatchStore)
	if !st.created.NativeID {
		t.Error("native offer_id must set native_id")
	}
	a.OfferID = ""
	if out := d.watchAdd(context.Background(), a); !out.OK {
		t.Fatal(out.Error)
	}
	if st.created.NativeID {
		t.Error("url-derived offer_id must report native_id=false")
	}
}

// Variant pinning (issue #115): offer-kind carries the selector into the
// stored watch and echoes it in the entry; query-kind rejects it — a
// re-picked cheapest offer can never honour a pin.
func TestWatchAddVariantSelector(t *testing.T) {
	d := watchDeps()
	st := d.watchStore.(*fakeWatchStore)
	a := watchArgs{Action: "add", Kind: "offer", URL: "http://192.0.2.10/p",
		TargetPrice: 100, Currency: "USD", Variant: " 64GB "}
	out := d.watchAdd(context.Background(), a)
	if !out.OK {
		t.Fatalf("variant-pinned offer add rejected: %s", out.Error)
	}
	if st.created.VariantSel != "64GB" {
		t.Fatalf("stored variant_sel = %q", st.created.VariantSel)
	}
	if out.Watch.VariantSel != "64GB" {
		t.Fatalf("entry variant = %q", out.Watch.VariantSel)
	}

	q := watchArgs{Action: "add", Kind: "query", Query: "mbp",
		TargetPrice: 100, Currency: "USD", Variant: "64GB"}
	if out := d.watchAdd(context.Background(), q); out.OK {
		t.Fatal("variant on a query watch must be rejected")
	}
}
