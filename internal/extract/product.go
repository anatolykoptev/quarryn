// Package extract is the extraction+normalization stage (P3): it turns
// raw funnel candidates into validated Product records. Deterministic-first
// (ADR-2): SERP-level candidate fields are the baseline; fields still
// missing are filled by schema.org parsing of the product page
// (go-enriche/structured — the ONLY go-enriche package this module may
// import; the root and fetch packages would bypass the wowa-only egress
// rule, ADR-14), and a fenced wowa /api/v1/extract LLM call is the last
// resort for top-N survivors still lacking required fields. Every merged
// value passes the same strict validation — reject, never clamp — before
// it can reach the jeff path.
package extract

import (
	"encoding/json"

	"github.com/anatolykoptev/go-product-search/internal/sources"
)

// Method vocabulary — records which extraction path produced a Product.
const (
	// MethodSERP: SERP-level candidate metadata alone satisfied validation.
	MethodSERP = "serp"
	// MethodSchema: a detail-page fetch + schema.org parse contributed.
	MethodSchema = "schema"
	// MethodRender: a stealth-Chrome render + schema.org parse contributed
	// (P6 — the page needed JS, the render tier delivered it).
	MethodRender = "render"
	// MethodLLM: the wowa /api/v1/extract fallback contributed.
	MethodLLM = "llm"
	// MethodInteract: a cleared live-browser session's DOM contributed
	// (P-solve tier — the page stood behind a challenge /render lost).
	MethodInteract = "interact"
)

// Product is the normalized record this stage emits. Fields originate from
// SERP metadata, schema.org markup, or the LLM fallback; Method records the
// strongest path that contributed, and Raw carries provenance (the parsed
// schema.org item or the LLM payload) for auditability.
type Product struct {
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	Price        *float64        `json:"price,omitempty"`
	Currency     string          `json:"currency,omitempty"` // ISO 4217
	Availability string          `json:"availability,omitempty"`
	Condition    string          `json:"condition,omitempty"`
	SellerName   string          `json:"seller_name,omitempty"`
	Rating       *float64        `json:"rating,omitempty"` // schema.org 0-5 convention
	ImageURL     string          `json:"image_url,omitempty"`
	Description  string          `json:"description,omitempty"`
	Source       string          `json:"source"` // product-page domain, www stripped
	Method       string          `json:"method"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

// EnrichedCandidate is the stage output: the sourced candidate plus its
// normalized Product and the disposition flags downstream stages read.
type EnrichedCandidate struct {
	sources.Candidate

	Product Product `json:"product"`

	// ExtractionFailed marks a candidate whose merged product data is
	// missing required fields or failed strict validation (ADR-2). It is
	// dropped from the jeff path but still returnable as a degraded result.
	ExtractionFailed bool   `json:"extraction_failed,omitempty"`
	FailureReason    string `json:"failure_reason,omitempty"`

	// NeedsRender marks a candidate whose detail page requires JS
	// rendering — the adapter declared FetchClass=render, or the fetch hit
	// a bot-wall challenge — AND the P6 render tier did not deliver a
	// usable page (no renderer wired, render call failed, or budget
	// refused). Match excludes these: the product is untrusted or empty.
	// A successfully rendered page clears through schema.org parse like
	// any detail fetch and never sets this flag.
	NeedsRender bool `json:"needs_render,omitempty"`

	// LLMBudgetExhausted marks a candidate that skipped the fenced LLM
	// fallback because the process-local daily spend cap
	// (EXTRACT_LLM_DAILY_MAX) was reached. Typed data, not an error — the
	// candidate continues with whatever the cheaper tiers produced.
	LLMBudgetExhausted bool `json:"llm_budget_exhausted,omitempty"`
}

// PublicBlurbMax caps the description blurb that may leave the box.
const PublicBlurbMax = 500

// PublicProduct is the egress allow-list projection: the ONLY product
// fields permitted to leave the box — jeff state, LLM prompts and tool
// output are built from this shape (P4 wraps it into CandidateState). It
// deliberately excludes seller names, review text and the raw URL:
// marketplace identity (Source domain) is enough for jeff to weigh a
// candidate without leaking seller-identifying text.
type PublicProduct struct {
	Name             string   `json:"name"`
	Price            *float64 `json:"price,omitempty"`
	Currency         string   `json:"currency,omitempty"`
	Availability     string   `json:"availability,omitempty"`
	Condition        string   `json:"condition,omitempty"`
	Rating           *float64 `json:"rating,omitempty"`
	Source           string   `json:"source"`
	DescriptionBlurb string   `json:"description_blurb,omitempty"`
}

// ProductPublic returns the egress-safe projection of p: the allow-listed
// fields only, Description truncated to PublicBlurbMax runes with control
// characters stripped.
func (p Product) ProductPublic() PublicProduct {
	return PublicProduct{
		Name:             p.Name,
		Price:            p.Price,
		Currency:         p.Currency,
		Availability:     p.Availability,
		Condition:        p.Condition,
		Rating:           p.Rating,
		Source:           p.Source,
		DescriptionBlurb: sanitizeBlurb(p.Description),
	}
}

// ProductPublic is the same projection for the enriched record — the field
// P4 sanitizes further into CandidateState.
func (e EnrichedCandidate) ProductPublic() PublicProduct {
	return e.Product.ProductPublic()
}

// sanitizeBlurb removes control characters (incl. newlines → spaces) and
// truncates to PublicBlurbMax runes.
func sanitizeBlurb(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	if len(out) > PublicBlurbMax {
		out = out[:PublicBlurbMax]
	}
	return string(out)
}
