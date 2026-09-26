package extract

import (
	"math"
	"strings"
	"testing"
)

func validProduct() *Product {
	return &Product{
		Name:       "Thing",
		URL:        "https://shop.example.com/p/1",
		PriceMinor: iminor(999),
		Currency:   "USD",
	}
}

func TestProblemsValidProductPasses(t *testing.T) {
	if probs := validProduct().problems(); len(probs) > 0 {
		t.Fatalf("valid product rejected: %v", probs)
	}
}

func TestProblemsRejectsBadPrices(t *testing.T) {
	for _, v := range []int64{0, -1, -500, math.MaxInt64} {
		p := validProduct()
		p.PriceMinor = &v
		if probs := p.problems(); len(probs) == 0 {
			t.Errorf("price %v accepted", v)
		}
	}
	p := validProduct()
	p.PriceMinor = nil
	if probs := p.problems(); len(probs) == 0 {
		t.Error("missing price accepted")
	}
}

func TestProblemsRejectsBadCurrency(t *testing.T) {
	for _, c := range []string{"XXX", "BTC", "US", "us d", "USD2", "€"} {
		p := validProduct()
		p.Currency = c
		if probs := p.problems(); len(probs) == 0 {
			t.Errorf("currency %q accepted", c)
		}
	}
	p := validProduct()
	p.Currency = "" // price present but currency missing → incomplete
	if probs := p.problems(); len(probs) == 0 {
		t.Error("price without currency accepted")
	}
}

func TestProblemsRejectsOversizedNameAndEmptyName(t *testing.T) {
	p := validProduct()
	p.Name = strings.Repeat("a", maxNameLen+1)
	if probs := p.problems(); len(probs) == 0 {
		t.Error("501-rune name accepted")
	}
	p = validProduct()
	p.Name = "   "
	if probs := p.problems(); len(probs) == 0 {
		t.Error("blank name accepted")
	}
}

func TestProblemsRejectsEnumViolations(t *testing.T) {
	p := validProduct()
	p.Condition = "minty fresh and unused" // not in enum → reject
	if probs := p.problems(); len(probs) == 0 {
		t.Error("non-enum condition accepted")
	}
	p = validProduct()
	p.Availability = "ships_someday"
	if probs := p.problems(); len(probs) == 0 {
		t.Error("non-enum availability accepted")
	}
	p = validProduct()
	p.Rating = f64(9)
	if probs := p.problems(); len(probs) == 0 {
		t.Error("rating 9 accepted (>5)")
	}
}

func TestProblemsRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "javascript:x", "notaurl", "ftp://h/p"} {
		p := validProduct()
		p.URL = u
		if probs := p.problems(); len(probs) == 0 {
			t.Errorf("url %q accepted", u)
		}
	}
}

func TestNormalizeCondition(t *testing.T) {
	cases := map[string]string{
		"NEW":                                     "new",
		"https://schema.org/NewCondition":         "new",
		"https://schema.org/UsedCondition":        "used",
		"https://schema.org/RefurbishedCondition": "refurbished",
		"For parts or not working":                "for_parts",
		"New other (see details)":                 "new",
		"Manufacturer refurbished":                "refurbished",
		"Open box":                                "like_new",
		"Pre-owned":                               "used",
		"USED_EXCELLENT":                          "used",
		"alien taxonomy":                          "",
		"":                                        "",
	}
	for in, want := range cases {
		if got := normalizeCondition(in); got != want {
			t.Errorf("normalizeCondition(%q) = %q want %q", in, got, want)
		}
	}
}

func TestNormalizeAvailability(t *testing.T) {
	cases := map[string]string{
		"in_stock":                               "in_stock",
		"out_of_stock":                           "out_of_stock",
		"https://schema.org/InStock":             "in_stock",
		"https://schema.org/OutOfStock":          "out_of_stock",
		"https://schema.org/PreOrder":            "pre_order",
		"https://schema.org/LimitedAvailability": "limited",
		"https://schema.org/SoldOut":             "out_of_stock",
		"https://schema.org/Discontinued":        "discontinued",
		"something else":                         "",
	}
	for in, want := range cases {
		if got := normalizeAvailability(in); got != want {
			t.Errorf("normalizeAvailability(%q) = %q want %q", in, got, want)
		}
	}
}

func TestNormalizeCurrency(t *testing.T) {
	if got := normalizeCurrency("usd"); got != "USD" {
		t.Errorf("usd → %q", got)
	}
	if got := normalizeCurrency("$"); got != "USD" {
		t.Errorf("$ → %q", got)
	}
	if got := normalizeCurrency(" BTC "); got != "BTC" {
		t.Errorf("expected BTC passthrough for allowlist rejection, got %q", got)
	}
}
