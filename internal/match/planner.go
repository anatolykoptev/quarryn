package match

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxCriterionLen caps one raw criterion in runes. Criteria are caller
// input (MCP): over-cap criteria are REJECTED, never truncated — a cut
// criterion silently changes what the caller asked for.
const maxCriterionLen = 200

// maxQuestions is the jeff System One request cap (≤64 questions per Ask,
// ADR-4). One packed Ask per candidate carries every subjective criterion.
const maxQuestions = 64

// Criterion validation errors — PlanCriteria rejects the whole call on the
// first offending criterion rather than silently dropping part of the ask.
var (
	ErrEmptyCriterion    = errors.New("match: empty criterion")
	ErrCriterionTooLong  = errors.New("match: criterion exceeds 200 runes")
	ErrTooManyCriteria   = errors.New("match: more than 64 subjective criteria (jeff question cap)")
	ErrInvalidConstraint = errors.New("match: malformed deterministic criterion")
	ErrUnknownConstraint = errors.New("match: unknown deterministic constraint value")
)

// noulInstructionTemplate is the FIXED template wrapping every subjective
// criterion (ADR-3). The criterion text never alters the question shape —
// it is data inside a constant instruction, which is what keeps a
// hostile criterion from rewriting the gate's question into a score or a
// different task. There is deliberately no overall-score question.
const noulInstructionTemplate = "Does this product satisfy: %s?"

// deterministicKeys is the prefix vocabulary routing a criterion to the
// deterministic prefilter instead of jeff (ADR-3): "key:value" where key
// is one of these. Everything else is subjective and becomes a noul.
//
//	price_max:N      price_min:N     currency:ISO
//	brand:X          not_brand:X     keyword:X        not_keyword:X
//	availability:ENUM
//
// ENUM is the canonical availability vocabulary emitted by extract:
// in_stock | out_of_stock | pre_order | backorder | limited | discontinued.
var deterministicKeys = map[string]struct{}{
	"price_max": {}, "price_min": {}, "currency": {},
	"brand": {}, "not_brand": {},
	"keyword": {}, "not_keyword": {},
	"availability": {},
}

// availabilities mirrors extract's canonical availability enum — the only
// values a deterministic availability criterion may carry. Kept as a local
// copy because extract's set is (correctly) package-private; the enum is
// small and stable.
var availabilities = map[string]struct{}{
	"in_stock": {}, "out_of_stock": {}, "pre_order": {},
	"backorder": {}, "limited": {}, "discontinued": {},
}

// Constraints is the deterministic half of a Plan, applied by the
// prefilter BEFORE any jeff call (ADR-3).
type Constraints struct {
	PriceMin        *float64 `json:"price_min,omitempty"`
	PriceMax        *float64 `json:"price_max,omitempty"`
	Currency        string   `json:"currency,omitempty"` // ISO 4217, upper-cased at parse
	BrandsInclude   []string `json:"brands_include,omitempty"`
	BrandsExclude   []string `json:"brands_exclude,omitempty"`
	KeywordsMust    []string `json:"keywords_must,omitempty"`
	KeywordsMustNot []string `json:"keywords_must_not,omitempty"`
	Availability    string   `json:"availability,omitempty"` // canonical enum
}

// empty reports whether no deterministic constraint was parsed.
func (c Constraints) empty() bool {
	return c.PriceMin == nil && c.PriceMax == nil && c.Currency == "" &&
		len(c.BrandsInclude) == 0 && len(c.BrandsExclude) == 0 &&
		len(c.KeywordsMust) == 0 && len(c.KeywordsMustNot) == 0 &&
		c.Availability == ""
}

// Question is one subjective criterion packed for jeff. ID ("c0", "c1", …)
// is the stable key in Request.Questions and in JudgedCandidate.Verdicts.
type Question struct {
	ID          string `json:"id"`
	Criterion   string `json:"criterion"`   // sanitized caller text
	Instruction string `json:"instruction"` // fixed template + criterion
}

// Plan is the ADR-3 criteria split: Constraints run deterministically in
// the prefilter; Questions become packed noul questions in one Ask per
// candidate.
type Plan struct {
	Constraints Constraints `json:"constraints"`
	Questions   []Question  `json:"questions,omitempty"`
}

