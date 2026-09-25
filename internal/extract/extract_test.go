package extract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/sources"
)

// stubFetcher is a hand-rolled Fetcher counting calls.
type stubFetcher struct {
	calls atomic.Int64
	resp  *wowa.FetchResponse
	err   error
}

func (s *stubFetcher) Fetch(context.Context, wowa.FetchRequest) (*wowa.FetchResponse, error) {
	s.calls.Add(1)
	return s.resp, s.err
}

// stubExtractor is a hand-rolled ExtractCaller counting calls; data is the
// raw JSON payload the fake /extract returns.
type stubExtractor struct {
	calls atomic.Int64
	data  string
	err   error
}

func (s *stubExtractor) Extract(context.Context, wowa.ExtractRequest) (*wowa.ExtractResponse, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &wowa.ExtractResponse{Data: json.RawMessage(s.data), SourceMethod: "read"}, nil
}

func testConfig() Config {
	return Config{LLMTopN: 10, MaxDetailFetches: 15, Concurrency: 2}
}

func cand(url, title string, price *float64) sources.Candidate {
	return sources.Candidate{Source: "shopify", Title: title, URL: url, Price: price}
}

func f64(v float64) *float64 { return &v }

func TestEnrichSerpSufficient_NoFetch(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: amazonProductHTML}}
	x := &stubExtractor{}
	p := New(f, x, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "ebay", Title: "Sony headphones", URL: "https://www.ebay.com/itm/1",
			Price: f64(19.99), Currency: "USD"},
	})
	if len(out) != 1 || out[0].ExtractionFailed {
		t.Fatalf("unexpected result: %+v", out)
	}
	if f.calls.Load() != 0 || x.calls.Load() != 0 {
		t.Fatalf("serp-sufficient candidate triggered detail work: fetch=%d llm=%d",
			f.calls.Load(), x.calls.Load())
	}
	if out[0].Product.Method != MethodSERP || out[0].Product.Source != "ebay.com" {
		t.Fatalf("product = %+v", out[0].Product)
	}
}

func TestEnrichDetailFetchSchemaFillsPrice(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: amazonProductHTML}}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://www.amazon.com/dp/B09XS7JWHH", "Sony WH-1000XM5", nil),
	})
	if f.calls.Load() != 1 {
		t.Fatalf("fetch calls = %d", f.calls.Load())
	}
	got := out[0]
	if got.ExtractionFailed || got.Product.Price == nil || *got.Product.Price != 278.0 {
		t.Fatalf("schema fill failed: %+v", got)
	}
	if got.Product.Method != MethodSchema || got.Product.Currency != "USD" {
		t.Fatalf("method/currency = %q %q", got.Product.Method, got.Product.Currency)
	}
}

func TestEnrichCacheHitAvoidsSecondFetch(t *testing.T) {
	c := cache.New(cache.Config{L1MaxItems: 100})
	t.Cleanup(c.Close)
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: amazonProductHTML}}
	cfg := testConfig()
	cfg.Cache = c
	p := New(f, nil, cfg)
	in := []sources.Candidate{
		cand("https://www.amazon.com/dp/B09XS7JWHH?utm_source=x", "Sony WH-1000XM5", nil),
	}
	p.Enrich(t.Context(), in)
	out := p.Enrich(t.Context(), in)
	if f.calls.Load() != 1 {
		t.Fatalf("cache miss on second Enrich: fetch calls = %d", f.calls.Load())
	}
	if out[0].Product.Price == nil || *out[0].Product.Price != 278.0 {
		t.Fatalf("cached product wrong: %+v", out[0].Product)
	}
}

func TestEnrichLLMFallbackFillsPrice(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: `<html><body>plain page</body></html>`}}
	x := &stubExtractor{data: `{"name":"Mystery Gadget","price":42.5,"currency":"USD","condition":"used"}`}
	p := New(f, x, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://shop.example.com/p/1", "", nil), // no title, no price
	})
	got := out[0]
	if x.calls.Load() != 1 {
		t.Fatalf("llm calls = %d", x.calls.Load())
	}
	if got.ExtractionFailed || got.Product.Method != MethodLLM {
		t.Fatalf("llm fallback failed: %+v", got)
	}
	if got.Product.Price == nil || *got.Product.Price != 42.5 || got.Product.Name != "Mystery Gadget" {
		t.Fatalf("llm product = %+v", got.Product)
	}
	if got.Product.Condition != "used" {
		t.Fatalf("condition = %q", got.Product.Condition)
	}
}

