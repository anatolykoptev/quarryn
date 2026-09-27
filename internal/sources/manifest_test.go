package sources

import (
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
)

// TestAdapterManifestConformance is the issue-#55 gate: every adapter in
// the registry ships a valid manifest whose id equals its Name(), and the
// userSession flag agrees with ResolveOutbound — the two declarations
// can't drift. A missing/invalid manifest fails the suite, not the
// runtime.
func TestAdapterManifestConformance(t *testing.T) {
	adapters := map[string]Adapter{
		"ebay":       NewEBay("id", "secret", "", nil),
		"etsy":       NewEtsy("key", "secret", "", nil),
		"slickdeals": NewSlickdeals(stubFetcher{body: "<rss/>"}, ""),
		"shopify": NewShopify(stubFetcher{body: "{}"}, ShopifyConfig{
			Shops: []string{"shop.example"},
		}),
	}
	for name, a := range adapters {
		if a.Name() != name {
			t.Fatalf("registry name %q != adapter Name() %q", name, a.Name())
		}
		spec := a.Spec()
		m := spec.Manifest
		if err := m.Validate(); err != nil {
			t.Fatalf("%s manifest invalid: %v", name, err)
		}
		if m.ID != a.Name() {
			t.Fatalf("%s manifest id %q != Name()", name, m.ID)
		}
		if m.UserSession != spec.ResolveOutbound {
			t.Fatalf("%s: userSession=%v but ResolveOutbound=%v — drift",
				name, m.UserSession, spec.ResolveOutbound)
		}
	}
}

func TestManifestValidatePatterns(t *testing.T) {
	for _, bad := range []string{"*.com", "*.net", "*.a", "no-dot-host", "bad/*.x", "two * stars"} {
		m := Manifest{ID: "t", AllowedHosts: []string{bad}}
		if err := m.Validate(); err == nil {
			t.Errorf("pattern %q accepted", bad)
		}
	}
	for _, good := range []string{"*", "example.com", "*.myshopify.com", "a.b.example.co.uk"} {
		m := Manifest{ID: "t", AllowedHosts: []string{good}}
		if err := m.Validate(); err != nil {
			t.Errorf("pattern %q rejected: %v", good, err)
		}
	}
	if err := (Manifest{ID: "t"}).Validate(); err == nil {
		t.Error("empty allowedHosts accepted")
	}
	if err := (Manifest{AllowedHosts: []string{"*"}}).Validate(); err == nil {
		t.Error("empty id accepted")
	}
}

func TestManifestAllows(t *testing.T) {
	m := Manifest{ID: "t", AllowedHosts: []string{"etsy.com", "*.etsy.com"}}
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"etsy.com", true},
		{"www.etsy.com", true},
		{"deep.sub.etsy.com", true},
		{"notetsy.com", false},
		{"etsy.com.evil.com", false},
		{"WWW.ETSY.COM.", true}, // case + trailing dot folded
		{"", false},
	} {
		if got := m.Allows(tc.host); got != tc.want {
			t.Errorf("Allows(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
	star := Manifest{ID: "t", AllowedHosts: []string{"*"}}
	if !star.Allows("anything.example") {
		t.Error("* manifest should allow any host")
	}
	if !star.AllowsURL("https://evil.example/x") {
		t.Error("* manifest should allow any parseable URL")
	}
	if star.AllowsURL("http://%zz") {
		t.Error("unparseable URL should never be allowed")
	}
}

// TestFunnelDropsOffManifestURL is the runtime half of the gate: a result
// pointing outside the adapter's declared hosts is dropped — an adapter
// bug can't smuggle foreign hosts downstream.
func TestFunnelDropsOffManifestURL(t *testing.T) {
	bounded := stubAdapter{
		name:    "bounded",
		enabled: true,
		manifest: Manifest{
			ID:           "bounded",
			AllowedHosts: []string{"203.0.113.99"}, // TEST-NET literal: no DNS
		},
		results: []sources.Result{
			result("http://203.0.113.99/p/1", "in-manifest deal", nil),
			result("http://203.0.113.20/x", "off-manifest deal", nil),
		},
	}
	out, err := NewFunnel(map[string]Adapter{"bounded": bounded}).Search(
		t.Context(), sources.Query{Text: "deal"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 (off-manifest dropped)", len(out.Candidates))
	}
	st := statusOf(out, "bounded")
	if st.Rejected != 1 || st.Count != 1 {
		t.Fatalf("status = %+v, want count=1 rejected=1", st)
	}
}
