package sources

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
)

func TestCleanTrackingURLDenylist(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{
			"https://slickdeals.net/f/20060124-deal?utm_source=rss&utm_medium=feed&utm_campaign=deals",
			"https://slickdeals.net/f/20060124-deal",
		},
		{
			"https://shop.example.com/p/1?variant=503&utm_source=shopify&_gsid=abc&size=XL",
			"https://shop.example.com/p/1?size=XL&variant=503",
		},
		{
			"https://www.ebay.com/itm/123?mkcid=1&mkrid=711&campid=5338&customid=x&toolid=10001",
			"https://www.ebay.com/itm/123?toolid=10001",
		},
		{
			"https://amazon.com/dp/B09?tag=aff-20&ascsubtag=x&ref=sr_1_1&linkCode=osi",
			"https://amazon.com/dp/B09?linkCode=osi",
		},
		{
			"https://m.example.com/o?cjdata=MXx8&cjevent=x&irclickid=y&aff_id=1&aff=2&affiliate=z",
			"https://m.example.com/o",
		},
	}
	for _, tc := range cases {
		if got := CleanTrackingURL(tc.in); got != tc.want {
			t.Fatalf("CleanTrackingURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCleanTrackingURLKeepsUntrackedBytes(t *testing.T) {
	t.Parallel()
	clean := "https://shop.example.com/p?variant=1&color=Red"
	if got := CleanTrackingURL(clean); got != clean {
		t.Fatalf("clean URL rewritten: %q", got)
	}
	if got := CleanTrackingURL("not a url ::::"); got != "not a url ::::" {
		t.Fatalf("unparseable must pass through for the SSRF guard: %q", got)
	}
	if got := CleanTrackingURL("https://x.example/a?utm_source=x#frag"); got != "https://x.example/a#frag" {
		t.Fatalf("query tracker must go, fragment must survive: %q", got)
	}
}

// TestFunnelCleansCandidateURLs proves the funnel strips trackers off
// provenance URLs at the boundary — the failure mode (utm_* leaking into
// user-facing results) was observed live on slickdeals cards.
func TestFunnelCleansCandidateURLs(t *testing.T) {
	a := stubAdapter{
		name: "deals", enabled: true, class: FetchClassFetch,
		results: []sources.Result{
			result("http://203.0.113.10/f/999-deal?utm_source=rss&utm_medium=feed&ref=home", "jbl speaker deal", nil),
		},
	}
	f := NewFunnel(map[string]Adapter{"deals": a})
	out, err := f.Search(t.Context(), sources.Query{Text: "jbl"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(out.Candidates))
	}
	got := out.Candidates[0].URL
	if strings.Contains(got, "utm_") || strings.Contains(got, "ref=") {
		t.Fatalf("tracker params leaked into candidate URL: %q", got)
	}
	if got != "http://203.0.113.10/f/999-deal" {
		t.Fatalf("URL = %q, want tracker-free", got)
	}
}
