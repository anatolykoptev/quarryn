package sources

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
)

func TestOfferIDPerAdapter(t *testing.T) {
	cases := []struct {
		name string
		r    sources.Result
		want string
	}{
		{"ebay item", sources.Result{
			URL:      "https://www.ebay.com/itm/176543210987",
			Metadata: map[string]string{MetaSource: "ebay", MetaListingID: "176543210987"},
		}, "ebay|176543210987"},
		{"etsy listing", sources.Result{
			URL:      "https://www.etsy.com/listing/1472098365/x",
			Metadata: map[string]string{MetaSource: "etsy", MetaListingID: "1472098365"},
		}, "etsy|1472098365"},
		{"shopify scoped by shop domain", sources.Result{
			URL: "https://shop.example/products/mug",
			Metadata: map[string]string{
				MetaSource: "shopify", MetaListingID: "881001", MetaSeller: "shop.example"},
		}, "shopify|shop.example|881001"},
		{"shopify seller given as URL", sources.Result{
			URL: "https://www.shop.example/products/mug",
			Metadata: map[string]string{
				MetaSource: "shopify", MetaListingID: "881001", MetaSeller: "https://www.shop.example/"},
		}, "shopify|shop.example|881001"},
		{"shopify without seller still stable", sources.Result{
			URL:      "https://shop.example/products/mug",
			Metadata: map[string]string{MetaSource: "shopify", MetaListingID: "881001"},
		}, "shopify|881001"},
		{"slickdeals thread from URL", sources.Result{
			URL:      "https://slickdeals.net/f/18273645-sony-wh-1000xm5",
			Metadata: map[string]string{MetaSource: "slickdeals"},
		}, "slickdeals|18273645"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OfferID(tc.r); got != tc.want {
				t.Fatalf("OfferID = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOfferIDURLFallback: no native listing id → stable url-hash handle.
// Fragment and query order must not fork the identity.
func TestOfferIDURLFallback(t *testing.T) {
	a := OfferID(sources.Result{
		URL:      "https://Shop.Example/p/42?utm_source=x&size=L#reviews",
		Metadata: map[string]string{MetaSource: "other"},
	})
	b := OfferID(sources.Result{
		URL:      "https://shop.example/p/42?size=L&utm_source=x",
		Metadata: map[string]string{MetaSource: "other"},
	})
	if !strings.HasPrefix(a, "url|") || len(a) != len("url|")+16 {
		t.Fatalf("fallback id = %q", a)
	}
	// Tracking params are already stripped by the funnel; order + fragment
	// must not change the hash.
	if a != b {
		t.Fatalf("order/fragment forked identity: %q vs %q", a, b)
	}
}

func TestOfferIDEmpty(t *testing.T) {
	if got := OfferID(sources.Result{}); got != "" {
		t.Fatalf("empty result produced id %q", got)
	}
}

func TestParseOfferID(t *testing.T) {
	adapter, parts, ok := ParseOfferID("shopify|shop.example|881001")
	if !ok || adapter != "shopify" || len(parts) != 2 || parts[1] != "881001" {
		t.Fatalf("parse = %q %v %v", adapter, parts, ok)
	}
	for _, bad := range []string{"", "nopipes", "|tail", "head|"} {
		if _, _, ok := ParseOfferID(bad); ok {
			t.Fatalf("malformed id %q parsed ok", bad)
		}
	}
}

// TestCandidateCarriesOfferID pins the wiring: candidateFromResult builds
// the identity at decode so no consumer can forget it.
func TestCandidateCarriesOfferID(t *testing.T) {
	c := candidateFromResult(sources.Result{
		Title: "t", URL: "https://www.ebay.com/itm/99",
		Metadata: map[string]string{MetaSource: "ebay", MetaListingID: "99"},
	})
	if c.OfferID != "ebay|99" {
		t.Fatalf("candidate offer_id = %q", c.OfferID)
	}
}
