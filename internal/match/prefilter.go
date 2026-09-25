package match

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anatolykoptev/go-product-search/internal/extract"
)

// Exclusion reason vocabulary — a bounded set safe for metric labels. The
// human-facing ExcludeReason may carry more detail; the metric label is
// always one of these.
const (
	exclMissingPrice         = "missing_price"
	exclPriceAboveMax        = "price_above_max"
	exclPriceBelowMin        = "price_below_min"
	exclCurrencyMismatch     = "currency_mismatch"
	exclBrandMissing         = "brand_missing"
	exclBrandExcluded        = "brand_excluded"
	exclKeywordMissing       = "keyword_missing"
	exclKeywordExcluded      = "keyword_excluded"
	exclAvailabilityMismatch = "availability_mismatch"
	exclExtractionFailed     = "extraction_failed"
	exclDeferredRender       = "deferred_render"
)

// checkCandidate runs the deterministic constraints against one enriched
// candidate's product (ADR-3): every constraint must hold. It returns ""
// on pass, otherwise the bounded exclusion reason.
func checkCandidate(p extract.Product, c Constraints) string {
	if c.empty() {
		return ""
	}
	if r := checkPrice(p, c); r != "" {
		return r
	}
	if r := checkCurrency(p, c); r != "" {
		return r
	}
	if r := checkTerms(p, c); r != "" {
		return r
	}
	return checkAvailability(p, c)
}

// checkPrice applies the bounds. A product without a price cannot prove it
// satisfies a bound — excluded, not waved through.
func checkPrice(p extract.Product, c Constraints) string {
	if c.PriceMin == nil && c.PriceMax == nil {
		return ""
	}
	if p.Price == nil {
		return exclMissingPrice
	}
	if c.PriceMax != nil && *p.Price > *c.PriceMax {
		return exclPriceAboveMax
	}
	if c.PriceMin != nil && *p.Price < *c.PriceMin {
		return exclPriceBelowMin
	}
	return ""
}

// checkCurrency compares against the extract-normalized ISO code; extract
// already canonicalized, so a residual mismatch is a real mismatch.
func checkCurrency(p extract.Product, c Constraints) string {
	if c.Currency != "" && !strings.EqualFold(p.Currency, c.Currency) {
		return exclCurrencyMismatch
	}
	return ""
}

// checkTerms applies brand and keyword must/must-not lists as
// case-insensitive word-boundary matches against the product's searchable
// text (name + description).
func checkTerms(p extract.Product, c Constraints) string {
	text := strings.ToLower(p.Name + " " + p.Description)
	// want=true: term must appear; want=false: term must not appear.
	for _, rule := range []struct {
		terms  []string
		want   bool
		reason string
	}{
		{c.BrandsInclude, true, exclBrandMissing},
		{c.BrandsExclude, false, exclBrandExcluded},
		{c.KeywordsMust, true, exclKeywordMissing},
		{c.KeywordsMustNot, false, exclKeywordExcluded},
	} {
		for _, term := range rule.terms {
			if containsWord(text, term) != rule.want {
				return rule.reason
			}
		}
	}
	return ""
}

// checkAvailability requires an exact canonical-enum match; a product
// with unknown availability fails a stated availability constraint.
func checkAvailability(p extract.Product, c Constraints) string {
	if c.Availability != "" && p.Availability != c.Availability {
		return exclAvailabilityMismatch
	}
	return ""
}

// containsWord reports whether haystack (already lower-cased) contains
// needle as a word-bounded term — "sony" matches "Sony headphones" but not
// "Sonya". Boundaries are non-letter/non-digit positions or string edges;
// needle must already be lower-cased.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); {
		j := strings.Index(haystack[i:], needle)
		if j < 0 {
			return false
		}
		j += i
		if isBoundary(haystack, j-1) && isBoundary(haystack, j+len(needle)) {
			return true
		}
		i = j + 1
	}
	return false
}

// isBoundary reports whether position i in s is outside the string or on a
// non-letter/non-digit rune (a word edge).
func isBoundary(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	r := rune(s[i])
	if s[i] >= 0x80 {
		r, _ = utf8.DecodeRuneInString(s[i:])
	}
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