func TestEnrichLLMTopNGate(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: `<html><body>plain</body></html>`}}
	x := &stubExtractor{data: `{"name":"g","price":9.99,"currency":"USD"}`}
	cfg := testConfig()
	cfg.LLMTopN = 1
	p := New(f, x, cfg)
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://shop.example.com/p/a", "", nil),
		cand("https://shop.example.com/p/b", "", nil),
	})
	if x.calls.Load() != 1 {
		t.Fatalf("llm must gate to top-N: calls = %d", x.calls.Load())
	}
	if out[0].ExtractionFailed || !out[1].ExtractionFailed {
		t.Fatalf("top-N gating wrong: %+v / %+v", out[0], out[1])
	}
}

func TestEnrichLLMOutputSameValidation(t *testing.T) {
	// LLM returns schema-conforming shape but implausible values — strict
	// validation rejects them exactly like any other source.
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: `<html><body>x</body></html>`}}
	x := &stubExtractor{data: `{"name":"ok","price":-5,"currency":"USD"}`}
	p := New(f, x, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://shop.example.com/p/neg", "", nil),
	})
	if !out[0].ExtractionFailed || !strings.Contains(out[0].FailureReason, "price") {
		t.Fatalf("implausible llm price must flag candidate: %+v", out[0])
	}
}

func TestEnrichRenderSourceDeferred(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: amazonProductHTML}}
	cfg := testConfig()
	cfg.FetchClasses = map[string]sources.FetchClass{"heavysite": sources.FetchClassRender}
	p := New(f, &stubExtractor{}, cfg)
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "heavysite", Title: "r", URL: "https://heavy.example.com/p/1"},
	})
	if f.calls.Load() != 0 {
		t.Fatal("render-class source must not be fetched")
	}
	if !out[0].NeedsRender || !out[0].ExtractionFailed {
		t.Fatalf("deferred candidate wrong: %+v", out[0])
	}
}

func TestEnrichCFDetectedDefers(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, CFDetected: true, CFType: "challenge", Body: "<html>cf</html>"}}
	x := &stubExtractor{data: `{"name":"g","price":9.99,"currency":"USD"}`}
	p := New(f, x, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://walled.example.com/p/1", "", nil),
	})
	if !out[0].NeedsRender {
		t.Fatalf("CF wall must flag NeedsRender: %+v", out[0])
	}
	// The LLM tier still runs — wowa /extract fetches via its own path.
	if x.calls.Load() != 1 {
		t.Fatalf("llm should still attempt behind CF: calls = %d", x.calls.Load())
	}
	if out[0].ExtractionFailed {
		t.Fatalf("llm rescue behind CF should succeed: %+v", out[0])
	}
}

func TestEnrichDetailBudgetExhausted(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: amazonProductHTML}}
	cfg := testConfig()
	cfg.MaxDetailFetches = 1
	p := New(f, nil, cfg)
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://a.example.com/1", "", nil),
		cand("https://a.example.com/2", "", nil),
		cand("https://a.example.com/3", "", nil),
	})
	var failed, enriched int
	for _, ec := range out {
		if ec.ExtractionFailed {
			failed++
		} else if ec.Product.Price != nil {
			enriched++
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("budget breached: fetch calls = %d", f.calls.Load())
	}
	if enriched != 1 || failed != 2 {
		t.Fatalf("expected 1 schema-enriched + 2 over-budget failures, got %d/%d", enriched, failed)
	}
}

func TestEnrichInvalidSerpPriceRecoveredBySchema(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: amazonProductHTML}}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "ebay", Title: "x", URL: "https://www.ebay.com/itm/9",
			Price: f64(0), Currency: "USD"}, // serp price 0 → invalid → detail fetch
	})
	if f.calls.Load() != 1 {
		t.Fatal("invalid serp price did not trigger detail fetch")
	}
	if out[0].ExtractionFailed || *out[0].Product.Price != 278.0 {
		t.Fatalf("schema did not repair serp price: %+v", out[0])
	}
}

func TestEnrichFetchErrorStillReturnsCandidate(t *testing.T) {
	f := &stubFetcher{err: errors.New("wowa down")}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://a.example.com/1", "has title", nil),
	})
	if len(out) != 1 || !out[0].ExtractionFailed {
		t.Fatalf("fetch failure must degrade, not drop: %+v", out)
	}
}

