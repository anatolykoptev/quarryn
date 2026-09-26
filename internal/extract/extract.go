package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/sources"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/sync/errgroup"
)

// extractOutcomes is the stage's observability surface: one increment per
// candidate. Vocabulary: serp (SERP fields sufficient) | cache (extraction
// cache hit) | schema (detail fetch + schema.org fill) | llm (fenced LLM
// fallback) | render (stealth-Chrome detail page parsed) | render_deferred
// (page needs JS render but no renderer is wired) | render_failed |
// over_budget (detail-fetch or per-request page budget exhausted) |
// llm_budget (daily LLM spend cap reached — candidate kept unenriched) |
// fetch_failed | incomplete (required fields never obtained) | invalid
// (values rejected by strict validation).
var extractOutcomes = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "prodsearch",
		Name:      "extract_outcomes_total",
		Help:      "Product-extraction disposition per candidate.",
	},
	[]string{"outcome"},
)

// Fetcher is the go-kit/wowa /api/v1/fetch surface used for detail pages —
// the same narrow slice the sources stage declares.
type Fetcher interface {
	Fetch(ctx context.Context, req wowa.FetchRequest) (*wowa.FetchResponse, error)
}

// LLMCaller is the wowa /api/v1/extract surface — the fenced LLM fallback.
// *wowa.Client satisfies both interfaces; nil disables the LLM tier
// entirely.
type LLMCaller interface {
	Extract(ctx context.Context, req wowa.ExtractRequest) (*wowa.ExtractResponse, error)
}

// Renderer is the wowa /api/v1/render surface (stealth Chrome) — the P6
// escalation path for detail pages the plain fetch cannot read (adapter
// declared FetchClass=render, or the fetch answered a CF challenge).
// *wowa.Client satisfies it; in production it is the detail-stage fetch
// gate so renders share the page budget and domain pacing. Nil keeps the
// P3 deferral behaviour.
type Renderer interface {
	Render(ctx context.Context, req wowa.RenderRequest) (*wowa.RenderResponse, error)
}

// Interacter is the wowa /api/v1/chrome/interact surface — a real stealth
// Chrome session with auto_bypass that waits out a managed CF challenge
// and returns the cleared DOM. The solve tier for detail pages that beat
// the plain /render navigation. *wowa.Client satisfies it; nil disables.
type Interacter interface {
	Interact(ctx context.Context, req wowa.InteractRequest) (*wowa.InteractResponse, error)
}

// Config bundles pipeline limits and injected dependencies.
type Config struct {
	// LLMTopN gates the LLM fallback to the top-N funnel-ranked candidates
	// (ADR-2). Default 10; 0 → default.
	LLMTopN int
	// MaxDetailFetches bounds product-page fetches per Enrich call.
	// Default 15.
	MaxDetailFetches int
	// Concurrency bounds parallel candidate processing. Default 4.
	Concurrency int
	// MaxBrowserCalls bounds interact resolutions per search (one unit
	// covers session + outbound hop; ~30-60s of real Chrome each). It serves a
	// different purpose than detail fetches — it must not starve behind
	// cheap PDP fetches in the shared detail budget. <=0 defaults to 12.
	MaxBrowserCalls int
	// CandidateTimeout is the per-candidate bound covering cache lookup,
	// detail fetch and LLM call. Default 45s.
	CandidateTimeout time.Duration
	// FetchTimeoutSecs is the wire timeout handed to wowa /fetch.
	// Default 25.
	FetchTimeoutSecs int
	// LLMMaxChars caps page content fed to /extract. Default 12000.
	LLMMaxChars int
	// LLMDailyMax is the process-local daily cap on wowa /extract calls
	// (EXTRACT_LLM_DAILY_MAX, ADR-12). Default 50. The counter resets at
	// UTC midnight and on restart — it bounds LLM spend per process, not
	// per calendar; persistence was deliberately skipped (budget.go).
	LLMDailyMax int
	// Render is the P6 stealth-Chrome escalation for detail pages the
	// plain fetch cannot read (FetchClass=render adapters, CF-challenged
	// pages). The call consumes the same detail budget — render is a
	// detail fetch. Nil keeps the P3 deferral (NeedsRender + skip).
	Render Renderer
	// Interact is the CF-solve tier between render and LLM: a live browser
	// session (auto_bypass) that waits out managed challenges /render does
	// not clear. Gated to top-N candidates — a session costs ~12s of real
	// browser time. Counts against the detail budget. Nil disables.
	Interact Interacter
	// FetchClasses maps adapter name → declared FetchClass (ADR-13).
	// Render-class sources route detail work through Render instead of
	// the plain fetcher. Unknown sources default to fetch.
	FetchClasses map[string]sources.FetchClass
	// ResolveOutbound marks deal-aggregator sources (SourceSpec) whose
	// SERP-complete cards still deserve an interact session to resolve
	// the outbound merchant link — the card URL is a thread, not a
	// buyable page.
	ResolveOutbound map[string]bool
	// Cache is the ADR-7 extraction cache (L1 + optional Redis L2).
	// Injected, not wrapped; nil disables caching.
	Cache *cache.Cache
}

