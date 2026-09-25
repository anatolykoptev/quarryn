package match

import (
	"testing"

	"github.com/anatolykoptev/go-product-search/internal/extract"
)

func f64(v float64) *float64 { return &v }

func baseProduct() extract.Product {
	return extract.Product{
		Name:         "Sony WH-1000XM5 Wireless Headphones",
		Price:        f64(299.99),
		Currency:     "USD",
		Availability: "in_stock",
		Condition:    "new",
		Description:  "Noise cancelling over-ear headphones",
	}
}

func TestPrefilterCurrencyMismatch(t *testing.T) {
	p := baseProduct()
	if got := checkCandidate(p, Constraints{Currency: "EUR"}); got != exclCurrencyMismatch {
		t.Fatalf("currency mismatch: got %q", got)
	}
	if got := checkCandidate(p, Constraints{Currency: "USD"}); got != "" {
		t.Fatalf("same currency rejected: %q", got)
	}
	// Lower-case criterion normalized at parse — checkCandidate folds too.
	if got := checkCandidate(p, Constraints{Currency: "usd"}); got != "" {
		t.Fatalf("lower-case currency rejected: %q", got)
	}
}

func TestPrefilterPriceBounds(t *testing.T) {
	p := baseProduct()
	if got := checkCandidate(p, Constraints{PriceMax: f64(200)}); got != exclPriceAboveMax {
		t.Fatalf("above max: got %q", got)
	}
	if got := checkCandidate(p, Constraints{PriceMin: f64(400)}); got != exclPriceBelowMin {
		t.Fatalf("below min: got %q", got)
	}
	if got := checkCandidate(p, Constraints{PriceMin: f64(200), PriceMax: f64(400)}); got != "" {
		t.Fatalf("in-range rejected: %q", got)
	}
	// Missing price cannot prove a bound — excluded.
	p.Price = nil
	if got := checkCandidate(p, Constraints{PriceMax: f64(500)}); got != exclMissingPrice {
		t.Fatalf("missing price: got %q", got)
	}
	// Without a bound, missing price is not a prefilter problem.
	if got := checkCandidate(p, Constraints{Currency: "USD"}); got != "" {
		t.Fatalf("price-free constraint rejected: %q", got)
	}
}

func TestPrefilterKeywords(t *testing.T) {
	p := baseProduct()
	if got := checkCandidate(p, Constraints{KeywordsMustNot: []string{"headphones"}}); got != exclKeywordExcluded {
		t.Fatalf("must-not hit: got %q", got)
	}
	// Word boundary: "sony" must not match inside "Sonya" — and here the
	// name has a clean "Sony" token, so must-keyword passes.
	if got := checkCandidate(p, Constraints{KeywordsMust: []string{"sony"}}); got != "" {
		t.Fatalf("must keyword missed: %q", got)
	}
	if got := checkCandidate(p, Constraints{KeywordsMust: []string{"bluetooth"}}); got != exclKeywordMissing {
		t.Fatalf("must keyword absent: got %q", got)
	}
	// Word boundary: "son" is a prefix of "sony" but not a word.
	if got := checkCandidate(p, Constraints{KeywordsMustNot: []string{"son"}}); got != "" {
		t.Fatalf("partial word matched: %q", got)
	}
}

func TestPrefilterBrands(t *testing.T) {
	p := baseProduct()
	if got := checkCandidate(p, Constraints{BrandsInclude: []string{"sony"}}); got != "" {
		t.Fatalf("brand present: got %q", got)
	}
	if got := checkCandidate(p, Constraints{BrandsInclude: []string{"bose"}}); got != exclBrandMissing {
		t.Fatalf("brand missing: got %q", got)
	}
	if got := checkCandidate(p, Constraints{BrandsExclude: []string{"sony"}}); got != exclBrandExcluded {
		t.Fatalf("brand excluded hit: got %q", got)
	}
	if got := checkCandidate(p, Constraints{BrandsExclude: []string{"bose"}}); got != "" {
		t.Fatalf("brand excluded miss: %q", got)
	}
}

func TestPrefilterAvailability(t *testing.T) {
	p := baseProduct()
	if got := checkCandidate(p, Constraints{Availability: "in_stock"}); got != "" {
		t.Fatalf("matching availability rejected: %q", got)
	}
	if got := checkCandidate(p, Constraints{Availability: "pre_order"}); got != exclAvailabilityMismatch {
		t.Fatalf("mismatched availability: got %q", got)
	}
	p.Availability = ""
	if got := checkCandidate(p, Constraints{Availability: "in_stock"}); got != exclAvailabilityMismatch {
		t.Fatalf("empty availability: got %q", got)
	}
}

func TestPrefilterEmptyConstraintsPassAll(t *testing.T) {
	p := extract.Product{} // even an empty product passes with no constraints
	if got := checkCandidate(p, Constraints{}); got != "" {
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