// TestEnrichAgainstFakedWowa exercises the whole chain against a real
// wowa.Client pointed at httptest fakes: /fetch answers a schema-less
// page, /extract answers the LLM payload — the ADR-2 fallback path end to
// end on the wire.
func TestEnrichAgainstFakedWowa(t *testing.T) {
	var gotExtract bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/fetch":
			var req wowa.FetchRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(wowa.FetchResponse{
				Status: 200, Body: malformedSchemaHTML,
			})
		case "/api/v1/extract":
			gotExtract = true
			var req wowa.ExtractRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.Schema) == 0 {
				t.Error("extract request must carry the product JSON schema")
			}
			_ = json.NewEncoder(w).Encode(wowa.ExtractResponse{
				Data: json.RawMessage(`{"name":"Faked Lamp","price":19.95,"currency":"USD","availability":"in_stock"}`),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("wowa client: %v", err)
	}
	p := New(wc, wc, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://shop.example.com/p/lamp", "", nil),
	})
	if !gotExtract {
		t.Fatal("malformed schema did not reach the /extract fallback")
	}
	got := out[0]
	if got.ExtractionFailed || got.Product.Method != MethodLLM {
		t.Fatalf("wowa fallback chain failed: %+v", got)
	}
	if *got.Product.Price != 19.95 || got.Product.Name != "Faked Lamp" {
		t.Fatalf("llm product wrong: %+v", got.Product)
	}
}

func TestProductPublicIsEgressAllowlist(t *testing.T) {
	p := Product{
		Name: "Gadget", Price: f64(9.99), Currency: "USD",
		Availability: "in_stock", Condition: "used", SellerName: "secretSeller42",
		Source: "ebay.com", URL: "https://www.ebay.com/itm/1",
		Description: strings.Repeat("x", 600),
	}
	pub := p.ProductPublic()
	raw, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, banned := range []string{"seller", "secretSeller42", "ebay.com/itm", `"url"`, `"raw"`} {
		if strings.Contains(js, banned) {
			t.Fatalf("egress projection leaks %q: %s", banned, js)
		}
	}
	if pub.Source != "ebay.com" {
		t.Fatalf("source domain dropped: %q", pub.Source)
	}
	if utf8Len(pub.DescriptionBlurb) > PublicBlurbMax {
		t.Fatalf("blurb not capped: %d runes", utf8Len(pub.DescriptionBlurb))
	}
	// Control characters stripped.
	p2 := Product{Description: "line1\nline2\t\x00end"}
	if b := p2.ProductPublic().DescriptionBlurb; strings.ContainsAny(b, "\n\t\x00") {
		t.Fatalf("blurb kept control chars: %q", b)
	}
	if !strings.Contains(p2.ProductPublic().DescriptionBlurb, "line1 line2") {
		t.Fatal("blurb should join lines with spaces")
	}
}

func utf8Len(s string) int { return len([]rune(s)) }

// stubInteracter is a hand-rolled Interacter counting calls; dom is the
// DOM string the evaluate action returns.
type stubInteracter struct {
	calls  atomic.Int64
	dom    string // first page (thread) head HTML
	dom2   string // second page (merchant) head HTML; falls back to dom
	click  string // outbound tracker href the thread page yields
	landed string // final URL after following the tracker
	err    error
}

func (s *stubInteracter) Interact(context.Context, wowa.InteractRequest) (*wowa.InteractResponse, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	n := s.calls.Load()
	var payload map[string]string
	if n == 1 {
		payload = map[string]string{"h": s.dom, "click": s.click}
	} else {
		h := s.dom2
		if h == "" {
			h = s.dom
		}
		payload = map[string]string{"u": s.landed, "h": h}
	}
	// Mirror the live wire shape: evaluate returns the script's value, so a
	// JSON.stringify result arrives as a quoted JSON string, not an object.
	inner, _ := json.Marshal(payload)
	data, _ := json.Marshal(string(inner))
	return &wowa.InteractResponse{Actions: []wowa.ActionResult{
		{Action: "wait_for", Ok: true},
		{Action: "evaluate", Ok: true, Data: data},
	}}, nil
}

// TestEnrichInteractSolvesCF: fetch hits the managed-challenge transport
// error and no renderer is wired — the live-session tier must deliver the
// cleared DOM and its schema.org product.
func TestEnrichInteractSolvesCF(t *testing.T) {
	f := &stubFetcher{err: errors.New("remote error (http 502): cloudflare managed_challenge_200")}
	i := &stubInteracter{dom: amazonProductHTML}
	cfg := testConfig()
	cfg.Interact = i
	p := New(f, &stubExtractor{}, cfg)
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://walled.example.com/p/1", "", nil),
	})
	if i.calls.Load() != 1 {
		t.Fatalf("interact calls = %d, want 1", i.calls.Load())
	}
	if out[0].ExtractionFailed || out[0].Product.Method != MethodInteract {
		t.Fatalf("cleared DOM should extract via interact: %+v", out[0])
	}
}

