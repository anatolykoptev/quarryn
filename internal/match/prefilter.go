package match

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anatolykoptev/go-product-search/internal/extract"
	"github.com/anatolykoptev/go-product-search/internal/money"
)

// ReasonCode is the stable machine-readable vocabulary carried on
// JudgedCandidate reasons — consumers key on the code, never on the prose
// detail (the northcinder {code, detail} split).
type ReasonCode string

// Exclusion reason vocabulary — a bounded set safe for metric labels. The
// human-facing ExcludeDetail may carry more detail; the metric label and
// the JSON field are always one of these.
const (
	ExclMissingPrice         ReasonCode = "missing_price"
	ExclPriceAboveMax        ReasonCode = "price_above_max"
	ExclPriceBelowMin        ReasonCode = "price_below_min"
	ExclCurrencyMismatch     ReasonCode = "currency_mismatch"
	ExclBrandMissing         ReasonCode = "brand_missing"
	ExclBrandExcluded        ReasonCode = "brand_excluded"
	ExclKeywordMissing       ReasonCode = "keyword_missing"
	ExclKeywordExcluded      ReasonCode = "keyword_excluded"
	ExclAvailabilityMismatch ReasonCode = "availability_mismatch"
	ExclExtractionFailed     ReasonCode = "extraction_failed"
	ExclDeferredRender       ReasonCode = "deferred_render"
)

// checkCandidate runs the deterministic constraints against one enriched
// candidate's product (ADR-3): every constraint must hold. It returns the
// zero code on pass, otherwise the bounded exclusion reason plus a detail
// naming the offending value.
func checkCandidate(p extract.Product, c Constraints) (ReasonCode, string) {
	if c.empty() {
		return "", ""
	}
	if r, d := checkPrice(p, c); r != "" {
		return r, d
	}
	if r, d := checkCurrency(p, c); r != "" {
		return r, d
	}
	if r, d := checkTerms(p, c); r != "" {
		return r, d
	}
	return checkAvailability(p, c)
}

// checkPrice applies the bounds. A product without a price cannot prove it
// satisfies a bound — excluded, not waved through.
func checkPrice(p extract.Product, c Constraints) (ReasonCode, string) {
	if c.PriceMin == nil && c.PriceMax == nil {
		return "", ""
	}
	if p.PriceMinor == nil {
		return ExclMissingPrice, "product carries no price"
	}
	if c.PriceMax != nil {
		if bound, ok := money.FromFloat(*c.PriceMax, p.Currency); ok && *p.PriceMinor > bound {
			return ExclPriceAboveMax, fmt.Sprintf("price %s > max %s",
				money.Format(*p.PriceMinor, p.Currency),
				money.Format(bound, p.Currency))
		}
	}
	if c.PriceMin != nil {
		if bound, ok := money.FromFloat(*c.PriceMin, p.Currency); ok && *p.PriceMinor < bound {
			return ExclPriceBelowMin, fmt.Sprintf("price %s < min %s",
				money.Format(*p.PriceMinor, p.Currency),
				money.Format(bound, p.Currency))
		}
	}
	return "", ""
}

// checkCurrency compares against the extract-normalized ISO code; extract
// already canonicalized, so a residual mismatch is a real mismatch.
func checkCurrency(p extract.Product, c Constraints) (ReasonCode, string) {
	if c.Currency != "" && !strings.EqualFold(p.Currency, c.Currency) {
		return ExclCurrencyMismatch, fmt.Sprintf("currency %q != required %q", p.Currency, c.Currency)
	}
	return "", ""
}

// checkTerms applies brand and keyword must/must-not lists as
// case-insensitive word-boundary matches against the product's searchable
// text (name + description).
func checkTerms(p extract.Product, c Constraints) (ReasonCode, string) {
	text := strings.ToLower(p.Name + " " + p.Description)
	// want=true: term must appear; want=false: term must not appear.
	for _, rule := range []struct {
		terms  []string
		want   bool
		kind   string
		reason ReasonCode
	}{
		{c.BrandsInclude, true, "brand", ExclBrandMissing},
		{c.BrandsExclude, false, "brand", ExclBrandExcluded},
		{c.KeywordsMust, true, "keyword", ExclKeywordMissing},
		{c.KeywordsMustNot, false, "keyword", ExclKeywordExcluded},
	} {
		for _, term := range rule.terms {
			if containsWord(text, term) != rule.want {
				if rule.want {
					return rule.reason, fmt.Sprintf("required %s %q absent", rule.kind, term)
				}
				return rule.reason, fmt.Sprintf("excluded %s %q present", rule.kind, term)
			}
		}
	}
	return "", ""
}

// checkAvailability requires an exact canonical-enum match; a product
// with unknown availability fails a stated availability constraint.
func checkAvailability(p extract.Product, c Constraints) (ReasonCode, string) {
	if c.Availability != "" && p.Availability != c.Availability {
		return ExclAvailabilityMismatch, fmt.Sprintf("availability %q != required %q", p.Availability, c.Availability)
	}
	return "", ""
}

// containsWord reports whether haystack (already lower-cased) contains
// needle as a word-bounded term — "sony" matches "Sony headphones" but not
// "Sonya" or "lésony". Boundaries are non-letter/non-digit positions or
// string edges; needle must already be lower-cased.
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
		if leftBoundary(haystack, j) && isBoundary(haystack, j+len(needle)) {
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

// leftBoundary reports whether a match starting at byte j sits on a word
// edge: start of string, or the rune ENDING just before j is non-letter/
// non-digit. Decoding forward from j-1 can land on a continuation byte
// and read RuneError — a false boundary that let "sony" match inside
// "lésony".
func leftBoundary(s string, j int) bool {
	if j <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:j])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
