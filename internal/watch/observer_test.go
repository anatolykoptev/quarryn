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