func (c *Config) applyDefaults() {
	if c.LLMTopN <= 0 {
		c.LLMTopN = 10
	}
	if c.MaxBrowserCalls <= 0 {
		c.MaxBrowserCalls = 12
	}
	if c.MaxDetailFetches <= 0 {
		c.MaxDetailFetches = 15
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.CandidateTimeout <= 0 {
		c.CandidateTimeout = 45 * time.Second
	}
	if c.FetchTimeoutSecs <= 0 {
		c.FetchTimeoutSecs = 25
	}
	if c.LLMMaxChars <= 0 {
		c.LLMMaxChars = 12000
	}
	if c.LLMDailyMax <= 0 {
		c.LLMDailyMax = 50
	}
}

// Pipeline is the extraction stage: serp → (cache) → schema.org detail →
// fenced LLM, with strict validation on everything it emits. Construct via
// New; safe for concurrent Enrich calls.
type Pipeline struct {
	fetch     Fetcher
	llm       LLMCaller
	cfg       Config
	llmBudget *dailyBudget
}

// New builds the pipeline. fetch/llm are the wowa client surfaces (llm may
// be nil to disable the fallback tier); cfg carries limits, the source→
// FetchClass map, the optional render escalation and the injected cache.
func New(fetch Fetcher, llm LLMCaller, cfg Config) *Pipeline {
	cfg.applyDefaults()
	return &Pipeline{fetch: fetch, llm: llm, cfg: cfg, llmBudget: newDailyBudget(cfg.LLMDailyMax)}
}

// Close releases pipeline resources (the cache's cleanup goroutine). The
// process-lifetime cache in the server never calls it; tests do.
func (p *Pipeline) Close() {
	if p.cfg.Cache != nil {
		p.cfg.Cache.Close()
	}
}

// Enrich maps each candidate through the extraction chain. Order and count
// are preserved — enrichment never drops a candidate, it only flags
// (ExtractionFailed / NeedsRender) and fills Product.
func (p *Pipeline) Enrich(ctx context.Context, cands []sources.Candidate) []EnrichedCandidate {
	out := make([]EnrichedCandidate, len(cands))
	var budget atomic.Int64
	budget.Store(int64(p.cfg.MaxDetailFetches))
	var browser atomic.Int64
	browser.Store(int64(p.cfg.MaxBrowserCalls))

	var g errgroup.Group
	g.SetLimit(p.cfg.Concurrency)
	for i := range cands {
		g.Go(func() error {
			cctx, cancel := context.WithTimeout(ctx, p.cfg.CandidateTimeout)
			defer cancel()
			out[i] = p.enrichCandidate(cctx, i, cands[i], &budget, &browser)
			return nil // per-candidate failure is data, never fatal
		})
	}
	_ = g.Wait()
	return out
}

// enrichCandidate runs one candidate through serp → cache → detail → LLM,
// then stamps the outcome metric and the validation flags.
func (p *Pipeline) enrichCandidate(ctx context.Context, rank int, c sources.Candidate, budget, browser *atomic.Int64) EnrichedCandidate {
	ec := EnrichedCandidate{Candidate: c}
	prod := productFromCandidate(c)
	key := cacheKey(c.URL)

	var outcome string
	if len(prod.problems()) > 0 {
		if cached, ok := p.cachedProduct(ctx, key); ok {
			extractOutcomes.WithLabelValues("cache").Inc()
			ec.Product = *cached
			return ec
		}
		outcome = p.extractDetail(ctx, rank, c, &prod, &ec, budget, browser)
	} else if p.cfg.ResolveOutbound[c.Source] && prod.BuyURL == "" {
		// SERP-complete card on a deal aggregator: the product is usable
		// as-is, but the buyable merchant link lives behind the thread's
		// outbound tracker — resolve it when the interact budget allows.
		p.tryInteract(ctx, rank, c, &prod, browser, true)
	}

	if probs := prod.problems(); len(probs) > 0 {
		ec.ExtractionFailed = true
		ec.FailureReason = strings.Join(probs, "; ")
		if outcome == "" {
			outcome = failureOutcome(&ec, &prod)
		}
	}
	if outcome == "" {
		outcome = prod.Method
	}
	extractOutcomes.WithLabelValues(outcome).Inc()

	// Cache only detail-extracted products that passed validation — serp
	// merges are free to recompute, and a poisoned entry would outlive the
	// candidate list by 24h.
	if !ec.ExtractionFailed && p.cfg.Cache != nil && key != "" &&
		(prod.Method == MethodSchema || prod.Method == MethodRender || prod.Method == MethodLLM) {
		if raw, err := json.Marshal(prod); err == nil {
			p.cfg.Cache.Set(ctx, key, raw)
		}
	}
	ec.Product = prod
	return ec
}

// failureOutcome picks the metric label for a failed candidate when the
// detail path left the choice open — the flag-based dispositions beat the
// generic incomplete/invalid split.
func failureOutcome(ec *EnrichedCandidate, prod *Product) string {
	switch {
	case ec.NeedsRender:
		return "render_failed"
	case ec.LLMBudgetExhausted:
		return "llm_budget"
	case missingRequired(prod):
		return "incomplete"
	default:
		return "invalid"
	}
}

// cachedProduct returns the validated product the cache holds for this
// URL's canonical key. A corrupt entry is treated as a miss.
func (p *Pipeline) cachedProduct(ctx context.Context, key string) (*Product, bool) {
	if p.cfg.Cache == nil || key == "" {
		return nil, false
	}
	raw, ok := p.cfg.Cache.Get(ctx, key)
	if !ok {
		return nil, false
	}
	var prod Product
	if err := json.Unmarshal(raw, &prod); err != nil || len(prod.problems()) > 0 {
		return nil, false
	}
	return &prod, true
}

// extractDetail runs the detail-fetch + render + LLM tiers for a candidate
// whose SERP fields did not validate. It mutates prod and ec, and returns a
// disposition outcome only for hard stops ("render_deferred",
// "render_failed", "over_budget", "fetch_failed"); "" leaves outcome
// selection to the caller's final state.
func (p *Pipeline) extractDetail(ctx context.Context, rank int, c sources.Candidate, prod *Product, ec *EnrichedCandidate, budget, browser *atomic.Int64) string {
	declaredRender := p.fetchClass(c.Source) == sources.FetchClassRender
	needRender := declaredRender
	if !declaredRender {
		escalate, outcome := p.fetchDetail(ctx, c, prod, budget)
		if outcome != "" {
			return outcome
		}
		needRender = escalate
	}

	if needRender {
		outcome := p.renderDetail(ctx, c, prod, budget)
		if outcome != "" {
			// The render tier could not deliver the page — flag the
			// candidate so match excludes it (match.go: NeedsRender →
			// excluded). On a CF escalation the LLM tier still runs:
			// wowa /extract fetches through its own solver path. For
			// declared render-class sources it is skipped entirely —
			// /extract reads via /read, unrendered, and would hit the same
			// wall.
			ec.NeedsRender = true
			if !declaredRender {
				// Render could not clear the wall — try the live-session
				// solve tier before spending an LLM call.
				if p.tryInteract(ctx, rank, c, prod, browser, false) {
					return ""
				}
				p.tryLLM(ctx, rank, c, prod, ec)
			}
			return outcome
		}
		if declaredRender {
			return ""
		}
	}

	p.tryLLM(ctx, rank, c, prod, ec)
	return ""
}

// fetchDetail runs the plain wowa /fetch detail tier for a fetch-class
// candidate, merging schema.org output into prod. It returns (needRender,
// outcome): a non-empty outcome is a hard stop ("over_budget" |
// "fetch_failed"), needRender marks a CF bot-wall escalation to the render
// tier.
func (p *Pipeline) fetchDetail(ctx context.Context, c sources.Candidate, prod *Product, budget *atomic.Int64) (needRender bool, outcome string) {
	if budget.Add(-1) < 0 {
		return false, "over_budget"
	}
	if p.fetch == nil {
		return false, "fetch_failed"
	}
	resp, err := p.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         c.URL,
		TimeoutSecs: p.cfg.FetchTimeoutSecs,
	})
	switch {
	case errors.Is(err, sources.ErrPageBudgetExhausted),
		errors.Is(err, sources.ErrDomainThrottled):
		// P6 gate stops: page budget spent or the domain throttled out this
		// search. Both mean "rank what exists" — no wire call or a refused
		// retry, never an error worth failing the search over.
		return false, "over_budget"
	case err != nil:
		if isCFChallengeError(err) {
			// Bot-wall answer surfaced as a transport error (wowa reports
			// the challenge page's HTTP 200 as a remote 502) — escalate to
			// the render tier exactly like a CFDetected body.
			slog.Info("extract: detail fetch hit CF challenge, escalating to render",
				slog.String("url", c.URL), slog.Any("error", err))
			return true, ""
		}
		// wowa is down — the /extract fallback would fail identically.
		slog.Warn("extract: detail fetch failed",
			slog.String("url", c.URL), slog.Any("error", err))
		return false, "fetch_failed"
	case resp.Status >= 400:
		// Dead page — the LLM would hit the same corpse; skip everything.
		return false, "fetch_failed"
	case resp.CFDetected:
		// Bot-wall challenge page: the plain fetch carried the page but a
		// schema.org parse would find nothing — escalate to the render tier.
		return true, ""
	default:
		p.schemaMerge(c, resp.Body, prod)
		return false, ""
	}
}