// PlanCriteria splits raw caller criteria into the Plan. Each criterion is
// sanitized (control chars stripped, length-capped) before classification;
// empty or over-cap input and malformed deterministic values fail the whole
// call — a partial plan would silently drop part of the user's ask.
func PlanCriteria(raw []string) (Plan, error) {
	var plan Plan
	// Duplicate subjective criteria would ask jeff the identical question
	// twice — dedup on the sanitized text before they eat the budget.
	seen := make(map[string]struct{}, len(raw))
	for i, r := range raw {
		c, err := sanitizeCriterion(r)
		if err != nil {
			return Plan{}, fmt.Errorf("criterion %d: %w", i, err)
		}
		if key, val, ok := splitDeterministic(c); ok {
			if err := plan.Constraints.add(key, val); err != nil {
				return Plan{}, fmt.Errorf("criterion %d: %w", i, err)
			}
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		if len(plan.Questions) >= maxQuestions {
			return Plan{}, fmt.Errorf("criterion %d: %w", i, ErrTooManyCriteria)
		}
		plan.Questions = append(plan.Questions, Question{
			ID:          "c" + strconv.Itoa(len(plan.Questions)),
			Criterion:   c,
			Instruction: fmt.Sprintf(noulInstructionTemplate, c),
		})
	}
	return plan, nil
}

// sanitizeCriterion strips ASCII control characters (the caller-side
// counterpart of clipText — criterion text lands inside jeff question
// instructions) and enforces the rune cap.
func sanitizeCriterion(s string) (string, error) {
	// Control-strip without truncation first so the cap applies to what
	// would actually be sent.
	clean := clipText(s, utf8.RuneCountInString(s))
	clean = strings.TrimSpace(clean)
	switch {
	case clean == "":
		return "", ErrEmptyCriterion
	case utf8.RuneCountInString(clean) > maxCriterionLen:
		return "", ErrCriterionTooLong
	}
	return clean, nil
}

// splitDeterministic parses "key:value". It returns ok=false both for
// subjective criteria (no recognized key) and for strings that merely
// contain a colon inside prose — the key must match the vocabulary
// exactly.
func splitDeterministic(c string) (key, val string, ok bool) {
	k, v, found := strings.Cut(c, ":")
	if !found {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(k))
	if _, known := deterministicKeys[key]; !known {
		return "", "", false
	}
	val = strings.TrimSpace(v)
	return key, val, true
}

// add folds one deterministic criterion value into the constraint set.
// Malformed values (non-numeric price, non-ISO currency, unknown
// availability, empty brand/keyword) reject the criterion loudly.
func (c *Constraints) add(key, val string) error {
	switch key {
	case "price_max", "price_min":
		return c.addPrice(key, val)
	case "currency":
		return c.addCurrency(val)
	case "brand", "not_brand", "keyword", "not_keyword":
		return c.addTerm(key, val)
	case "availability":
		return c.addAvailability(val)
	}
	return nil
}

func (c *Constraints) addPrice(key, val string) error {
	f, err := strconv.ParseFloat(val, 64)
	// ParseFloat accepts "NaN"/"Inf" without error — a non-finite bound
	// would silently no-op the constraint instead of rejecting loudly.
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%w: %s:%q is not a finite non-negative number", ErrInvalidConstraint, key, val)
	}
	if key == "price_max" {
		c.PriceMax = &f
	} else {
		c.PriceMin = &f
	}
	return nil
}

func (c *Constraints) addCurrency(val string) error {
	if len(val) != 3 || !allAlpha(val) {
		return fmt.Errorf("%w: currency %q is not a 3-letter ISO code", ErrInvalidConstraint, val)
	}
	c.Currency = strings.ToUpper(val)
	return nil
}

func (c *Constraints) addTerm(key, val string) error {
	if val == "" {
		return fmt.Errorf("%w: %s needs a non-empty value", ErrInvalidConstraint, key)
	}
	lv := strings.ToLower(val)
	switch key {
	case "brand":
		c.BrandsInclude = append(c.BrandsInclude, lv)
	case "not_brand":
		c.BrandsExclude = append(c.BrandsExclude, lv)
	case "keyword":
		c.KeywordsMust = append(c.KeywordsMust, lv)
	default:
		c.KeywordsMustNot = append(c.KeywordsMustNot, lv)
	}
	return nil
}

func (c *Constraints) addAvailability(val string) error {
	lv := strings.ToLower(val)
	if _, ok := availabilities[lv]; !ok {
		return fmt.Errorf("%w: availability %q", ErrUnknownConstraint, val)
	}
	c.Availability = lv
	return nil
}

func allAlpha(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return len(s) > 0
}
