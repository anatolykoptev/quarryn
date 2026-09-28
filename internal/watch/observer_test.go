package watch

import (
	"testing"

	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

func variantProduct() *extract.Product {
	min := int64(238900)
	return &extract.Product{
		Name:         "MacBook Pro 14",
		URL:          "https://shop.example.com/products/mbp",
		PriceMinor:   &min,
		Currency:     "USD",
		Availability: "in_stock",
		Variants: []sources.Variant{
			{Title: "15C/16G / 24GB / 512GB", VariantID: "5001", Price: "2389.00",
				Available: boolRef(true), URL: "https://shop.example.com/products/mbp?variant=5001"},
			{Title: "18C/20G / 64GB / 1TB", VariantID: "5002", Price: "3529.00",
				Available: boolRef(false), URL: "https://shop.example.com/products/mbp?variant=5002"},
		},
	}
}

func boolRef(v bool) *bool { return &v }

// A pinned watch must observe ITS configuration — price and availability
// come from the matched variant, never the listing's min-price head.
func TestObserveVariantPinnedBySubstring(t *testing.T) {
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "64GB", Currency: "USD", OfferID: "url|x"}
	obs := observeVariant(w, variantProduct())
	if obs.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q (%s)", obs.Outcome, obs.Detail)
	}
	if obs.PriceMinor == nil || *obs.PriceMinor != 352900 {
		t.Fatalf("price = %v, want 352900 (the 64GB SKU, not the 2389 min)", obs.PriceMinor)
	}
	if obs.Availability != "out_of_stock" {
		t.Fatalf("availability = %q — the pinned variant's stock wins over the listing", obs.Availability)
	}
	if obs.OfferURL != "https://shop.example.com/products/mbp?variant=5002" {
		t.Fatalf("offer_url = %q", obs.OfferURL)
	}
}

func TestObserveVariantPinnedByID(t *testing.T) {
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "5001", Currency: "USD"}
	obs := observeVariant(w, variantProduct())
	if obs.Outcome != OutcomeOK || obs.PriceMinor == nil || *obs.PriceMinor != 238900 {
		t.Fatalf("id-pin failed: %+v", obs)
	}
	if obs.Availability != "in_stock" {
		t.Fatalf("availability = %q", obs.Availability)
	}
}

// Fail-closed is the contract: a selector matching nothing is a
// no_offers observation, never a silent watch of the wrong SKU.
func TestObserveVariantMissFailsClosed(t *testing.T) {
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "128GB", Currency: "USD"}
	obs := observeVariant(w, variantProduct())
	if obs.Outcome != OutcomeNoOffers {
		t.Fatalf("outcome = %q, want no_offers", obs.Outcome)
	}
	if obs.PriceMinor != nil {
		t.Fatalf("miss must not carry a price: %v", obs.PriceMinor)
	}
}

// A variant-less listing under a pinned watch fails closed the same way —
// the empty matrix has no member to follow.
func TestObserveVariantEmptyMatrix(t *testing.T) {
	w := Watch{URL: "https://s.example/p", VariantSel: "64GB"}
	obs := observeVariant(w, &extract.Product{Name: "n", Currency: "USD"})
	if obs.Outcome != OutcomeNoOffers {
		t.Fatalf("outcome = %q", obs.Outcome)
	}
}

// A variant without an availability flag must NOT inherit the listing's
// stock state — a sibling SKU in stock would false-fire a restock.
func TestObserveVariantUnknownStockStaysEmpty(t *testing.T) {
	p := variantProduct()
	p.Variants[0].Available = nil
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "5001", Currency: "USD"}
	obs := observeVariant(w, p)
	if obs.Availability != "" {
		t.Fatalf("availability = %q, want empty (listing state must not leak)", obs.Availability)
	}
}

// A watch URL carrying its own query must not grow a second "?".
func TestObserveVariantOfferURLQuerySafe(t *testing.T) {
	p := variantProduct()
	p.Variants[0].URL = "" // force the fallback branch
	w := Watch{URL: "https://shop.example.com/products/mbp?utm=x",
		VariantSel: "5001", Currency: "USD"}
	obs := observeVariant(w, p)
	if obs.OfferURL != "https://shop.example.com/products/mbp?variant=5001" {
		t.Fatalf("offer_url = %q", obs.OfferURL)
	}
}

// The condition evaluator receives the pinned configuration — the scoped
// product carries the variant's price/availability, never the listing's
// min-price head.
func TestObserveVariantScopesConditionProduct(t *testing.T) {
	p := variantProduct()
	w := Watch{URL: "https://shop.example.com/products/mbp",
		VariantSel: "64GB", Currency: "USD"}
	obs := observeVariant(w, p)
	if obs.Product == nil || obs.Product == p {
		t.Fatal("condition product must be a scoped copy")
	}
	if obs.Product.PriceMinor == nil || *obs.Product.PriceMinor != 352900 {
		t.Fatalf("scoped price = %v, want 352900", obs.Product.PriceMinor)
	}
	if obs.Product.Availability != "out_of_stock" {
		t.Fatalf("scoped availability = %q", obs.Product.Availability)
	}
	// The parent stays untouched for other consumers of the extraction.
	if p.PriceMinor == nil || *p.PriceMinor != 238900 || len(p.Variants) != 2 {
		t.Fatal("parent product mutated by pinning")
	}
}