// isCFChallengeError reports whether a wowa fetch error is a bot-management
// challenge surfaced as a transport failure rather than a CFDetected body —
// e.g. "remote error (http 502): cloudflare managed_challenge_200".
func isCFChallengeError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "cloudflare") ||
		strings.Contains(s, "managed_challenge") ||
		strings.Contains(s, "cf_challenge") ||
		strings.Contains(s, "cf_detected")
}

// renderTimeoutSecs gives the render tier more headroom than a plain fetch —
// a CF challenge round-trip plus JS execution routinely outlives the 25s
// fetch bound. Server clamps to 60.
func renderTimeoutSecs(fetch int) int {
	if t := fetch * 2; t > 60 {
		return 60
	} else {
		return t
	}
}

// renderDetail fetches the page through wowa /render (stealth Chrome) and
// schema-merges the HTML. It consumes the same per-Enrich detail budget —
// a render is a detail fetch — while the gate's page budget and domain
// pacing bound the wire call itself. Returns "" on a successful parse
// (product marked MethodRender); otherwise the hard-stop outcome:
// "render_deferred" when no renderer is wired, "over_budget" when either
// budget refuses, "render_failed" on a call or parse failure.
func (p *Pipeline) renderDetail(ctx context.Context, c sources.Candidate, prod *Product, budget *atomic.Int64) string {
	if p.cfg.Render == nil {
		return "render_deferred"
	}
	if budget.Add(-1) < 0 {
		return "over_budget"
	}
	resp, err := p.cfg.Render.Render(ctx, wowa.RenderRequest{
		URL:         c.URL,
		TimeoutSecs: renderTimeoutSecs(p.cfg.FetchTimeoutSecs),
		Wait:        "domcontentloaded",
	})
	switch {
	case errors.Is(err, sources.ErrPageBudgetExhausted),
		errors.Is(err, sources.ErrDomainThrottled):
		return "over_budget"
	case err != nil:
		slog.Warn("extract: detail render failed",
			slog.String("url", c.URL), slog.Any("error", err))
		return "render_failed"
	case p.schemaMerge(c, resp.HTML, prod):
		prod.Method = MethodRender
		return ""
	default:
		return "render_failed"
	}
}

