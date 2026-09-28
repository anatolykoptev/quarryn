package sources

import (
	"encoding/json"
	"strings"
)

// Variant is one purchasable configuration of a product listing —
// "M5 Pro 18C / 64GB / 1TB" on a Shopify CTO page, an ebay variation
// pair, etc. Adapters emit the wire-level fields (decimal price string,
// same units as MetaPrice); nothing here is display-formatted — Title is
// the merchant's own option label.
type Variant struct {
	Title     string `json:"title"`
	VariantID string `json:"variant_id,omitempty"`
	Price     string `json:"price,omitempty"` // decimal string, listing currency
	Currency  string `json:"currency,omitempty"`
	Available *bool  `json:"available,omitempty"`
	URL       string `json:"url,omitempty"` // deep link with the variant preselected
}

// MetaVariants carries a JSON array of Variant in Result.Metadata. The
// candidate decode lifts it into Candidate.Variants so the judge and the
// watcher see the real configuration matrix instead of the collapsed
// min-price (issue #115).
const MetaVariants = "variants"

// maxVariantsWired caps the variant list riding Metadata/Candidate —
// configurators like a MacBook CTO page can carry 100+ SKUs; the judge
// context and egress only need the in-stock head of the list.
const maxVariantsWired = 40

// EncodeVariants compacts a variant list (in-stock first) to its Metadata
// JSON form. Returns "" when the list is empty — callers only set the key
// on a non-empty result.
func EncodeVariants(vs []Variant) string {
	if len(vs) == 0 {
		return ""
	}
	inStock, outStock := splitInStock(vs)
	ordered := append(inStock, outStock...)
	if len(ordered) > maxVariantsWired {
		ordered = ordered[:maxVariantsWired]
	}
	raw, err := json.Marshal(ordered)
	if err != nil {
		return ""
	}
	return string(raw)
}

// DecodeVariants parses the MetaVariants wire form. Returns nil on
// malformed input — the caller keeps the raw key in Candidate.Metadata so
// a bad upstream payload never vanishes silently.
func DecodeVariants(s string) []Variant {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var vs []Variant
	if err := json.Unmarshal([]byte(s), &vs); err != nil || len(vs) == 0 {
		return nil
	}
	return vs
}

// splitInStock partitions variants by availability, preserving order in
// each half. Available==nil (unknown) counts as in-stock — a configurator
// that omits the flag still sells the option.
func splitInStock(vs []Variant) (in, out []Variant) {
	for _, v := range vs {
		if v.Available != nil && !*v.Available {
			out = append(out, v)
			continue
		}
		in = append(in, v)
	}
	return in, out
}
