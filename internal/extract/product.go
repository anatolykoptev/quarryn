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
	"strconv"
	"strings"

	"github.com/anatolykoptev/quarryn/internal/money"
	"github.com/anatolykoptev/quarryn/internal/sources"
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
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	PriceMinor   *int64   `json:"price_minor,omitempty"` // minor units in Currency
	Currency     string   `json:"currency,omitempty"`    // ISO 4217
	Availability string   `json:"availability,omitempty"`
	Condition    string   `json:"condition,omitempty"`
	SellerName   string   `json:"seller_name,omitempty"`
	Rating       *float64 `json:"rating,omitempty"` // schema.org 0-5 convention
	ImageURL     string   `json:"image_url,omitempty"`
	Description  string   `json:"description,omitempty"`
	Source       string   `json:"source"` // product-page domain, www stripped
	// OfferID is the stable offer identity carried from the candidate
	// (adapter-scoped listing id or url-hash fallback). Not merged from
	// detail extraction — identity is fixed at the SERP boundary.
	OfferID string `json:"offer_id,omitempty"`
	Method  string `json:"method"`
	// BuyURL is the resolved merchant URL captured when the interact tier
	// followed a deal aggregator's outbound tracker (slickdeals /click).
	// Distinct from URL (the listing/thread address) — this is where the
	// item is actually purchased.
	BuyURL string `json:"buy_url,omitempty"`
	// Variants is the purchasable-configuration matrix when the listing is
	// an optioned/configurator product (issue #115): adapter-emitted wire
	// variants, schema.org hasVariant entries, or a Shopify .js rescue
	// fetch. Empty on single-variant listings.
	Variants []sources.Variant `json:"variants,omitempty"`
	Raw      json.RawMessage   `json:"raw,omitempty"`

	// jsTried marks a product whose /products/<handle>.js mirror was
	// already fetched this enrichment — the two rescue call sites share
	// the flag so a variant-less .js answer never triggers a retry
	// (unexported: internal state, never serialized or cached).
	jsTried bool
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
	Price            *float64 `json:"price,omitempty"`       // decimal, display/compat
	PriceMinor       *int64   `json:"price_minor,omitempty"` // minor units in Currency
	Currency         string   `json:"currency,omitempty"`
	Availability     string   `json:"availability,omitempty"`
	Condition        string   `json:"condition,omitempty"`
	Rating           *float64 `json:"rating,omitempty"`
	Source           string   `json:"source"`
	OfferID          string   `json:"offer_id,omitempty"`
	DescriptionBlurb string   `json:"description_blurb,omitempty"`
	// Variants exposes the purchasable configs (issue #115) — the option
	// label, decimal price and availability, no seller-identifying text.
	// Capped at PublicVariantMax, in-stock first.
	Variants []PublicVariant `json:"variants,omitempty"`
}

// PublicVariantMax bounds the egress variant list — configurator pages
// can carry 100+ SKUs; past that the tail is padding, not information.
const PublicVariantMax = 40

// PublicVariant is the egress-safe variant view. Price stays decimal —
// the listing's currency lives on the parent product.
type PublicVariant struct {
	Title     string   `json:"title"`
	VariantID string   `json:"variant_id,omitempty"`
	Price     *float64 `json:"price,omitempty"`
	Available *bool    `json:"available,omitempty"`
	URL       string   `json:"url,omitempty"`
}

// ProductPublic returns the egress-safe projection of p: the allow-listed
// fields only, Description truncated to PublicBlurbMax runes with control
// characters stripped.
func (p Product) ProductPublic() PublicProduct {
	return PublicProduct{
		Name:             p.Name,
		Price:            money.DecimalPtr(p.PriceMinor, p.Currency),
		PriceMinor:       p.PriceMinor,
		Currency:         p.Currency,
		Availability:     p.Availability,
		Condition:        p.Condition,
		Rating:           p.Rating,
		Source:           p.Source,
		OfferID:          p.OfferID,
		DescriptionBlurb: sanitizeBlurb(p.Description),
		Variants:         publicVariants(p.Variants),
	}
}

// publicVariants projects the wire variants into the egress shape:
// in-stock first, clipped titles, capped at PublicVariantMax.
func publicVariants(vs []sources.Variant) []PublicVariant {
	if len(vs) == 0 {
		return nil
	}
	in, out := splitVariantsAvailable(vs)
	ordered := append(in, out...)
	if len(ordered) > PublicVariantMax {
		ordered = ordered[:PublicVariantMax]
	}
	out2 := make([]PublicVariant, 0, len(ordered))
	for _, v := range ordered {
		out2 = append(out2, PublicVariant{
			Title:     sanitizeBlurb(v.Title),
			VariantID: v.VariantID,
			Price:     variantPrice(v),
			Available: v.Available,
			URL:       v.URL,
		})
	}
	return out2
}

func splitVariantsAvailable(vs []sources.Variant) (in, out []sources.Variant) {
	for _, v := range vs {
		if v.Available != nil && !*v.Available {
			out = append(out, v)
			continue
		}
		in = append(in, v)
	}
	return in, out
}

// variantPrice parses the wire decimal price to a float for egress; a
// missing/unparseable price stays nil — never clamped.
func variantPrice(v sources.Variant) *float64 {
	if v.Price == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v.Price), 64)
	if err != nil {
		return nil
	}
	return &f
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