// schemaMerge parses a fetched page body for schema.org Product data and
// merges it into prod. Returns true when a product was merged.
func (p *Pipeline) schemaMerge(c sources.Candidate, body string, prod *Product) bool {
	return p.schemaMergeURL(c.URL, body, prod)
}

// schemaMergeURL parses body for schema.org Product data attributed to
// pageURL and merges it into prod. Returns true when a product merged.
func (p *Pipeline) schemaMergeURL(pageURL, body string, prod *Product) bool {
	sp, err := productFromSchema([]byte(body), pageURL)
	if err != nil || sp == nil {
		slog.Debug("extract: no schema.org product",
			slog.String("url", pageURL), slog.Any("error", err))
		return false
	}
	mergeMissing(prod, sp)
	prod.Method = MethodSchema
	return true
}

// tryInteract runs the CF-solve tier: wowa /chrome/interact with
// auto_bypass — a real browser session that waits out the challenge and
// returns the cleared DOM, which goes through the same schema.org parse as
// a plain fetch. Gated to top-N funnel-ranked candidates like the LLM tier
// (a session is expensive); counts against the detail budget. Returns true
// when the recovered DOM merged product data.
func (p *Pipeline) tryInteract(ctx context.Context, rank int, c sources.Candidate, prod *Product, browser *atomic.Int64, wantOutbound bool) bool {
	if p.cfg.Interact == nil {
		return false
	}
	if !wantOutbound && rank >= p.cfg.LLMTopN {
		return false
	}
	// One browser unit buys the whole resolution: the thread session AND
	// its outbound hop. Charging per call let call-1 sessions consume the
	// pool and then deny their own hops — wasted solves, no buy_url.
	if browser.Add(-1) < 0 {
		if wantOutbound {
			slog.Warn("extract: outbound resolve skipped, browser budget spent",
				slog.String("url", c.URL))
		}
		return false
	}
	// Named session so the second call (outbound-link resolution) reuses
	// the same cleared tab; destroyed at the end of each call.
	session := fmt.Sprintf("prodsearch-%d", time.Now().UnixNano())
	resp, err := p.cfg.Interact.Interact(ctx, wowa.InteractRequest{
		URL:         c.URL,
		AutoBypass:  true,
		TimeoutSecs: 60,
		Session:     session,
		Actions: []wowa.Action{
			outboundWaitAction(wantOutbound),
			{Type: "evaluate", Script: interactExtractJS},
		},
	})
	if err != nil {
		slog.Warn("extract: interact solve failed",
			slog.String("url", c.URL), slog.Any("error", err))
		return false
	}
	got, click := interactPayload(resp)
	merged := false
	if got != "" {
		if p.schemaMerge(c, got, prod) {
			prod.Method = MethodInteract
			merged = true
		}
		if og := ogDescription(got); og != "" && prod.Description == "" {
			prod.Description = og
		}
	}
	reportInteractYield(c.URL, got, click, wantOutbound)
	// Deal aggregators keep the buyable URL behind an outbound tracker
	// (slickdeals /click). Follow it in-session — the merchant page may
	// carry real Product schema the thread never had.
	if click == "" {
		return merged
	}
	merged = p.interactOutbound(ctx, session, click, c, prod) || merged
	return merged
}

