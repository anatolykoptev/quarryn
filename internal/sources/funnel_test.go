package sources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

// stubAdapter is a hand-rolled Adapter for funnel tests.
type stubAdapter struct {
	name    string
	enabled bool
	class   FetchClass
	results []sources.Result
	err     error
}

func (s stubAdapter) Name() string     { return s.name }
func (s stubAdapter) Spec() SourceSpec { return SourceSpec{FetchClass: s.class} }
func (s stubAdapter) Enabled() bool    { return s.enabled }
func (s stubAdapter) Search(context.Context, sources.Query) ([]sources.Result, error) {
	return s.results, s.err
}

// stubFetcher is a hand-rolled Fetcher for tests that must not touch wowa.
type stubFetcher struct {
	body string
	err  error
}

func (f stubFetcher) Fetch(context.Context, wowa.FetchRequest) (*wowa.FetchResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &wowa.FetchResponse{Status: 200, Body: f.body}, nil
}

func result(url, content string, md map[string]string) sources.Result {
	return sources.Result{Title: content, URL: url, Content: content, Metadata: md}
}

func statusOf(out SearchOutput, name string) SourceStatus {
	for _, s := range out.Sources {
		if s.Name == name {
			return s
		}
	}
	return SourceStatus{}
}

func TestFunnelMergesAndSSRFScreens(t *testing.T) {
	// Literal-IP URLs keep the real httputil.CheckRawURL hermetic: public
	// TEST-NET passes without DNS, RFC1918/metadata/scheme violations are
	// refused without DNS.
	adapterA := stubAdapter{
		name: "a", enabled: true, class: FetchClassAPI,
		results: []sources.Result{
			result("http://203.0.113.10/item1", "sony headphones deal", nil),
			result("http://10.0.0.1/internal", "private range", nil), // RFC1918 → reject
			result("http://169.254.169.254/latest", "metadata", nil), // cloud metadata → reject
			result("javascript:alert(1)", "bad scheme", nil),         // non-http(s) → reject
			result("", "no url", nil),                                // empty → reject
		},
	}
	adapterB := stubAdapter{
		name: "b", enabled: true, class: FetchClassFetch,
		results: []sources.Result{
			result("http://203.0.113.20/item2", "lg oled tv deal", nil),
			result("http://203.0.113.10/item1", "sony headphones deal", nil), // same URL → FuseWRR merges
		},
	}
	f := NewFunnel(map[string]Adapter{"a": adapterA, "b": adapterB})

	out, err := f.Search(t.Context(), sources.Query{Text: "deal"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2 (dup URL fused, private/scheme/empty rejected): %+v",
			len(out.Candidates), out.Candidates)
	}
	for _, c := range out.Candidates {
		if strings.Contains(c.URL, "10.0.0.1") || strings.Contains(c.URL, "169.254") ||
			strings.HasPrefix(c.URL, "javascript:") {
			t.Fatalf("SSRF guard let %q through", c.URL)
		}
		if c.Source == "" {
			t.Fatalf("candidate missing source: %+v", c)
		}
	}
	st := statusOf(out, "a")
	if st.Outcome != OutcomeOK {
		t.Fatalf("a outcome = %q", st.Outcome)
	}
	if st.Rejected != 4 {
		t.Fatalf("a rejected = %d, want 4 (10.x, metadata, js, empty)", st.Rejected)
	}
	if st.Count != 1 {
		t.Fatalf("a count = %d, want 1", st.Count)
	}
}