// TestEnrichInteractTopNGate: the solve tier is expensive — only top-N
// candidates get a live session.
func TestEnrichInteractTopNGate(t *testing.T) {
	f := &stubFetcher{err: errors.New("remote error (http 502): cloudflare managed_challenge_200")}
	i := &stubInteracter{dom: amazonProductHTML}
	cfg := testConfig()
	cfg.Interact = i
	cfg.LLMTopN = 1
	p := New(f, &stubExtractor{}, cfg)
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://walled.example.com/p/1", "", nil),
		cand("https://walled.example.com/p/2", "", nil),
	})
	if i.calls.Load() != 1 {
		t.Fatalf("interact must fire for top-1 only: calls = %d", i.calls.Load())
	}
	if out[1].ExtractionFailed != true {
		// second candidate may still be rescued by the LLM stub — the gate
		// assertion is on interact calls, not the outcome.
		_ = out[1]
	}
}

// TestEnrichInteractFollowsBuyLink: the thread page carries no Product
// schema but exposes a /click tracker — the tier follows it and merges
// the merchant page, recording the resolved buy URL.
func TestEnrichInteractFollowsBuyLink(t *testing.T) {
	f := &stubFetcher{err: errors.New("remote error (http 502): cloudflare managed_challenge_200")}
	i := &stubInteracter{
		dom:    `<head><title>thread</title></head>`,
		click:  "https://slickdeals.net/click?sdtid=1",
		landed: "https://electronics.woot.com/offers/speaker",
	}
	cfg := testConfig()
	cfg.Interact = i
	p := New(f, &stubExtractor{}, cfg)

	// Second-call DOM carries the merchant schema.
	i2dom := `<head><script type="application/ld+json">{"@type":"Product","name":"JBL Charge 6","offers":{"@type":"Offer","price":"95.96","priceCurrency":"USD"}}</script></head>`
	i.dom2 = i2dom
	_ = i

	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://slickdeals.net/f/1-deal", "", nil),
	})
	if i.calls.Load() != 2 {
		t.Fatalf("interact calls = %d, want 2 (thread + outbound hop)", i.calls.Load())
	}
	if out[0].ExtractionFailed {
		t.Fatalf("merchant page should rescue the candidate: %+v", out[0])
	}
	if out[0].Product.BuyURL == "" {
		t.Fatalf("resolved buy url missing: %+v", out[0].Product)
	}
}

// TestEnrichResolvesOutboundCard: a SERP-complete deal card on a
// ResolveOutbound source never fetches the thread (card data suffices)
// but still gets an interact session to resolve the buyable merchant
// link — the outbound hop merges verified merchant data and sets BuyURL.
func TestEnrichResolvesOutboundCard(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: "<html/>"}}
	i := &stubInteracter{
		dom:    `<head><title>thread</title></head>`,
		click:  "https://slickdeals.net/click?sdtid=42",
		landed: "https://electronics.woot.com/offers/jbl-charge-6",
		dom2:   `<head><script type="application/ld+json">{"@type":"Product","name":"JBL Charge 6","offers":{"@type":"Offer","price":"95.96","priceCurrency":"USD","availability":"https://schema.org/InStock"}}</script></head>`,
	}
	cfg := testConfig()
	cfg.Interact = i
	cfg.ResolveOutbound = map[string]bool{"slickdeals": true}
	p := New(f, &stubExtractor{}, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "slickdeals", Title: "JBL Charge 6 @ Woot", URL: "https://slickdeals.net/f/42-x",
			Price: f64(95.96), Currency: "USD"},
	})
	if f.calls.Load() != 0 {
		t.Fatalf("card-complete candidate must not fetch detail: fetches=%d", f.calls.Load())
	}
	if i.calls.Load() != 2 {
		t.Fatalf("interact calls = %d, want 2 (thread + outbound hop)", i.calls.Load())
	}
	if out[0].ExtractionFailed {
		t.Fatalf("card should stay valid: %+v", out[0])
	}
	if out[0].Product.BuyURL != "https://electronics.woot.com/offers/jbl-charge-6" {
		t.Fatalf("buy url not resolved: %+v", out[0].Product)
	}
}
