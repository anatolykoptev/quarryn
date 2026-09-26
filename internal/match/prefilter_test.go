package match

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go-product-search/internal/extract"
)

func f64(v float64) *float64 { return &v }

func iminor(v int64) *int64 { return &v }

func baseProduct() extract.Product {
	return extract.Product{
		Name:         "Sony WH-1000XM5 Wireless Headphones",
		PriceMinor:   iminor(29999),
		Currency:     "USD",
		Availability: "in_stock",
		Condition:    "new",
		Description:  "Noise cancelling over-ear headphones",
	}
}

func TestPrefilterCurrencyMismatch(t *testing.T) {
	p := baseProduct()
	if got, _ := checkCandidate(p, Constraints{Currency: "EUR"}); got != ExclCurrencyMismatch {
		t.Fatalf("currency mismatch: got %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{Currency: "USD"}); got != "" {
		t.Fatalf("same currency rejected: %q", got)
	}
	// Lower-case criterion normalized at parse — checkCandidate folds too.
	if got, _ := checkCandidate(p, Constraints{Currency: "usd"}); got != "" {
		t.Fatalf("lower-case currency rejected: %q", got)
	}
}

func TestPrefilterPriceBounds(t *testing.T) {
	p := baseProduct()
	if got, _ := checkCandidate(p, Constraints{PriceMax: f64(200)}); got != ExclPriceAboveMax {
		t.Fatalf("above max: got %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{PriceMin: f64(400)}); got != ExclPriceBelowMin {
		t.Fatalf("below min: got %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{PriceMin: f64(200), PriceMax: f64(400)}); got != "" {
		t.Fatalf("in-range rejected: %q", got)
	}
	// Missing price cannot prove a bound — excluded.
	p.PriceMinor = nil
	if got, _ := checkCandidate(p, Constraints{PriceMax: f64(500)}); got != ExclMissingPrice {
		t.Fatalf("missing price: got %q", got)
	}
	// Without a bound, missing price is not a prefilter problem.
	if got, _ := checkCandidate(p, Constraints{Currency: "USD"}); got != "" {
		t.Fatalf("price-free constraint rejected: %q", got)
	}
}

func TestPrefilterKeywords(t *testing.T) {
	p := baseProduct()
	if got, _ := checkCandidate(p, Constraints{KeywordsMustNot: []string{"headphones"}}); got != ExclKeywordExcluded {
		t.Fatalf("must-not hit: got %q", got)
	}
	// Word boundary: "sony" must not match inside "Sonya" — and here the
	// name has a clean "Sony" token, so must-keyword passes.
	if got, _ := checkCandidate(p, Constraints{KeywordsMust: []string{"sony"}}); got != "" {
		t.Fatalf("must keyword missed: %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{KeywordsMust: []string{"bluetooth"}}); got != ExclKeywordMissing {
		t.Fatalf("must keyword absent: got %q", got)
	}
	// Word boundary: "son" is a prefix of "sony" but not a word.
	if got, _ := checkCandidate(p, Constraints{KeywordsMustNot: []string{"son"}}); got != "" {
		t.Fatalf("partial word matched: %q", got)
	}
}

func TestPrefilterBrands(t *testing.T) {
	p := baseProduct()
	if got, _ := checkCandidate(p, Constraints{BrandsInclude: []string{"sony"}}); got != "" {
		t.Fatalf("brand present: got %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{BrandsInclude: []string{"bose"}}); got != ExclBrandMissing {
		t.Fatalf("brand missing: got %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{BrandsExclude: []string{"sony"}}); got != ExclBrandExcluded {
		t.Fatalf("brand excluded hit: got %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{BrandsExclude: []string{"bose"}}); got != "" {
		t.Fatalf("brand excluded miss: %q", got)
	}
}

func TestPrefilterAvailability(t *testing.T) {
	p := baseProduct()
	if got, _ := checkCandidate(p, Constraints{Availability: "in_stock"}); got != "" {
		t.Fatalf("matching availability rejected: %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{Availability: "pre_order"}); got != ExclAvailabilityMismatch {
		t.Fatalf("mismatched availability: got %q", got)
	}
	p.Availability = ""
	if got, _ := checkCandidate(p, Constraints{Availability: "in_stock"}); got != ExclAvailabilityMismatch {
		t.Fatalf("empty availability: got %q", got)
	}
}

func TestPrefilterEmptyConstraintsPassAll(t *testing.T) {
	p := extract.Product{} // even an empty product passes with no constraints
	if got, _ := checkCandidate(p, Constraints{}); got != "" {
		t.Fatalf("empty constraints rejected: %q", got)
	}
}

// TestContainsWordUTF8Boundary — the left edge must decode the rune
// ENDING before the match. Decoding forward from j-1 lands on a
// continuation byte, reads RuneError, and fakes a boundary — which let
// "sony" match inside "lésony".
func TestContainsWordUTF8Boundary(t *testing.T) {
	// 'é' (U+00E9, 2 bytes) is a letter: a needle trailing it is not a
	// word.
	if containsWord("lésony audio", "sony") {
		t.Fatal("needle after a multibyte letter matched as a word")
	}
	// '—' (U+2014, 3 bytes) is not a letter: the needle is a real word.
	if !containsWord("casque—sony", "sony") {
		t.Fatal("needle after a multibyte non-letter did not match")
	}
}

// TestPrefilterExclusionDetail — the code/detail pair is the contract:
// detail names the offending value so consumers never re-derive it from
// the code (and a code alone can't say WHICH keyword was missing).
func TestPrefilterExclusionDetail(t *testing.T) {
	p := baseProduct()
	c := Constraints{KeywordsMust: []string{"waterproof"}, PriceMax: f64(1)}
	code, detail := checkCandidate(p, c)
	if code != ExclPriceAboveMax {
		t.Fatalf("price bound should run first: %q", code)
	}
	if !strings.Contains(detail, "max") {
		t.Fatalf("detail must name the bound: %q", detail)
	}
	code, detail = checkCandidate(p, Constraints{KeywordsMust: []string{"waterproof"}})
	if code != ExclKeywordMissing || !strings.Contains(detail, "waterproof") {
		t.Fatalf("keyword detail must carry the term: %q %q", code, detail)
	}
}

// TestPrefilterPriceBoundaryExact — a price equal to the bound must pass.
// With float prices, parsePrice("29.99") landed 1e-15 under the literal
// while the bound parsed to 29.99 — the boundary candidate was excluded.
// Minor units make the comparison exact.
func TestPrefilterPriceBoundaryExact(t *testing.T) {
	p := baseProduct()
	p.PriceMinor = iminor(2999) // exactly 9.99
	if got, _ := checkCandidate(p, Constraints{PriceMax: f64(29.99)}); got != "" {
		t.Fatalf("boundary price excluded: %q", got)
	}
	if got, _ := checkCandidate(p, Constraints{PriceMax: f64(29.98)}); got != ExclPriceAboveMax {
		t.Fatalf("one-cent-over must exclude: %q", got)
	}
}
