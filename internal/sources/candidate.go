package sources

import (
	"github.com/anatolykoptev/quarryn/internal/money"
	"strconv"

	"github.com/anatolykoptev/go-engine/sources"
)

// Candidate is the typed record the funnel hands downstream (P3 scoring,
// P5 tool output). Product fields that arrived as Result.Metadata strings
// are lifted into typed fields here — the single decode site, so adapters
// never format for humans and consumers never parse strings.
type Candidate struct {
	Source string `json:"source"`
	// OfferID is the stable offer identity (see OfferID func): resolvable
	// across calls, unlike a URL. Built at decode so every consumer sees it.
	OfferID      string   `json:"offer_id,omitempty"`
	Title        string   `json:"title"`
	URL          string   `json:"url"`
	Content      string   `json:"content,omitempty"`
	Score        float64  `json:"score"`                 // fused funnel score; higher = stronger cross-source consensus
	PriceMinor   *int64   `json:"price_minor,omitempty"` // minor units in Currency
	Currency     string   `json:"currency,omitempty"`
	Condition    string   `json:"condition,omitempty"`
	Availability string   `json:"availability,omitempty"`
	Seller       string   `json:"seller,omitempty"`
	DiscountPct  *float64 `json:"discount_pct,omitempty"`
	Thumbs       *int     `json:"thumbs,omitempty"`
	ImageURL     string   `json:"image_url,omitempty"`

	// Metadata carries adapter-specific keys with no Candidate field
	// (buying_options, merchant, listing_id, ...) plus lifted keys whose
	// values failed to decode — a malformed upstream price must not
	// silently vanish. Nil when empty.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// liftedKeys is the set of Metadata keys consumed into Candidate fields.
// Any other key passes through into Candidate.Metadata.
var liftedKeys = map[string]struct{}{
	MetaSource:       {},
	MetaPrice:        {},
	MetaCurrency:     {},
	MetaCondition:    {},
	MetaAvailability: {},
	MetaSeller:       {},
	MetaDiscountPct:  {},
	MetaThumbs:       {},
	MetaImageURL:     {},
}

// candidateFromResult decodes a funnel-merged Result into a Candidate.
func candidateFromResult(r sources.Result) Candidate {
	c := Candidate{
		Source:       r.Metadata[MetaSource],
		OfferID:      OfferID(r),
		Title:        r.Title,
		URL:          r.URL,
		Content:      r.Content,
		Score:        r.Score,
		Currency:     r.Metadata[MetaCurrency],
		Condition:    r.Metadata[MetaCondition],
		Availability: r.Metadata[MetaAvailability],
		Seller:       r.Metadata[MetaSeller],
		ImageURL:     r.Metadata[MetaImageURL],
	}
	// decodeFailed collects lifted keys whose value did not parse — they
	// still land in Metadata so nothing vanishes silently.
	decodeFailed := make(map[string]struct{})
	if s := r.Metadata[MetaPrice]; s != "" {
		if v, ok := money.ToMinor(s, c.Currency); ok {
			c.PriceMinor = &v
		} else {
			decodeFailed[MetaPrice] = struct{}{}
		}
	}
	if s := r.Metadata[MetaDiscountPct]; s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			c.DiscountPct = &v
		} else {
			decodeFailed[MetaDiscountPct] = struct{}{}
		}
	}
	if s := r.Metadata[MetaThumbs]; s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			c.Thumbs = &v
		} else {
			decodeFailed[MetaThumbs] = struct{}{}
		}
	}
	for k, v := range r.Metadata {
		if _, ok := liftedKeys[k]; ok {
			if _, failed := decodeFailed[k]; !failed {
				continue
			}
		}
		if c.Metadata == nil {
			c.Metadata = make(map[string]string)
		}
		c.Metadata[k] = v
	}
	return c
}
