package match

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/money"
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
	ExclConditionMismatch    ReasonCode = "condition_mismatch"
	ExclExtractionFailed     ReasonCode = "extraction_failed"
	ExclDeferredRender       ReasonCode = "deferred_render"
	ExclSpecMismatch         ReasonCode = "spec_mismatch"
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
	if r, d := checkAvailability(p, c); r != "" {
		return r, d
	}
	if r, d := checkCondition(p, c); r != "" {
		return r, d
	}
	return checkSpec(p, c)
}

// checkCondition requires an exact canonical-enum match, same rule as
// availability: a stated condition constraint is not waived by missing
// product data — an unknown condition fails it.
func checkCondition(p extract.Product, c Constraints) (ReasonCode, string) {
	if c.Condition != "" && p.Condition != c.Condition {
		return ExclConditionMismatch, fmt.Sprintf("condition %q != required %q", p.Condition, c.Condition)
	}
	return "", ""
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

// checkSpec applies the parsed size/chip requirements (issue #111) per
// purchasable row: when a variant matrix exists each row is name+one
// variant title — pooling every variant into one bag would let a
// 64GB/512GB variant and a 24GB/1TB variant jointly fake a "64GB AND
// 1TB" config. Without variants the single row is name+description.
// Any row satisfying every requirement passes; every row contradicting
// at least one excludes spec_mismatch; anything else is inconclusive —
// thin listings keep their jeff question.
func checkSpec(p extract.Product, c Constraints) (ReasonCode, string) {
	if len(c.SpecSizes) == 0 && len(c.SpecChips) == 0 {
		return "", ""
	}
	var rows []string
	if len(p.Variants) > 0 {
		for _, v := range p.Variants {
			rows = append(rows, p.Name+" "+v.Title)
		}
	} else {
		rows = []string{p.Name + " " + p.Description}
	}
	contradicts, inconclusive := 0, 0
	for _, row := range rows {
		switch rowVerdict(row, c) {
		case rowSatisfies:
			return "", ""
		case rowContradicts:
			contradicts++
		default:
			inconclusive++
		}
	}
	if contradicts > 0 && inconclusive == 0 {
		return ExclSpecMismatch, fmt.Sprintf(
			"no purchasable config satisfies sizes %v / chips %v",
			c.SpecSizes, c.SpecChips)
	}
	return "", ""
}

const (
	rowInconclusive = iota
	rowSatisfies
	rowContradicts
)

// rowVerdict scores one purchasable config row: satisfies when every
// requirement is met, contradicts when at least one is answerable yet
// unmet, inconclusive when a requirement has nothing to speak to.
func rowVerdict(text string, c Constraints) int {
	ms := rowSizes(text)
	verdict := rowSatisfies
	for _, req := range c.SpecSizes {
		if slices.ContainsFunc(ms, req.satisfiedBy) {
			continue
		}
		if req.answerableBy(ms) {
			return rowContradicts
		}
		verdict = rowInconclusive
	}
	chips := rowChips(text)
	for _, req := range c.SpecChips {
		ok := false
		for _, ch := range chips {
			if ch.gen == req.Gen && ch.tier >= req.MinTier {
				ok = true
				break
			}
		}
		if ok {
			continue
		}
		if len(chips) > 0 {
			return rowContradicts
		}
		verdict = rowInconclusive
	}
	return verdict
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
