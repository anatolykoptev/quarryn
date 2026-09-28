package sources

import (
	"strings"
	"testing"

	enginesources "github.com/anatolykoptev/go-engine/sources"
)

func b(v bool) *bool { return &v }

func TestVariantsRoundTrip(t *testing.T) {
	in := []Variant{
		{Title: "64GB / 1TB", VariantID: "501", Price: "3529.00", Available: b(true), URL: "https://s.example/p?variant=501"},
		{Title: "24GB / 512GB", VariantID: "500", Price: "2389.00", Available: b(false)},
	}
	got := DecodeVariants(EncodeVariants(in))
	if len(got) != 2 {
		t.Fatalf("decoded %d variants, want 2", len(got))
	}
	if got[0].VariantID != "501" || got[0].Price != "3529.00" || got[0].Title != "64GB / 1TB" {
		t.Fatalf("variant 0 mangled: %+v", got[0])
	}
}

// A watch or judge pinning an in-stock config must see it before the
// out-of-stock tail — EncodeVariants is the ordering choke point.
func TestEncodeVariantsInStockFirst(t *testing.T) {
	in := []Variant{
		{Title: "oos", Available: b(false)},
		{Title: "unknown", Available: nil}, // nil counts as in-stock
		{Title: "in", Available: b(true)},
	}
	got := DecodeVariants(EncodeVariants(in))
	if got[0].Title != "unknown" || got[1].Title != "in" || got[2].Title != "oos" {
		t.Fatalf("order = %q, %q, %q", got[0].Title, got[1].Title, got[2].Title)
	}
}

func TestEncodeVariantsCap(t *testing.T) {
	in := make([]Variant, maxVariantsWired+50)
	for i := range in {
		in[i] = Variant{Title: "v"}
	}
	got := DecodeVariants(EncodeVariants(in))
	if len(got) != maxVariantsWired {
		t.Fatalf("decoded %d, want cap %d", len(got), maxVariantsWired)
	}
}

func TestDecodeVariantsMalformed(t *testing.T) {
	if DecodeVariants("") != nil || DecodeVariants("  ") != nil ||
		DecodeVariants("{not json") != nil || DecodeVariants("[]") != nil {
		t.Fatal("malformed input must decode to nil")
	}
}

// The funnel boundary is where MetaVariants silently dies if the lift is
// removed — this is the production call site that must stay wired.
func TestCandidateLiftsVariants(t *testing.T) {
	r := enginesources.Result{
		Title: "p", URL: "https://s.example/p",
		Metadata: map[string]string{
			MetaVariants: EncodeVariants([]Variant{{Title: "64GB", Price: "3529", Available: b(true)}}),
		},
	}
	c := candidateFromResult(r)
	if len(c.Variants) != 1 || c.Variants[0].Title != "64GB" {
		t.Fatalf("candidate variants = %+v", c.Variants)
	}
	if _, leaked := c.Metadata[MetaVariants]; leaked {
		t.Fatal("decoded variants must not leak back into Metadata")
	}
}

// A malformed variant payload must survive in Metadata — never dropped.
func TestCandidateKeepsMalformedVariants(t *testing.T) {
	r := enginesources.Result{
		Title: "p", URL: "https://s.example/p",
		Metadata: map[string]string{MetaVariants: "{oops"},
	}
	c := candidateFromResult(r)
	if len(c.Variants) != 0 {
		t.Fatalf("malformed variants decoded: %+v", c.Variants)
	}
	if c.Metadata[MetaVariants] != "{oops" {
		t.Fatalf("malformed payload lost: %+v", c.Metadata)
	}
}

// The adapter's products.json leg must emit the full option matrix —
// collapsing to the min price is the #115 failure mode.
func TestShopifyResultEmitsVariantMatrix(t *testing.T) {
	p := shopifyProduct{Title: "Laptop", Handle: "laptop"}
	p.Variants = []shopifyVariant{
		{ID: 11, Title: "Default Title", Price: "99.00", Available: true},
		{ID: 12, Title: "24GB / 512GB", Price: "2389.00", Available: true},
		{ID: 13, Title: "64GB / 1TB", Price: "3529.00", Available: false},
	}
	res := shopifyResult("shop.example.com", p)
	enc := res.Metadata[MetaVariants]
	if enc == "" {
		t.Fatal("MetaVariants missing from result metadata")
	}
	vs := DecodeVariants(enc)
	if len(vs) != 2 {
		t.Fatalf("variants = %+v (Default Title must be skipped)", vs)
	}
	if vs[0].VariantID != "12" || !strings.HasSuffix(vs[0].URL, "?variant=12") {
		t.Fatalf("variant = %+v", vs[0])
	}
	if vs[1].Title != "64GB / 1TB" || *vs[1].Available {
		t.Fatalf("out-of-stock variant must sort last and stay flagged: %+v", vs[1])
	}
}
