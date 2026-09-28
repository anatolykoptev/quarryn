package sources

import "testing"

func TestDistinctVariantSKU(t *testing.T) {
	if got := distinctVariantSKU([]shopifyVariant{{SKU: " A1 "}}); got != "A1" {
		t.Fatalf("single = %q", got)
	}
	if got := distinctVariantSKU([]shopifyVariant{{SKU: "A1"}, {SKU: "A1"}, {}}); got != "A1" {
		t.Fatalf("shared = %q", got)
	}
	if got := distinctVariantSKU([]shopifyVariant{{SKU: "A1-64"}, {SKU: "A1-128"}}); got != "" {
		t.Fatalf("per-variant codes must not become product sku: %q", got)
	}
	if got := distinctVariantSKU([]shopifyVariant{{}, {}}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestCandidateSKULift(t *testing.T) {
	c := candidateFromResult(result("https://s.example.com/p/1", "x", map[string]string{
		MetaSKU: "WH-1000XM5",
	}))
	if c.SKU != "WH-1000XM5" {
		t.Fatalf("sku = %q", c.SKU)
	}
	if _, leaked := c.Metadata[MetaSKU]; leaked {
		t.Fatal("lifted key must not also sit in Metadata")
	}
}
