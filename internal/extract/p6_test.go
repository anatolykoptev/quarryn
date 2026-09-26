package extract

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/sources"
)

// stubRenderer is a hand-rolled Renderer counting calls.
type stubRenderer struct {
	calls atomic.Int64
	html  string
	err   error
}

func (s *stubRenderer) Render(context.Context, wowa.RenderRequest) (*wowa.RenderResponse, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &wowa.RenderResponse{Status: 200, HTML: s.html}, nil
}

// TestEnrichLLMDailyBudgetExhausted — after EXTRACT_LLM_DAILY_MAX calls the
// fallback hard-stops: the candidate keeps its SERP data, is flagged
// budget-exhausted, and no further /extract call is placed.
func TestEnrichLLMDailyBudgetExhausted(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: "<html>no schema</html>"}}
	x := &stubExtractor{data: `{"name":"Rescued","price":9.99,"currency":"USD"}`}
	cfg := testConfig()
	cfg.LLMDailyMax = 1
	p := New(f, x, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://a.example.com/1", "", nil),
		cand("https://a.example.com/2", "", nil),
	})
	if x.calls.Load() != 1 {
		t.Fatalf("llm calls = %d, want 1 (daily cap)", x.calls.Load())
	}
	var rescued, exhausted int
	for _, ec := range out {
		switch {
		case ec.LLMBudgetExhausted:
			exhausted++
		case !ec.ExtractionFailed:
			rescued++
		}
	}
	if rescued != 1 || exhausted != 1 {
		t.Fatalf("want 1 rescued + 1 budget-exhausted, got %d/%d", rescued, exhausted)
	}
}

// TestEnrichLLMBudgetResetsOnRollover — the day-bucketed counter resets at
// the UTC day boundary (the injected clock jumps a day).
func TestEnrichLLMBudgetResetsOnRollover(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: "<html>no schema</html>"}}
	x := &stubExtractor{data: `{"name":"Rescued","price":9.99,"currency":"USD"}`}
	cfg := testConfig()
	cfg.LLMDailyMax = 1
	p := New(f, x, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{cand("https://a.example.com/1", "", nil)})
	if out[0].LLMBudgetExhausted || x.calls.Load() != 1 {
		t.Fatalf("first call should consume budget: %+v calls=%d", out[0], x.calls.Load())
	}
	out = p.Enrich(t.Context(), []sources.Candidate{cand("https://a.example.com/2", "", nil)})
	if !out[0].LLMBudgetExhausted || x.calls.Load() != 1 {
		t.Fatalf("same-day call must be capped: %+v calls=%d", out[0], x.calls.Load())
	}

	// Roll the clock past the UTC day boundary — the counter resets.
	p.llmBudget.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	out = p.Enrich(t.Context(), []sources.Candidate{cand("https://a.example.com/3", "", nil)})
	if out[0].LLMBudgetExhausted || x.calls.Load() != 2 {
		t.Fatalf("post-rollover call should run: %+v calls=%d", out[0], x.calls.Load())
	}
}

// TestEnrichRenderClassUsesRenderer — a FetchClass=render adapter routes
// the detail fetch through wowa /render (P6); the rendered HTML parses via
// schema.org like a plain detail fetch and the candidate is NOT flagged
// NeedsRender.
func TestEnrichRenderClassUsesRenderer(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, Body: "<html/>"}}
	r := &stubRenderer{html: amazonProductHTML}
	cfg := testConfig()
	cfg.FetchClasses = map[string]sources.FetchClass{"heavysite": sources.FetchClassRender}
	cfg.Render = r
	p := New(f, nil, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "heavysite", Title: "Sony WH-1000XM5", URL: "https://heavy.example.com/p/1"},
	})
	if f.calls.Load() != 0 {
		t.Fatal("render-class source must not hit plain fetch")
	}
	if r.calls.Load() != 1 {
		t.Fatalf("render calls = %d, want 1", r.calls.Load())
	}
	if out[0].NeedsRender || out[0].ExtractionFailed {
		t.Fatalf("rendered candidate wrongly flagged: %+v", out[0])
	}
	if out[0].Product.Method != MethodRender || *out[0].Product.PriceMinor != 27800 {
		t.Fatalf("render product = %+v", out[0].Product)
	}
}

// TestEnrichCFEscalatesToRender — a CF-challenged plain fetch escalates to
// the render tier; on success the candidate is fully extracted.
func TestEnrichCFEscalatesToRender(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, CFDetected: true, CFType: "challenge", Body: "<html>cf</html>"}}
	r := &stubRenderer{html: amazonProductHTML}
	cfg := testConfig()
	cfg.Render = r
	p := New(f, nil, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://walled.example.com/p/1", "", nil),
	})
	if f.calls.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("fetch=%d render=%d, want 1/1", f.calls.Load(), r.calls.Load())
	}
	if out[0].NeedsRender || out[0].ExtractionFailed {
		t.Fatalf("successful render must clear the flag: %+v", out[0])
	}
	if out[0].Product.Method != MethodRender {
		t.Fatalf("method = %q, want render", out[0].Product.Method)
	}
}

