package extract

import (
	"strings"
	"testing"
)

func TestCanonicalURLStripsTracking(t *testing.T) {
	clean := "https://www.amazon.com/dp/B09XS7JWHH"
	dirty := "https://www.amazon.com/dp/B09XS7JWHH/?utm_source=newsletter&utm_medium=email&fbclid=IwAR123&gclid=abc&sessionid=xyz&ref=sr_1_1&tag=aff-20#reviews"
	if CanonicalURL(dirty) != CanonicalURL(clean) {
		t.Fatalf("tracking params survived: %q vs %q", CanonicalURL(dirty), CanonicalURL(clean))
	}
}

func TestCanonicalURLKeepsIdentityParams(t *testing.T) {
	a := CanonicalURL("https://shop.example.com/p?utm_campaign=sale&size=XL")
	b := CanonicalURL("https://shop.example.com/p?size=XL")
	if a != b || !strings.Contains(b, "size=XL") {
		t.Fatalf("identity param dropped or tracking kept: %q vs %q", a, b)
	}
	// Different products must not share a key.
	if cacheKey("https://shop.example.com/p?size=XL") == cacheKey("https://shop.example.com/p?size=S") {
		t.Fatal("distinct products collide on canonical key")
	}
}

func TestCanonicalURLNormalizesHostAndScheme(t *testing.T) {
	got := CanonicalURL("HTTPS://WWW.EBAY.COM:443/itm/123/?")
	want := "https://www.ebay.com/itm/123"
	if got != want {
		t.Fatalf("canonical = %q want %q", got, want)
	}
}

func TestCacheKeyIsVersioned(t *testing.T) {
	k := cacheKey("https://a.example.com/p/1")
	if !strings.HasPrefix(k, "v1:") {
		t.Fatalf("key missing extractor-version prefix: %q", k)
	}
	if len(k) != len("v1:")+32 {
		t.Fatalf("key not fnv128a hex: %q", k)
	}
}

func TestCanonicalURLRejectsBadInput(t *testing.T) {
	for _, bad := range []string{"", "not a url", "javascript:alert(1)", "ftp://x/y"} {
		if got := CanonicalURL(bad); got != "" {
			t.Errorf("CanonicalURL(%q) = %q want empty", bad, got)
		}
	}
}
