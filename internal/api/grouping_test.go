package api

import (
	"testing"

	"github.com/anatolykoptev/quarryn/internal/extract"
)

func priced(key, source, url string, minor int64, passed bool) productResult {
	return productResult{
		URL: url, GroupKey: key, Passed: passed,
		PublicProduct: extract.PublicProduct{
			Source: source, PriceMinor: &minor, Currency: "USD",
		},
	}
}

func TestBuildGroupsCrossStore(t *testing.T) {
	results := []productResult{
		priced("mpn:wh1000xm5", "a-store.com", "https://a-store.com/p/1", 27900, true),
		priced("mpn:wh1000xm5", "b-store.com", "https://b-store.com/p/2", 25199, true),
		priced("sku:lonely1", "a-store.com", "https://a-store.com/p/3", 9900, true),
		{URL: "https://c-store.com/p/4", Passed: true,
			PublicProduct: extract.PublicProduct{Source: "c-store.com"}},
	}
	groups := buildGroups(results)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Key != "mpn:wh1000xm5" || len(g.Offers) != 2 {
		t.Fatalf("group = %+v", g)
	}
	if g.BestOffer == nil || g.BestOffer.Source != "b-store.com" || *g.BestOffer.PriceMinor != 25199 {
		t.Fatalf("best offer = %+v", g.BestOffer)
	}
}

func TestBuildGroupsSameStoreSkipped(t *testing.T) {
	// Two listings on one store sharing a key is dup signal, not a
	// cross-store comparison — tagged per result, no group row.
	results := []productResult{
		priced("mpn:x9999", "same.com", "https://same.com/a", 100, true),
		priced("mpn:x9999", "same.com", "https://same.com/b", 90, true),
	}
	if groups := buildGroups(results); len(groups) != 0 {
		t.Fatalf("same-store cluster emitted: %+v", groups)
	}
}

func TestBuildGroupsMixedCurrencyNoBest(t *testing.T) {
	eur := priced("gtin:0123456789012", "eu.com", "https://eu.com/p", 5000, true)
	eur.Currency = "EUR"
	usd := priced("gtin:0123456789012", "us.com", "https://us.com/p", 6000, true)
	groups := buildGroups([]productResult{eur, usd})
	if len(groups) != 1 {
		t.Fatalf("groups = %d", len(groups))
	}
	if groups[0].BestOffer != nil {
		t.Fatalf("cross-currency best_offer would lie: %+v", groups[0].BestOffer)
	}
}

func TestBuildAssignedGroupsByID(t *testing.T) {
	// Persistent-id path: a keyed offer and a vector-matched offer share
	// one group id — the group reports the exact provenance + its key.
	results := []productResult{
		{URL: "https://a.com/1", GroupKey: "gtin:4006381333931", GroupID: 7, Passed: true,
			PublicProduct: extract.PublicProduct{Source: "a.com", PriceMinor: int64p(27900), Currency: "USD"}},
		{URL: "https://b.com/2", GroupID: 7, Passed: true,
			PublicProduct: extract.PublicProduct{Source: "b.com", PriceMinor: int64p(25199), Currency: "USD"}},
		{URL: "https://c.com/3", GroupID: 9, Passed: true,
			PublicProduct: extract.PublicProduct{Source: "c.com"}},
	}
	groups := buildAssignedGroups(results)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.ID != 7 || g.Key != "gtin:4006381333931" || g.Match != "exact" {
		t.Fatalf("group = %+v", g)
	}
	if g.BestOffer == nil || *g.BestOffer.PriceMinor != 25199 {
		t.Fatalf("best = %+v", g.BestOffer)
	}
}

func TestBuildAssignedGroupsEmbeddingMatch(t *testing.T) {
	// Pure embedding-tier group: no member carried an identifier.
	results := []productResult{
		{URL: "https://a.com/1", GroupID: 11, Passed: true,
			PublicProduct: extract.PublicProduct{Source: "a.com", PriceMinor: int64p(100), Currency: "USD"}},
		{URL: "https://b.com/2", GroupID: 11, Passed: true,
			PublicProduct: extract.PublicProduct{Source: "b.com", PriceMinor: int64p(90), Currency: "USD"}},
	}
	groups := buildAssignedGroups(results)
	if len(groups) != 1 || groups[0].Match != "embedding" || groups[0].Key != "" {
		t.Fatalf("embed group = %+v", groups)
	}
}

func TestBuildAssignedGroupsOrphanFallback(t *testing.T) {
	// Registry outage: the assigner left every result unassigned —
	// keyed offers must still surface through the ephemeral exact-key
	// path rather than vanish from groups entirely.
	results := []productResult{
		{URL: "https://a.com/1", GroupKey: "gtin:4006381333931", Passed: true,
			PublicProduct: extract.PublicProduct{Source: "a.com", PriceMinor: int64p(27900), Currency: "USD"}},
		{URL: "https://b.com/2", GroupKey: "gtin:4006381333931", Passed: true,
			PublicProduct: extract.PublicProduct{Source: "b.com", PriceMinor: int64p(25199), Currency: "USD"}},
	}
	groups := buildAssignedGroups(results)
	if len(groups) != 1 {
		t.Fatalf("orphan keyed results must still group: %+v", groups)
	}
	g := groups[0]
	if g.ID != 0 || g.Key != "gtin:4006381333931" || g.Match != "exact" {
		t.Fatalf("ephemeral group = %+v", g)
	}
}

func int64p(v int64) *int64 { return &v }

func TestBestOfferSkipsFailed(t *testing.T) {
	// The cheapest offer failed the caller's criteria — "best across
	// stores" must never crown an excluded listing.
	offers := []groupOffer{
		{URL: "u1", Source: "a.com", Passed: false, Currency: "USD"},
		{URL: "u2", Source: "b.com", Passed: true, Currency: "USD"},
	}
	min := int64(100)
	dear := int64(200)
	offers[0].PriceMinor = &min
	offers[1].PriceMinor = &dear
	if best := bestOffer(offers); best == nil || best.URL != "u2" {
		t.Fatalf("best = %+v", best)
	}
}