func reportInteractYield(url, got, click string, wantOutbound bool) {
	switch {
	case got == "" && click == "":
		slog.Warn("extract: interact yielded no usable payload",
			slog.String("url", url))
	case wantOutbound && click == "":
		slog.Warn("extract: no outbound link on cleared page",
			slog.String("url", url))
	}
}

// interactExtractJS returns the pieces worth shipping back from a cleared
// page without hauling a megabyte DOM: <head> (JSON-LD/schema + og tags
// live there) and the deal aggregator's outbound href when present.
func outboundWaitAction(wantOutbound bool) wowa.Action {
	if !wantOutbound {
		return wowa.Action{Type: "wait_for", WaitMs: 12000}
	}
	// Selector wait beats the fixed sleep on both axes: returns as soon as
	// the cleared deal box renders, tolerates slow solves up to 25s, and a
	// miss must not abort the evaluate (skip_on_error keeps the chain).
	return wowa.Action{
		Type:        "wait_for",
		Selector:    `a[href*="/click?"]`,
		TimeoutMs:   25000,
		SkipOnError: true,
	}
}

const interactExtractJS = `JSON.stringify({
  h: document.head ? document.head.outerHTML.slice(0,400000) : "",
  click: (document.querySelector('a[href*="/click?"][href*="Get+Deal"]')
       || document.querySelector('a[href*="/click?"][href*="Main+CTA"]')
       || document.querySelector('a[href*="/click?"]')||{}).href || ""
})`

