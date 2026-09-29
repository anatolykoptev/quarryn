package group

import (
	"strings"

	"github.com/anatolykoptev/quarryn/internal/extract"
)

// embedTextMax bounds the canonical text one product contributes — an
// embedded blob is a retrieval key, not a document dump.
const embedTextMax = 256

// gateRowMax caps how many name+variant evidence rows the spec gate
// evaluates per product: a configurator matrix can carry 100+ titles and
// the existential check only needs the head of the list.
const gateRowMax = 8

// embedText builds the canonical document embedded for a product: the
// listing name, clipped. Variant titles deliberately stay out — two
// stores disclose different option subsets, so variant text would push
// same-product vectors apart; configuration enters through the spec gate,
// not the embedding. Description stays out for the same reason (seller
// prose dilutes the model identity the vector must anchor).
func embedText(p extract.Product) string {
	return clipRunes(p.Name)
}

func clipRunes(s string) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > embedTextMax {
		return string(r[:embedTextMax])
	}
	return string(r)
}

// embeddable reports whether the text carries enough signal to embed at
// all — one short token ("SALE", "NEW") retrieves noise.
func embeddable(text string) bool {
	return len(alnumTokens(text)) >= 2
}

// evidenceRows is the spec-gate input for a product. A product with
// variants contributes only name+variant rows: the disclosed
// configurations ARE its identity evidence, and keeping the spec-free
// bare name would let the existential row-pairing bypass every variant
// gate. A variant-less product's bare name is its only evidence.
func evidenceRows(p extract.Product) []string {
	var rows []string
	for _, v := range p.Variants {
		if len(rows) >= gateRowMax {
			break
		}
		if t := strings.TrimSpace(v.Title); t != "" {
			rows = append(rows, clipRunes(p.Name+" "+t))
		}
	}
	if len(rows) == 0 {
		rows = append(rows, clipRunes(p.Name))
	}
	return rows
}
