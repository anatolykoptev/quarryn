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
	created   *watch.Watch
	cancelled int64
}

func (f *fakeWatchStore) Create(_ context.Context, w *watch.Watch) error {
	w.ID = 7
	f.created = w
	return nil
}

func (f *fakeWatchStore) List(context.Context, bool) ([]watch.Watch, error) {
	return nil, nil
}

func (f *fakeWatchStore) Get(context.Context, int64) (watch.Watch, error) {
	return watch.Watch{}, nil
}

func (f *fakeWatchStore) Cancel(_ context.Context, id int64) (bool, error) {
	f.cancelled = id
	return true, nil
}

func watchDeps() deps {
	return deps{watchStore: &fakeWatchStore{}}
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