// interactOutbound follows the captured tracker URL inside the same
// session and schema-merges the merchant page it lands on. On success it
// records the resolved merchant URL as BuyURL.
func (p *Pipeline) interactOutbound(ctx context.Context, session, click string, c sources.Candidate, prod *Product) bool {
	// URL carries the merchant link directly: the named session reuses the
	// CF-cleared tab and the request's own navigate phase goes straight to
	// the merchant page — no second thread navigation, no re-solve.
	resp, err := p.cfg.Interact.Interact(ctx, wowa.InteractRequest{
		URL:         click,
		TimeoutSecs: 45,
		Session:     session,
		Actions: []wowa.Action{
			{Type: "wait_for", WaitMs: 8000},
			{Type: "evaluate", Script: `JSON.stringify({u:location.href,h:document.head?document.head.outerHTML.slice(0,400000):""})`},
			{Type: "destroy_session"},
		},
	})
	if err != nil {
		slog.Warn("extract: outbound hop failed",
			slog.String("url", c.URL), slog.Any("error", err))
		return false
	}
	body, landed := interactPayload(resp)
	switch {
	case landed == "":
		slog.Warn("extract: hop returned no landing URL",
			slog.String("url", c.URL))
	case domainOf(landed) == domainOf(c.URL):
		slog.Warn("extract: hop never left the aggregator",
			slog.String("url", c.URL), slog.String("landed", landed))
	default:
		if p.schemaMergeURL(landed, body, prod) {
			prod.Method = MethodInteract
			prod.BuyURL = cleanBuyURL(landed)
			return true
		}
		// Merchant page had no schema either — still worth surfacing the
		// resolved buy link.
		prod.BuyURL = cleanBuyURL(landed)
	}
	return false
}

