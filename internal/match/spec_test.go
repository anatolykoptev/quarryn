package match

import (
	"testing"

	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

// The observed failure in #111: "48 or 64GB" passed a 24GB listing on
// vibes. Spec tokens must land in deterministic constraints, and a
// pure-spec criterion must not mint a jeff question.
func TestPlanCriteriaSpecTokens(t *testing.T) {
	plan, err := PlanCriteria([]string{"64GB", "1TB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Questions) != 0 {
		t.Fatalf("pure-spec criteria must not reach jeff: %+v", plan.Questions)
	}
	if len(plan.Constraints.SpecSizes) != 2 {
		t.Fatalf("spec sizes = %+v", plan.Constraints.SpecSizes)
	}
}

func TestPlanCriteriaSpecRangeAndMixed(t *testing.T) {
	plan, err := PlanCriteria([]string{"48–64GB, ships to US"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Constraints.SpecSizes) != 1 ||
		plan.Constraints.SpecSizes[0].MinGB != 48 || plan.Constraints.SpecSizes[0].MaxGB != 64 {
		t.Fatalf("spec sizes = %+v", plan.Constraints.SpecSizes)
	}
	// The prose tail still becomes a jeff question.
	if len(plan.Questions) != 1 {
		t.Fatalf("mixed criterion lost its question: %+v", plan.Questions)
	}
}

func TestPlanCriteriaChipTier(t *testing.T) {
	plan, err := PlanCriteria([]string{"M5 Pro chip"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Constraints.SpecChips) != 1 ||
		plan.Constraints.SpecChips[0] != (ChipReq{Gen: 5, MinTier: 1}) {
		t.Fatalf("chips = %+v", plan.Constraints.SpecChips)
	}
	if len(plan.Questions) != 1 {
		t.Fatal("prose tail 'chip' must still question jeff")
	}
}

func prod(text string, variants ...string) extract.Product {
	p := extract.Product{Name: text}
	for _, v := range variants {
		p.Variants = append(p.Variants, sources.Variant{Title: v})
	}
	return p
}

func TestCheckSpecSizeContradiction(t *testing.T) {
	// The live miss: listing states 24GB, criterion demands 48–64.
	c := Constraints{SpecSizes: []SizeReq{{MinGB: 48, MaxGB: 64}}}
	r, d := checkSpec(prod("MacBook Pro 14 M5 Pro 24GB / 512GB"), c)
	if r != ExclSpecMismatch {
		t.Fatalf("24GB vs 48–64GB must exclude, got %q %q", r, d)
	}
}

func TestCheckSpecSizeSatisfied(t *testing.T) {
	c := Constraints{SpecSizes: []SizeReq{{MinGB: 64, MaxGB: 64}, {MinGB: 1024, MaxGB: 1024}}}
	if r, d := checkSpec(prod("MacBook Pro 14 — 64GB unified memory, 1TB SSD"), c); r != "" {
		t.Fatalf("64GB+1TB must satisfy: %q %q", r, d)
	}
	// TB↔GB normalization: 1024GB criterion accepts a "1TB" listing.
	if r, d := checkSpec(prod("…64GB, 1TB"), Constraints{
		SpecSizes: []SizeReq{{MinGB: 1024, MaxGB: 1024}},
	}); r != "" {
		t.Fatalf("unit normalization failed: %q %q", r, d)
	}
}

func TestCheckSpecSizeInVariants(t *testing.T) {
	// Configurator: the page title carries no RAM, the matrix does.
	c := Constraints{SpecSizes: []SizeReq{{MinGB: 64, MaxGB: 64}}}
	p := prod("14-inch MacBook Pro (M5 Pro or Max)",
		"18C/20G / 64GB / 1TB", "15C/16G / 24GB / 512GB")
	if r, d := checkSpec(p, c); r != "" {
		t.Fatalf("variant matrix must satisfy 64GB: %q %q", r, d)
	}
}

func TestCheckSpecAlternatives(t *testing.T) {
	// "32GB or 64GB" is a discrete set — 48GB inside the span fails it.
	c := Constraints{SpecSizes: []SizeReq{{Points: []int64{32, 64}}}}
	if r, _ := checkSpec(prod("MacBook Pro 32GB / 1TB"), c); r != "" {
		t.Fatalf("32GB must satisfy 32|64, got %q", r)
	}
	if r, _ := checkSpec(prod("MacBook Pro 48GB / 1TB"), c); r != ExclSpecMismatch {
		t.Fatalf("48GB must fail 32|64, got %q", r)
	}
}

func TestCheckSpecNoDisclosureIsInconclusive(t *testing.T) {
	// Product states no sizes at all — nothing to contradict; jeff
	// (or a variant pin) decides.
	c := Constraints{SpecSizes: []SizeReq{{MinGB: 64, MaxGB: 64}}}
	if r, _ := checkSpec(prod("MacBook Pro 14 laptop"), c); r != "" {
		t.Fatalf("unstated spec must stay inconclusive, got %q", r)
	}
}

func TestCheckSpecChip(t *testing.T) {
	c := Constraints{SpecChips: []ChipReq{{Gen: 5, MinTier: 1}}}
	// The live miss: bare M5 passed "M5 Pro".
	if r, _ := checkSpec(prod("14-inch MacBook Pro (M5)"), c); r != ExclSpecMismatch {
		t.Fatalf("base M5 must fail M5 Pro, got %q", r)
	}
	if r, _ := checkSpec(prod("MacBook Pro M5 Pro"), c); r != "" {
		t.Fatalf("M5 Pro must satisfy, got %q", r)
	}
	// A higher tier of the same gen satisfies a lower ask.
	if r, _ := checkSpec(prod("Mac Studio M5 Max"), c); r != "" {
		t.Fatalf("M5 Max must satisfy M5 Pro ask, got %q", r)
	}
	// Older generation never does.
	if r, _ := checkSpec(prod("MacBook Pro M4 Max"), c); r != ExclSpecMismatch {
		t.Fatalf("M4 Max must fail a gen-5 ask, got %q", r)
	}
}

func TestCheckSpecDisabledWithoutReqs(t *testing.T) {
	if r, _ := checkSpec(prod("anything"), Constraints{}); r != "" {
		t.Fatalf("empty spec constraints must be a no-op, got %q", r)
	}
}

// checkSpec must be wired into the checkCandidate chain, not just exist.
func TestCheckCandidateWiresSpec(t *testing.T) {
	plan, err := PlanCriteria([]string{"48–64GB"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := checkCandidate(prod("MacBook Pro 14 M5 24GB / 512GB"), plan.Constraints)
	if r != ExclSpecMismatch {
		t.Fatalf("checkCandidate must apply spec constraints, got %q", r)
	}
	if r, _ := checkCandidate(prod("MacBook Pro 14 M5 Pro 48GB / 1TB"), plan.Constraints); r != "" {
		t.Fatalf("48GB must pass a 48-64GB ask, got %q", r)
	}
}
