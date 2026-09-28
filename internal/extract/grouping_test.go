package extract

import (
	"strings"
	"testing"
)

// identProductHTML: a schema.org Product carrying the full identifier set
// cross-store grouping keys on (issue #98). The GTIN is a real GTIN-13 —
// "0123456789012" passes the mod-10 check.
const identProductHTML = `<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product",
 "name":"Sony WH-1000XM5",
 "sku":"WH1000XM5/B",
 "mpn":"WH-1000XM5B",
 "gtin13":"0123-456789-012",
 "offers":{"@type":"Offer","priceCurrency":"USD","price":"279.00"}}
</script></head><body></body></html>`

func TestGroupKeyPrecedence(t *testing.T) {
	p := Product{SKU: "ABC-12345", MPN: "WH-1000XM5B", GTIN: "0-123456-789012"}
	if k := p.GroupKey(); k != "gtin:0123456789012" {
		t.Fatalf("gtin should win, got %q", k)
	}
	p.GTIN = ""
	if k := p.GroupKey(); k != "mpn:wh1000xm5b" {
		t.Fatalf("mpn should follow, got %q", k)
	}
	p.MPN = ""
	if k := p.GroupKey(); k != "sku:abc12345" {
		t.Fatalf("sku should follow, got %q", k)
	}
	p.SKU = ""
	if k := p.GroupKey(); k != "" {
		t.Fatalf("no identifiers → no key, got %q", k)
	}
}

func TestGroupKeyNormalization(t *testing.T) {
	// Separator/case variance across merchants must collide — the
	// "Z1ML00050 twice in one result set" case from the field.
	a := Product{SKU: "Z1ML-00050"}
	b := Product{SKU: "z1ml00050"}
	if a.GroupKey() != b.GroupKey() {
		t.Fatalf("normalized keys differ: %q vs %q", a.GroupKey(), b.GroupKey())
	}
}

func TestGroupKeyJunkRejected(t *testing.T) {
	// Too short, purely alphabetic, or classic junk — merchant SKUs are
	// store-scoped, so the key tier must demand a vendor-code shape.
	for _, sku := range []string{"", "0", "n/a", "----", "ab12", "BLACKMUG"} {
		if k := (Product{SKU: sku}).GroupKey(); k != "" {
			t.Fatalf("sku %q produced key %q — junk codes must not group", sku, k)
		}
	}
	for _, mpn := range []string{"", "n/a", "-"} {
		if k := (Product{MPN: mpn}).GroupKey(); k != "" {
			t.Fatalf("mpn %q produced key %q", mpn, k)
		}
	}
	// GTIN shape is enforced: wrong length, bad check digit, and the
	// all-same-digit placeholder all refuse a key.
	for _, g := range []string{"123", "12345678", "0000000000000"} {
		if k := (Product{GTIN: g}).GroupKey(); k != "" {
			t.Fatalf("gtin %q produced key %q", g, k)
		}
	}
	// Sanity anchors: real check digits pass. "0123456789012" is GTIN-13,
	// "12345670" is GTIN-8.
	if k := (Product{GTIN: "12345670"}).GroupKey(); k != "gtin:12345670" {
		t.Fatalf("valid gtin8 rejected: %q", k)
	}
	if k := (Product{GTIN: "0123456789012"}).GroupKey(); k != "gtin:0123456789012" {
		t.Fatalf("valid gtin13 rejected: %q", k)
	}
}

func TestSchemaIdentifiers(t *testing.T) {
	p, err := productFromSchema([]byte(identProductHTML), "https://m.example.com/p/x")
	if err != nil {
		t.Fatalf("productFromSchema: %v", err)
	}
	if p.SKU != "WH1000XM5/B" || p.MPN != "WH-1000XM5B" || p.GTIN != "0123-456789-012" {
		t.Fatalf("identifiers = %+v", p)
	}
	if k := p.GroupKey(); k != "gtin:0123456789012" {
		t.Fatalf("group key = %q", k)
	}
}

func TestMergeOptionalIdentifiers(t *testing.T) {
	dst := Product{}
	src := Product{SKU: "S-11111", MPN: "M-2222", GTIN: "0123456789012"}
	mergeOptional(&dst, &src)
	if dst.SKU != src.SKU || dst.MPN != src.MPN || dst.GTIN != src.GTIN {
		t.Fatalf("identifiers not merged: %+v", dst)
	}
	// Present dst values win — detail extraction never overwrites SERP
	// identity.
	dst2 := Product{SKU: "KEEP-ME"}
	mergeOptional(&dst2, &src)
	if dst2.SKU != "KEEP-ME" {
		t.Fatalf("existing sku overwritten: %q", dst2.SKU)
	}
}

func TestDistinctJSSKU(t *testing.T) {
	single := []shopifyJSVariant{{SKU: " EXP-100 "}}
	if got := distinctJSSKU(single); got != "EXP-100" {
		t.Fatalf("single variant sku = %q", got)
	}
	shared := []shopifyJSVariant{{SKU: "EXP-100"}, {SKU: "EXP-100"}}
	if got := distinctJSSKU(shared); got != "EXP-100" {
		t.Fatalf("shared sku = %q", got)
	}
	// A variant missing a code makes the SKU per-configuration —
	// promoting it would group the whole listing under one config.
	partial := []shopifyJSVariant{{SKU: "EXP-100"}, {}}
	if got := distinctJSSKU(partial); got != "" {
		t.Fatalf("partial-coverage sku promoted: %q", got)
	}
	// Per-configuration codes are variant identity — never the product key.
	mixed := []shopifyJSVariant{{SKU: "EXP-100-64"}, {SKU: "EXP-100-128"}}
	if got := distinctJSSKU(mixed); got != "" {
		t.Fatalf("per-variant skus leaked as product sku: %q", got)
	}
}

func TestShopifyJSFillSKU(t *testing.T) {
	prod := &Product{}
	jp := &shopifyJSProduct{
		Title:    "Deck",
		Variants: []shopifyJSVariant{{SKU: "DECK-9000", Price: 1000, Available: true}},
	}
	if !shopifyJSFill(prod, jp, nil, true) {
		t.Fatal("fill contributed nothing")
	}
	if prod.SKU != "DECK-9000" {
		t.Fatalf("sku = %q", prod.SKU)
	}
	if !strings.HasPrefix(prod.GroupKey(), "sku:") {
		t.Fatalf("rescued product should be groupable: %q", prod.GroupKey())
	}
}
