package extract

import (
	"context"
	"encoding/json"
	"log/slog"
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
// fallback) | render_deferred (page needs JS render — P6) | over_budget
// (detail-fetch budget exhausted) | fetch_failed | incomplete (required
// fields never obtained) | invalid (values rejected by strict validation).
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
	// CandidateTimeout is the per-candidate bound covering cache lookup,
	// detail fetch and LLM call. Default 45s.
	CandidateTimeout time.Duration
	// FetchTimeoutSecs is the wire timeout handed to wowa /fetch.
	// Default 25.
	FetchTimeoutSecs int
	// LLMMaxChars caps page content fed to /extract. Default 12000.
	LLMMaxChars int
	// FetchClasses maps adapter name → declared FetchClass (ADR-13);
	// render-class sources skip detail work. Unknown sources default to
	// fetch.
	FetchClasses map[string]sources.FetchClass
	// Cache is the ADR-7 extraction cache (L1 + optional Redis L2).
	// Injected, not wrapped; nil disables caching.
	Cache *cache.Cache
}

func (c *Config) applyDefaults() {
	if c.LLMTopN <= 0 {
		c.LLMTopN = 10
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
}

// Pipeline is the extraction stage: serp → (cache) → schema.org detail →
// fenced LLM, with strict validation on everything it emits. Construct via
// New; safe for concurrent Enrich calls.
type Pipeline struct {
	fetch Fetcher
	llm   LLMCaller
	cfg   Config
}

// New builds the pipeline. fetch/llm are the wowa client surfaces (llm may
// be nil to disable the fallback tier); cfg carries limits, the source→
// FetchClass map and the injected cache.
func New(fetch Fetcher, llm LLMCaller, cfg Config) *Pipeline {
	cfg.applyDefaults()
	return &Pipeline{fetch: fetch, llm: llm, cfg: cfg}
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

	var g errgroup.Group
	g.SetLimit(p.cfg.Concurrency)
	for i := range cands {
		g.Go(func() error {
			cctx, cancel := context.WithTimeout(ctx, p.cfg.CandidateTimeout)
			defer cancel()
			out[i] = p.enrichCandidate(cctx, i, cands[i], &budget)
			return nil // per-candidate failure is data, never fatal
		})
	}
	_ = g.Wait()
	return out
}

// enrichCandidate runs one candidate through serp → cache → detail → LLM,
// then stamps the outcome metric and the validation flags.
func (p *Pipeline) enrichCandidate(ctx context.Context, rank int, c sources.Candidate, budget *atomic.Int64) EnrichedCandidate {
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
		outcome = p.extractDetail(ctx, rank, c, &prod, &ec, budget)
	}

	if probs := prod.problems(); len(probs) > 0 {
		ec.ExtractionFailed = true
		ec.FailureReason = strings.Join(probs, "; ")
		if outcome == "" {
			switch {
			case ec.NeedsRender:
				outcome = "render_deferred"
			case missingRequired(&prod):
				outcome = "incomplete"
			default:
				outcome = "invalid"
			}
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
		(prod.Method == MethodSchema || prod.Method == MethodLLM) {
		if raw, err := json.Marshal(prod); err == nil {
			p.cfg.Cache.Set(ctx, key, raw)
		}
	}
	ec.Product = prod
	return ec
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

// extractDetail runs the detail-fetch + LLM tiers for a candidate whose
// SERP fields did not validate. It mutates prod and ec, and returns a
// disposition outcome only for hard stops ("render_deferred",
// "over_budget", "fetch_failed"); "" leaves outcome selection to the
// caller's final state.
func (p *Pipeline) extractDetail(ctx context.Context, rank int, c sources.Candidate, prod *Product, ec *EnrichedCandidate, budget *atomic.Int64) string {
	if p.fetchClass(c.Source) == sources.FetchClassRender {
		// Render-class pages need the P6 render path for BOTH schema.org and
		// LLM (wowa /extract reads via /read, unrendered) — skip both, flag
		// for the render stage.
		ec.NeedsRender = true
		return "render_deferred"
	}
	if budget.Add(-1) < 0 {
		return "over_budget"
	}
	if p.fetch == nil {
		return "fetch_failed"
	}

	resp, err := p.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         c.URL,
		TimeoutSecs: p.cfg.FetchTimeoutSecs,
	})
	switch {
	case err != nil:
		// wowa is down — the /extract fallback would fail identically.
		slog.Warn("extract: detail fetch failed",
			slog.String("url", c.URL), slog.Any("error", err))
		return "fetch_failed"
	case resp.Status >= 400:
		// Dead page — the LLM would hit the same corpse; skip everything.
		return "fetch_failed"
	case resp.CFDetected:
		// Bot-wall challenge page: schema.org parse would find nothing, and
		// the render tier (P6) owns the retry. The LLM tier still runs —
		// wowa /extract fetches through its own solver path.
		ec.NeedsRender = true
	default:
		p.schemaMerge(c, resp.Body, prod)
	}

	p.tryLLM(ctx, rank, c, prod)
	return ""
}

// schemaMerge parses a fetched page body for schema.org Product data and
// merges it into prod.
func (p *Pipeline) schemaMerge(c sources.Candidate, body string, prod *Product) {
	sp, err := productFromSchema([]byte(body), c.URL)
	if err != nil || sp == nil {
		slog.Debug("extract: no schema.org product",
			slog.String("url", c.URL), slog.Any("error", err))
		return
	}
	mergeMissing(prod, sp)
	prod.Method = MethodSchema
}

// tryLLM runs the fenced LLM tier when the ADR-2 gate allows: top-N
// funnel-ranked survivors still missing required fields.
func (p *Pipeline) tryLLM(ctx context.Context, rank int, c sources.Candidate, prod *Product) {
	if p.llm == nil || rank >= p.cfg.LLMTopN || !missingRequired(prod) {
		return
	}
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
		Price:        c.Price,
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
	if !priceValid(dst.Price) && src.Price != nil {
		dst.Price = src.Price
	}
	if !currencyOK(dst.Currency) && src.Currency != "" {
		dst.Currency = src.Currency
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
