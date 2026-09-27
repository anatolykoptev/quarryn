package match

import (
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
	}
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