// TestEnrichRenderFailureStillTriesLLM — CF escalation with a failing
// render: the candidate is flagged NeedsRender (match excludes it) but the
// LLM tier still runs — wowa /extract fetches through its own solver path.
func TestEnrichRenderFailureStillTriesLLM(t *testing.T) {
	f := &stubFetcher{resp: &wowa.FetchResponse{Status: 200, CFDetected: true, Body: "<html>cf</html>"}}
	r := &stubRenderer{err: errors.New("chrome crashed")}
	x := &stubExtractor{data: `{"name":"Rescued","price":9.99,"currency":"USD"}`}
	cfg := testConfig()
	cfg.Render = r
	p := New(f, x, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://walled.example.com/p/1", "", nil),
	})
	if !out[0].NeedsRender {
		t.Fatalf("failed render must keep NeedsRender: %+v", out[0])
	}
	if x.calls.Load() != 1 {
		t.Fatalf("llm should still attempt after render failure: calls = %d", x.calls.Load())
	}
	if out[0].ExtractionFailed {
		t.Fatalf("llm rescue should still fill the product: %+v", out[0])
	}
}

// TestEnrichDeclaredRenderSkipsLLM — a declared render-class adapter never
// reaches the LLM tier even when the render fails (wowa /extract reads via
// /read, unrendered — same wall).
func TestEnrichDeclaredRenderSkipsLLM(t *testing.T) {
	f := &stubFetcher{}
	r := &stubRenderer{err: errors.New("chrome crashed")}
	x := &stubExtractor{data: `{"name":"x","price":1,"currency":"USD"}`}
	cfg := testConfig()
	cfg.FetchClasses = map[string]sources.FetchClass{"heavysite": sources.FetchClassRender}
	cfg.Render = r
	p := New(f, x, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "heavysite", Title: "t", URL: "https://heavy.example.com/p/1"},
	})
	if x.calls.Load() != 0 {
		t.Fatal("render-class candidate must not reach the LLM tier")
	}
	if !out[0].NeedsRender || !out[0].ExtractionFailed {
		t.Fatalf("failed declared render should flag: %+v", out[0])
	}
}

// TestEnrichNoRendererKeepsDeferral — without a wired renderer the P3
// behaviour is unchanged: render-class and CF pages defer with
// NeedsRender.
func TestEnrichNoRendererKeepsDeferral(t *testing.T) {
	cfg := testConfig()
	cfg.FetchClasses = map[string]sources.FetchClass{"heavysite": sources.FetchClassRender}
	cfg.Render = nil
	p := New(&stubFetcher{}, nil, cfg)
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "heavysite", Title: "t", URL: "https://heavy.example.com/p/1"},
	})
	if !out[0].NeedsRender || !out[0].ExtractionFailed {
		t.Fatalf("no renderer must defer: %+v", out[0])
	}
}

// TestEnrichCFErrorEscalatesToRender — wowa surfaces a CF challenge page as
// a transport error ("remote error (http 502): cloudflare
// managed_challenge_200"), not as a CFDetected body. The detail tier must
// escalate that error signature to the render tier the same way.
func TestEnrichCFErrorEscalatesToRender(t *testing.T) {
	f := &stubFetcher{err: errors.New("wowa: fetch: remote error (http 502): cloudflare managed_challenge_200 (HTTP 200, ray abc123-SJC)")}
	r := &stubRenderer{html: amazonProductHTML}
	cfg := testConfig()
	cfg.Render = r
	p := New(f, nil, cfg)

	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://slickdeals.net/f/1-widget", "widget", nil),
	})
	if f.calls.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("fetch=%d render=%d, want 1/1", f.calls.Load(), r.calls.Load())
	}
	if out[0].NeedsRender || out[0].ExtractionFailed {
		t.Fatalf("CF-error candidate wrongly flagged: %+v", out[0])
	}
	if out[0].Product.Method != MethodRender || out[0].Product.PriceMinor == nil {
		t.Fatalf("rendered product = %+v", out[0].Product)
	}
}

// TestEnrichCFErrorNoRendererMarksNeedsRender — same CF error with no
// renderer wired degrades to the NeedsRender deferral, not fetch_failed.
func TestEnrichCFErrorNoRendererMarksNeedsRender(t *testing.T) {
	f := &stubFetcher{err: errors.New("remote error (http 502): cloudflare managed_challenge_200")}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		cand("https://slickdeals.net/f/2-widget", "widget", nil),
	})
	if !out[0].NeedsRender {
		t.Fatalf("no-renderer CF error must flag NeedsRender: %+v", out[0])
	}
}