// interactPayload unwraps the evaluate JSON the tier's scripts emit:
// "h" is the page head HTML, "u" the resolved location, "click" the
// captured outbound tracker href.
func interactPayload(resp *wowa.InteractResponse) (head, extra string) {
	if resp == nil {
		return "", ""
	}
	for _, a := range resp.Actions {
		if a.Action != "evaluate" || !a.Ok {
			continue
		}
		raw := a.Data
		// evaluate returns the script's value; a JSON.stringify payload
		// arrives as a quoted JSON string — unwrap before decoding.
		var str string
		if json.Unmarshal(raw, &str) == nil && strings.HasPrefix(str, "{") {
			raw = json.RawMessage(str)
		}
		var payload struct {
			H     string `json:"h"`
			U     string `json:"u"`
			Click string `json:"click"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			continue
		}
		if payload.Click != "" {
			extra = payload.Click
		}
		if payload.U != "" {
			extra = payload.U
		}
		if len(payload.H) > len(head) {
			head = payload.H
		}
	}
	return head, extra
}

// cleanBuyURL drops affiliate/tracking params from the resolved merchant
// URL via the shared denylist — the link stays valid and readable.
func cleanBuyURL(raw string) string {
	return sources.CleanTrackingURL(raw)
}

// ogDescription pulls <meta property="og:description"> from a DOM — thread
// pages carry no Product schema, so the og blurb is the best jeff context
// a cleared challenge page can give.
var ogDescRe = regexp.MustCompile(`(?i)<meta[^>]+property="og:description"[^>]+content="([^"]*)"`)

func ogDescription(html string) string {
	m := ogDescRe.FindStringSubmatch(html)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// tryLLM runs the fenced LLM tier when the gates allow: the ADR-2 top-N
// funnel-rank gate plus the ADR-12 daily spend cap (EXTRACT_LLM_DAILY_MAX).
// A spent budget is not an error — the candidate continues unenriched with
// LLMBudgetExhausted set so the outcome lands in the stage metrics.
func (p *Pipeline) tryLLM(ctx context.Context, rank int, c sources.Candidate, prod *Product, ec *EnrichedCandidate) {
	if p.llm == nil || rank >= p.cfg.LLMTopN || !missingRequired(prod) {
		return
	}
	if !p.llmBudget.tryConsume() {
		llmBudgetExhaustedTotal.Inc()
		ec.LLMBudgetExhausted = true
		slog.Info("extract: daily LLM budget spent — candidate stays unenriched",
			slog.String("url", c.URL))
		return
	}
	llmExtractCallsTotal.Inc()
	lp, err := p.llmExtract(ctx, c.URL)
	if err != nil {
		slog.Warn("extract: llm fallback failed",
			slog.String("url", c.URL), slog.Any("error", err))
		return
	}
	mergeMissing(prod, lp)
	prod.Method = MethodLLM
}

// fetchClass resolves a candidate's source adapter FetchClass; unknown
// sources default to fetch — detail pages always go through wowa regardless
// of how the adapter reached the marketplace (ADR-1).
func (p *Pipeline) fetchClass(source string) sources.FetchClass {
	if class, ok := p.cfg.FetchClasses[source]; ok {
		return class
	}
	return sources.FetchClassFetch
}

// productFromCandidate lifts the SERP-level candidate fields into a
// Product baseline. Everything is normalized; bounds stay for problems().
func productFromCandidate(c sources.Candidate) Product {
	return Product{
		Name:         strings.TrimSpace(c.Title),
		URL:          c.URL,
		PriceMinor:   c.PriceMinor,
		Currency:     normalizeCurrency(c.Currency),
		Condition:    normalizeCondition(c.Condition),
		Availability: normalizeAvailability(c.Availability),
		SellerName:   c.Seller,
		ImageURL:     c.ImageURL,
		Description:  c.Content,
		Source:       domainOf(c.URL),
		Method:       MethodSERP,
	}
}

// mergeMissing fills dst's absent or invalid fields from src (schema.org or
// LLM output, already normalized). Present-but-invalid dst values count as
// missing so a detail extraction can repair a bad SERP value.
func mergeMissing(dst *Product, src *Product) {
	mergeRequired(dst, src)
	mergeOptional(dst, src)
}

// mergeRequired fills the fields missingRequired gates on.
func mergeRequired(dst *Product, src *Product) {
	if strings.TrimSpace(dst.Name) == "" {
		dst.Name = src.Name
	}
	if !currencyOK(dst.Currency) && src.Currency != "" {
		dst.Currency = src.Currency
	}
	if !priceMinorValid(dst.PriceMinor, dst.Currency) && src.PriceMinor != nil {
		dst.PriceMinor = src.PriceMinor
	}
}

// mergeOptional fills the optional product attributes and provenance.
func mergeOptional(dst *Product, src *Product) {
	if dst.Availability == "" {
		dst.Availability = src.Availability
	}
	if dst.Condition == "" {
		dst.Condition = src.Condition
	}
	if (dst.Rating == nil || *dst.Rating < 0 || *dst.Rating > maxRating) && src.Rating != nil {
		dst.Rating = src.Rating
	}
	if dst.ImageURL == "" {
		dst.ImageURL = src.ImageURL
	}
	if dst.SellerName == "" {
		dst.SellerName = src.SellerName
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if len(dst.Raw) == 0 && len(src.Raw) > 0 {
		dst.Raw = src.Raw
	}
}
