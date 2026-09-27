// Package api is the MCP tool surface (P5) of quarryn: it binds
// the retained search.Searcher to the product_search / product_match
// tools and projects every result through the extract.PublicProduct
// egress allowlist — no seller-identifying text, no Raw provenance blobs,
// no internal verdict internals beyond per-criterion prob+pass.
package api

import (
	"github.com/anatolykoptev/go-kit/score"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/rank"
	pssources "github.com/anatolykoptev/quarryn/internal/sources"
)

// productSearchInput is the product_search tool argument shape.
type productSearchInput struct {
	Query      string   `json:"query"                jsonschema:"Product search query (e.g. \"sony wh-1000xm5\")"`
	Criteria   []string `json:"criteria,omitempty"   jsonschema:"Match criteria: deterministic key:value constraints (price_max:500, brand:sony, not_keyword:refurbished, availability:in_stock, currency:usd) and/or free-text subjective requirements (\"good battery life\") judged per product"`
	MaxResults int      `json:"max_results,omitempty" jsonschema:"Cap on returned products (default 10, max 50)"`
}

// productMatchInput is the product_match tool argument shape: one
// caller-supplied product URL judged against the criteria through the same
// extraction + jeff path search candidates take.
type productMatchInput struct {
	ProductURL string   `json:"product_url"        jsonschema:"URL of the product page to fetch and judge"`
	Criteria   []string `json:"criteria,omitempty" jsonschema:"Same criterion vocabulary as product_search"`
}

// productResult is the public projection of one ranked candidate. The
// embedded PublicProduct pins the egress allowlist — name, price,
// currency, availability, condition, rating, source domain and a clipped
// description blurb. Anything outside that allowlist (seller names, raw
// extraction payloads, metadata) is unreachable here by construction.
type productResult struct {
	URL     string `json:"url"`     // listing URL — needed to visit/buy the product
	Adapter string `json:"adapter"` // adapter that sourced it (ebay/etsy/shopify/slickdeals)
	// BuyURL is the resolved merchant URL captured when the interact tier
	// followed the listing's outbound tracker (e.g. slickdeals /click →
	// woot.com offer). Empty unless the solve tier resolved one.
	BuyURL string `json:"buy_url,omitempty"`
	extract.PublicProduct

	Score      float64               `json:"score"`      // fused score, [0,1]
	Confidence score.ConfidenceLevel `json:"confidence"` // low|medium|high bucket
	Passed     bool                  `json:"passed"`     // deterministic prefilter + all verdicts ≥ threshold

	MatchedCriteria []rank.CriterionVerdict `json:"matched_criteria,omitempty"`
	DealSignals     *rank.DealSignals       `json:"deal_signals,omitempty"`
	Trust           string                  `json:"trust,omitempty"`
	ExcludedReason  string                  `json:"excluded_reason,omitempty"`
	ExcludedDetail  string                  `json:"excluded_detail,omitempty"`
	UnjudgedReason  string                  `json:"unjudged_reason,omitempty"`
}

// searchOutput is the product_search response. RequestID is the ADR-6
// calibration id — the uuid on every jeff_gate log event of this call and
// the key callers pass to product_feedback so the outcome joins offline.
type searchOutput struct {
	RequestID     string                   `json:"request_id"`
	Results       []productResult          `json:"results"`
	Sources       []pssources.SourceStatus `json:"sources,omitempty"`
	Degraded      bool                     `json:"degraded,omitempty"`
	DegradeReason string                   `json:"degrade_reason,omitempty"`
	Brief         *searchBrief             `json:"brief,omitempty"`
}

// matchOutput is the product_match response — a single judged product.
// RequestID serves the same ADR-6 calibration join as product_search.
type matchOutput struct {
	RequestID     string        `json:"request_id"`
	Result        productResult `json:"result"`
	Degraded      bool          `json:"degraded,omitempty"`
	DegradeReason string        `json:"degrade_reason,omitempty"`
}
