package match

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlanCriteriaSplitsDeterministicAndSubjective(t *testing.T) {
	plan, err := PlanCriteria([]string{
		"price_max:500", "price_min:10", "currency:usd",
		"brand:sony", "not_brand:generic",
		"keyword:usb-c", "not_keyword:refurbished",
		"availability:in_stock",
		"good battery life",
	})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	assertSplitConstraints(t, plan.Constraints)
	if len(plan.Questions) != 1 {
		t.Fatalf("questions = %+v", plan.Questions)
	}
	q := plan.Questions[0]
	if q.ID != "c0" || q.Criterion != "good battery life" {
		t.Fatalf("question = %+v", q)
	}
	if q.Instruction != "Does this product satisfy: good battery life?" {
		t.Fatalf("instruction = %q", q.Instruction)
	}
}

// assertSplitConstraints checks every deterministic constraint field.
func assertSplitConstraints(t *testing.T, c Constraints) {
	t.Helper()
	if c.PriceMax == nil || *c.PriceMax != 500 {
		t.Fatalf("price_max = %v", c.PriceMax)
	}
	if c.PriceMin == nil || *c.PriceMin != 10 {
		t.Fatalf("price_min = %v", c.PriceMin)
	}
	if c.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", c.Currency)
	}
	if len(c.BrandsInclude) != 1 || c.BrandsInclude[0] != "sony" {
		t.Fatalf("brands_include = %v", c.BrandsInclude)
	}
	if len(c.BrandsExclude) != 1 || c.BrandsExclude[0] != "generic" {
		t.Fatalf("brands_exclude = %v", c.BrandsExclude)
	}
	if len(c.KeywordsMust) != 1 || c.KeywordsMust[0] != "usb-c" {
		t.Fatalf("keywords_must = %v", c.KeywordsMust)
	}
	if len(c.KeywordsMustNot) != 1 || c.KeywordsMustNot[0] != "refurbished" {
		t.Fatalf("keywords_must_not = %v", c.KeywordsMustNot)
	}
	if c.Availability != "in_stock" {
		t.Fatalf("availability = %q", c.Availability)
	}
}

func TestPlanCriteriaRejectsOverCap(t *testing.T) {
	_, err := PlanCriteria([]string{strings.Repeat("x", maxCriterionLen+1)})
	if !errors.Is(err, ErrCriterionTooLong) {
		t.Fatalf("err = %v, want ErrCriterionTooLong", err)
	}
	// Exactly at cap passes.
	if _, err := PlanCriteria([]string{strings.Repeat("x", maxCriterionLen)}); err != nil {
		t.Fatalf("at-cap criterion rejected: %v", err)
	}
}

func TestPlanCriteriaRejectsEmpty(t *testing.T) {
	for _, bad := range []string{"", "   ", "\n\t\r"} {
		if _, err := PlanCriteria([]string{bad}); !errors.Is(err, ErrEmptyCriterion) {
			t.Fatalf("criterion %q: err = %v, want ErrEmptyCriterion", bad, err)
		}
	}
}

func TestPlanCriteriaRejectsMalformedConstraint(t *testing.T) {
	for _, bad := range []string{
		"price_max:abc", "price_min:-5", "price_max:",
		"price_max:NaN", "price_min:Inf", "price_max:+Inf",
		"currency:US Dollar", "currency:12",
		"brand:", "keyword:", "not_keyword:",
	} {
		if _, err := PlanCriteria([]string{bad}); err == nil {
			t.Fatalf("criterion %q accepted, want rejection", bad)
		}
	}
	if _, err := PlanCriteria([]string{"availability:somewhere"}); !errors.Is(err, ErrUnknownConstraint) {
		t.Fatalf("err = %v, want ErrUnknownConstraint", err)
	}
}

func TestPlanCriteriaQuestionCap(t *testing.T) {
	raw := make([]string, maxQuestions+1)
	for i := range raw {
		raw[i] = strings.Repeat("x", 3) + " " + strings.Repeat("y", i%5+1) + " crit " + strings.Repeat("z", i)
	}
	// Ensure uniqueness isn't required — just count.
	if _, err := PlanCriteria(raw); !errors.Is(err, ErrTooManyCriteria) {
		t.Fatalf("err = %v, want ErrTooManyCriteria", err)
	}
}

func TestPlanCriteriaSanitizesSubjectiveText(t *testing.T) {
	plan, err := PlanCriteria([]string{" works\twith\npets\r "})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	if len(plan.Questions) != 1 {
		t.Fatalf("questions = %+v", plan.Questions)
	}
	got := plan.Questions[0].Criterion
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("control chars survived: %q", got)
	}
	if utf8.RuneCountInString(got) == 0 {
		t.Fatal("criterion emptied")
	}
}

// A colon inside free prose must NOT become a constraint — only a leading
// vocabulary key counts.
func TestPlanCriteriaColonInProseStaysSubjective(t *testing.T) {
	plan, err := PlanCriteria([]string{"review says: durable", "PRICE_MAX: 42"})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	if len(plan.Questions) != 1 || plan.Questions[0].Criterion != "review says: durable" {
		t.Fatalf("questions = %+v", plan.Questions)
	}
	if plan.Constraints.PriceMax == nil || *plan.Constraints.PriceMax != 42 {
		t.Fatalf("upper-case key should parse case-insensitively: %+v", plan.Constraints)
	}
}

// TestPlanCriteriaDedupesSubjective — identical sanitized criteria would
// ask jeff the same question twice; dedup before they eat the budget.
// Dedup is exact on sanitized text — case variants stay distinct.
func TestPlanCriteriaDedupesSubjective(t *testing.T) {
	plan, err := PlanCriteria([]string{
		"good battery", "usb-c", " good battery ", "GOOD BATTERY", "good battery",
	})
	if err != nil {
		t.Fatalf("PlanCriteria: %v", err)
	}
	if len(plan.Questions) != 3 {
		t.Fatalf("questions = %+v", plan.Questions)
	}
	for i, want := range []struct{ id, crit string }{
		{"c0", "good battery"}, {"c1", "usb-c"}, {"c2", "GOOD BATTERY"},
	} {
		if plan.Questions[i].ID != want.id || plan.Questions[i].Criterion != want.crit {
			t.Fatalf("question %d = %+v", i, plan.Questions[i])
		}
	}
}
