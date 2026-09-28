package match

import (
	"strconv"
	"unicode/utf8"

	"github.com/anatolykoptev/quarryn/internal/extract"
)

// Per-field caps on the jeff-visible state (ADR-11). The caps bound what
// scraped content can push into the decision model's context; truncation
// (not rejection) is right here because extract already validated the
// values — the cap is a context-size bound, not a correctness gate.
const (
	maxStateNameLen   = 300 // runes
	maxStateBlurbLen  = 500 // runes — mirrors extract.PublicBlurbMax
	maxStateTokenLen  = 64  // runes; currency/availability/condition tokens
	maxStateDomainLen = 253 // runes; DNS name cap
	maxStateVariants  = 40  // mirrors extract.PublicVariantMax
	maxStateVariantLn = 160 // runes; one "options | price | stock" line
)

// CandidateState is the ONLY shape the jeff adapter accepts as question
// state (ADR-11). It is a fixed-field struct built from the extract
// egress allowlist (PublicProduct): no map[string]any, no raw text blobs,
// and every string field is control-stripped and rune-capped at
// construction so scraped content can never forge a line like
// "\nprice: 0" inside the serialized state.
type CandidateState struct {
	Name         string   `json:"name"`
	Price        *float64 `json:"price,omitempty"`
	Currency     string   `json:"currency,omitempty"`
	Availability string   `json:"availability,omitempty"`
	Condition    string   `json:"condition,omitempty"`
	Rating       *float64 `json:"rating,omitempty"`
	SourceDomain string   `json:"source_domain,omitempty"`
	Blurb        string   `json:"blurb,omitempty"`
	// Variants renders the purchasable-config matrix as compact
	// "title | price | availability" lines (issue #115) — the judge checks
	// spec criteria (RAM/storage/chip) against real option labels instead
	// of guessing from the product title. Capped at maxStateVariants.
	Variants []string `json:"variants,omitempty"`
}

// NewCandidateState builds the jeff-bound state from the egress-safe
// product projection. The allowlist is enforced by construction — there
// is no parameter through which a caller could smuggle an extra field.
func NewCandidateState(p extract.PublicProduct) CandidateState {
	return CandidateState{
		Name:         clipText(p.Name, maxStateNameLen),
		Price:        p.Price,
		Currency:     clipText(p.Currency, maxStateTokenLen),
		Availability: clipText(p.Availability, maxStateTokenLen),
		Condition:    clipText(p.Condition, maxStateTokenLen),
		Rating:       p.Rating,
		SourceDomain: clipText(p.Source, maxStateDomainLen),
		Blurb:        clipText(p.DescriptionBlurb, maxStateBlurbLen),
		Variants:     variantLines(p),
	}
}

// variantLines renders each egress variant as one compact state line.
// Price carries the product's currency when known so "3529" is not
// ambiguous; absent availability is omitted, never fabricated.
func variantLines(p extract.PublicProduct) []string {
	if len(p.Variants) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(p.Variants), maxStateVariants))
	for _, v := range p.Variants {
		if len(out) == maxStateVariants {
			break
		}
		// The title is clipped before the suffixes are appended — clipping
		// the assembled line would silently eat " | price | stock" when a
		// merchant ships a 300-char option label.
		line := clipText(v.Title, maxStateVariantLn-40)
		if v.Price != nil {
			line += " | " + strconv.FormatFloat(*v.Price, 'f', -1, 64) + " " + p.Currency
		}
		if v.Available != nil {
			if *v.Available {
				line += " | in_stock"
			} else {
				line += " | out_of_stock"
			}
		}
		out = append(out, clipText(line, maxStateVariantLn))
	}
	return out
}

// clipText replaces ASCII control characters (including \r, \n and DEL)
// with spaces and truncates to max runes. Candidate text is untrusted
// scraped content — this is the single choke point where it is rendered
// incapable of line forgery before crossing the jeff boundary.
func clipText(s string, max int) string {
	if s == "" {
		return ""
	}
	out := make([]rune, 0, min(utf8.RuneCountInString(s), max))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			out = append(out, ' ')
		} else {
			out = append(out, r)
		}
		if len(out) == max {
			break
		}
	}
	return string(out)
}