func TestFunnelDedupsSimilarContent(t *testing.T) {
	near := "Sony WH-1000XM5 wireless noise canceling headphones black"
	full := "Sony WH-1000XM5 wireless noise canceling headphones black edition"
	f := NewFunnel(map[string]Adapter{
		"a": stubAdapter{name: "a", enabled: true,
			results: []sources.Result{result("http://203.0.113.10/x", near, nil)}},
		"b": stubAdapter{name: "b", enabled: true,
			results: []sources.Result{result("http://203.0.113.20/y", full, nil)}},
	})
	out, err := f.Search(t.Context(), sources.Query{Text: "sony"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("near-duplicate content should dedup to 1, got %d", len(out.Candidates))
	}
}

func TestFunnelSkipsDisabledAdapters(t *testing.T) {
	f := NewFunnel(map[string]Adapter{
		"dark": stubAdapter{name: "dark", enabled: false},
		"live": stubAdapter{name: "live", enabled: true,
			results: []sources.Result{result("http://203.0.113.30/z", "item", nil)}},
	})
	out, err := f.Search(t.Context(), sources.Query{Text: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if st := statusOf(out, "dark"); st.Outcome != OutcomeSkipped {
		t.Fatalf("dark outcome = %q, want skipped", st.Outcome)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Source != "live" {
		t.Fatalf("candidates = %+v", out.Candidates)
	}
}

func TestFunnelZeroEnabledIsError(t *testing.T) {
	f := NewFunnel(map[string]Adapter{
		"dark": stubAdapter{name: "dark", enabled: false},
	})
	_, err := f.Search(t.Context(), sources.Query{Text: "x"})
	if !errors.Is(err, ErrNoEnabledSources) {
		t.Fatalf("err = %v, want ErrNoEnabledSources", err)
	}
}

func TestFunnelAllFailedIsError(t *testing.T) {
	f := NewFunnel(map[string]Adapter{
		"a": stubAdapter{name: "a", enabled: true, err: fmt.Errorf("boom-a")},
		"b": stubAdapter{name: "b", enabled: true, err: fmt.Errorf("boom-b")},
	})
	_, err := f.Search(t.Context(), sources.Query{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "all 2 enabled adapters failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestFunnelPartialFailureNotFatal(t *testing.T) {
	f := NewFunnel(map[string]Adapter{
		"bad": stubAdapter{name: "bad", enabled: true, err: fmt.Errorf("boom")},
		"good": stubAdapter{name: "good", enabled: true,
			results: []sources.Result{result("http://203.0.113.40/i", "kept", nil)}},
	})
	out, err := f.Search(t.Context(), sources.Query{Text: "x"})
	if err != nil {
		t.Fatalf("partial failure must not error: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d", len(out.Candidates))
	}
	if st := statusOf(out, "bad"); st.Outcome != OutcomeFailed || st.Reason == "" {
		t.Fatalf("bad status = %+v", st)
	}
}

func TestFunnelCap(t *testing.T) {
	res := make([]sources.Result, 0, 10)
	for i := 0; i < 10; i++ {
		res = append(res, result(fmt.Sprintf("http://203.0.113.%d/i%d", i+1, i),
			fmt.Sprintf("distinct product number %d", i), nil))
	}
	f := NewFunnel(
		map[string]Adapter{"a": stubAdapter{name: "a", enabled: true, results: res}},
		WithLimit(3),
	)
	out, err := f.Search(t.Context(), sources.Query{Text: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out.Candidates) != 3 {
		t.Fatalf("cap = %d, want 3", len(out.Candidates))
	}
}

// runFunnelOne searches through a one-adapter funnel and returns the single
// resulting candidate.
func runFunnelOne(t *testing.T, md map[string]string) Candidate {
	t.Helper()
	f := NewFunnel(map[string]Adapter{
		"a": stubAdapter{name: "a", enabled: true, results: []sources.Result{
			result("http://203.0.113.50/p", "priced item", md),
		}},
	})
	out, err := f.Search(t.Context(), sources.Query{Text: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(out.Candidates))
	}
	return out.Candidates[0]
}

func TestCandidateDecodeLiftsTypedFields(t *testing.T) {
	c := runFunnelOne(t, map[string]string{
		MetaPrice:        "19.99",
		MetaCurrency:     "USD",
		MetaCondition:    "NEW",
		MetaAvailability: AvailabilityInStock,
		MetaSeller:       "seller-1",
	})
	if c.Price == nil || *c.Price != 19.99 {
		t.Fatalf("price = %v", c.Price)
	}
	if c.Currency != "USD" || c.Condition != "NEW" || c.Availability != AvailabilityInStock ||
		c.Seller != "seller-1" || c.Source != "a" {
		t.Fatalf("candidate = %+v", c)
	}
}

func TestCandidateDecodeLiftsScoreFields(t *testing.T) {
	c := runFunnelOne(t, map[string]string{
		MetaDiscountPct:   "25.0",
		MetaThumbs:        "42",
		MetaBuyingOptions: "FIXED_PRICE",
		MetaListingID:     "l-9",
	})
	if c.DiscountPct == nil || *c.DiscountPct != 25.0 {
		t.Fatalf("discount = %v", c.DiscountPct)
	}
	if c.Thumbs == nil || *c.Thumbs != 42 {
		t.Fatalf("thumbs = %v", c.Thumbs)
	}
	// Non-lifted keys pass through.
	if c.Metadata[MetaBuyingOptions] != "FIXED_PRICE" || c.Metadata[MetaListingID] != "l-9" {
		t.Fatalf("metadata passthrough = %v", c.Metadata)
	}
}

func TestCandidateDecodeKeepsMalformedNumbers(t *testing.T) {
	c := candidateFromResult(sources.Result{
		Title: "t", URL: "http://x",
		Metadata: map[string]string{MetaPrice: "call-for-price"},
	})
	if c.Price != nil {
		t.Fatal("unparseable price must not become a typed field")
	}
	if c.Metadata[MetaPrice] != "call-for-price" {
		t.Fatal("malformed price must survive in Metadata")
	}
}
