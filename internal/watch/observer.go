package watch

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/money"
	"github.com/anatolykoptev/quarryn/internal/search"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

// Observer turns a Watch into an Observation by re-fetching through the
// existing pipeline — never a second fetch path (spec: reuse, don't
// duplicate SSRF/pacing/browser transport).
type Observer interface {
	Observe(ctx context.Context, w Watch) Observation
}

// GroupLookup is the observer's read-only seam onto the durable
// product-group registry (issue #98): *group.Store satisfies it in
// prod; nil disables group watches and observation group-resolution.
type GroupLookup interface {
	// MemberURLs lists a group's known offer URLs, freshest first.
	MemberURLs(ctx context.Context, gid int64, limit int) ([]string, error)
	// GroupByURL resolves a canonical offer URL to its group id, 0 when
	// the URL never proved membership.
	GroupByURL(ctx context.Context, url string) (int64, error)
}

// groupObserveCap bounds the member offers a group check re-reads —
// freshness beats completeness past the head of the list.
const groupObserveCap = 5

// SearcherObserver backs the checker with search.Searcher: MatchURL for
// offer watches (same listing), SearchDetailed for query watches.
type SearcherObserver struct {
	s      *search.Searcher
	groups GroupLookup
}

// NewSearcherObserver wires the concrete pipeline. groups may be nil —
// group watches then observe fetch_failed and offer/query observations
// carry no group_id.
func NewSearcherObserver(s *search.Searcher, groups GroupLookup) *SearcherObserver {
	return &SearcherObserver{s: s, groups: groups}
}

// Observe dispatches on watch kind: query → re-search, group → member
// offers, offer → re-fetch.
func (o *SearcherObserver) Observe(ctx context.Context, w Watch) Observation {
	switch w.Kind {
	case KindQuery:
		return o.observeQuery(ctx, w)
	case KindGroup:
		return o.observeGroup(ctx, w)
	default:
		return o.observeOffer(ctx, w)
	}
}

// observeOffer re-fetches the pinned URL via MatchURL. Nil criteria → the
// deterministic-only path, no jeff calls — an offer check is a page
// fetch, not a judgment.
func (o *SearcherObserver) observeOffer(ctx context.Context, w Watch) Observation {
	out, err := o.s.MatchURL(ctx, w.URL, nil)
	if err != nil {
		return Observation{Outcome: OutcomeFetchFailed, Detail: err.Error()}
	}
	p := firstProduct(out)
	if p == nil || (p.PriceMinor == nil && p.Name == "") {
		return emptyObservation(out)
	}
	if w.VariantSel != "" {
		return o.observeVariant(ctx, w, p)
	}
	offerURL := p.BuyURL
	if offerURL == "" {
		offerURL = w.URL
	}
	cur := p.Currency
	if cur == "" {
		cur = w.Currency
	}
	return o.attachGroup(ctx, w, Observation{
		PriceMinor:   p.PriceMinor,
		Currency:     cur,
		Availability: p.Availability,
		OfferURL:     offerURL,
		OfferID:      w.OfferID,
		Outcome:      OutcomeOK,
		Product:      p,
	})
}

// observeVariant resolves the watch's pinned configuration inside the
// extracted variant matrix (issue #115). Fail-closed: a selector that
// matches nothing is a no_offers observation — never a silent fallthrough
// to the listing's min-price SKU, which would watch the wrong product.
func (o *SearcherObserver) observeVariant(ctx context.Context, w Watch, p *extract.Product) Observation {
	sel := strings.TrimSpace(w.VariantSel)
	var v *sources.Variant
	for i := range p.Variants {
		cand := &p.Variants[i]
		if cand.VariantID == sel ||
			strings.Contains(strings.ToLower(cand.Title), strings.ToLower(sel)) {
			v = cand
			break
		}
	}
	if v == nil {
		return Observation{
			Outcome: OutcomeNoOffers,
			Detail:  "variant " + strconv.Quote(sel) + " not in listing matrix",
		}
	}
	cur := p.Currency
	if v.Currency != "" {
		cur = v.Currency
	}
	if cur == "" {
		cur = w.Currency
	}
	var price *int64
	if v.Price != "" && cur != "" {
		if minor, ok := money.ToMinor(v.Price, cur); ok {
			price = &minor
		}
	}
	avail := variantAvailability(v)
	offerURL := variantOfferURL(w.URL, v)
	// The condition evaluator sees the pinned configuration, not the
	// listing's min-price head — a scoped copy, the parent is untouched.
	scoped := *p
	scoped.PriceMinor = price
	scoped.Availability = avail
	scoped.URL = offerURL
	scoped.Variants = nil // already selected — the matrix would re-confuse the gate
	return o.attachGroup(ctx, w, Observation{
		PriceMinor:   price,
		Currency:     cur,
		Availability: avail,
		OfferURL:     offerURL,
		OfferID:      w.OfferID,
		Outcome:      OutcomeOK,
		Product:      &scoped,
	})
}

// variantAvailability maps the wire flag to the canonical vocabulary.
// Unknown stock stays empty — falling back to the listing's availability
// would let a different SKU's state false-fire the pinned watch's
// restock trigger.
func variantAvailability(v *sources.Variant) string {
	if v.Available == nil {
		return ""
	}
	if *v.Available {
		return "in_stock"
	}
	return "out_of_stock"
}

// variantOfferURL prefers the deep link the source emitted; otherwise it
// composes one on the watch URL. The query is stripped first — appending
// "?variant=" to a URL that already carries one yields a dead link.
func variantOfferURL(watchURL string, v *sources.Variant) string {
	if v.URL != "" {
		return v.URL
	}
	if v.VariantID != "" {
		return strings.Split(watchURL, "?")[0] + "?variant=" + v.VariantID
	}
	return watchURL
}

// observeQuery re-runs the watch's search, then live-confirms the
// cheapest few passed offers before reporting (issue #120). The search
// leg may have ordered candidates on cached prices — up to 3 distinct
// URLs are re-read through MatchURL (freshness-critical, and re-judged
// against the watch's criteria: a listing that changed so it no longer
// qualifies must not alert). The alert carries the cheapest offer that
// still passes LIVE.
func (o *SearcherObserver) observeQuery(ctx context.Context, w Watch) Observation {
	out, err := o.s.SearchDetailed(ctx, w.Query, w.Criteria, 5)
	if err != nil {
		return Observation{Outcome: OutcomeFetchFailed, Detail: err.Error()}
	}
	urls := cheapestURLs(out, w.Currency, 3)
	if len(urls) == 0 {
		return Observation{Outcome: OutcomeNoOffers, Detail: "no passed offer in currency " + w.Currency}
	}
	live, liveURL, lastErr := o.confirmLive(ctx, urls, w)
	if live == nil {
		if lastErr != "" {
			return Observation{Outcome: OutcomeFetchFailed, Detail: "live confirm: " + lastErr}
		}
		return Observation{
			Outcome: OutcomeNoOffers,
			Detail:  "no offer still passes live in currency " + w.Currency,
		}
	}
	return o.attachGroup(ctx, w, Observation{
		PriceMinor:   live.PriceMinor,
		Currency:     live.Currency,
		Availability: live.Availability,
		OfferURL:     liveURL,
		OfferID:      live.OfferID,
		Outcome:      OutcomeOK,
		Product:      live,
	})
}

// observeGroup re-reads the watch's pinned group member offers and
// reports the cheapest that still passes live (issue #98) — the same
// live-confirm discipline observeQuery applies, minus the search leg:
// members are already identity-verified by the group gates. Every
// observation — failed outcomes included — carries the pinned group
// id so the history join never breaks.
func (o *SearcherObserver) observeGroup(ctx context.Context, w Watch) Observation {
	fail := func(outcome, detail string) Observation {
		return Observation{Outcome: outcome, Detail: detail, GroupID: w.GroupID}
	}
	if o.groups == nil {
		return fail(OutcomeFetchFailed, "group registry unavailable")
	}
	urls, err := o.groups.MemberURLs(ctx, w.GroupID, groupObserveCap)
	if err != nil {
		return fail(OutcomeFetchFailed, "group members: "+err.Error())
	}
	if len(urls) == 0 {
		return fail(OutcomeNoOffers, "group has no member offers")
	}
	live, liveURL, lastErr := o.confirmLive(ctx, urls, w)
	if live == nil {
		if lastErr != "" {
			return fail(OutcomeFetchFailed, "live confirm: "+lastErr)
		}
		return fail(OutcomeNoOffers,
			"no member offer still passes live in currency "+w.Currency)
	}
	return Observation{
		PriceMinor:   live.PriceMinor,
		Currency:     live.Currency,
		Availability: live.Availability,
		OfferURL:     liveURL,
		GroupID:      w.GroupID,
		Outcome:      OutcomeOK,
		Product:      live,
	}
}

// attachGroup resolves the observation to its durable group id — the
// price-history join for offer/query observations (issue #98). The
// winning URL is tried first, then the pinned listing URL (registry
// members are stored by listing URL — a resolved merchant BuyURL never
// lands in the registry), then the product's own schema URL. A lookup
// failure is silent: group resolution is additive evidence, the
// observation stands on its own.
func (o *SearcherObserver) attachGroup(ctx context.Context, w Watch, obs Observation) Observation {
	if o.groups == nil {
		return obs
	}
	for _, u := range groupLookupURLs(w, obs) {
		gid, err := o.groups.GroupByURL(ctx, extract.CanonicalURL(u))
		if err == nil && gid != 0 {
			obs.GroupID = gid
			return obs
		}
	}
	return obs
}

// groupLookupURLs orders the candidate URLs a member lookup should try,
// deduped — offer watches carry the merchant link in OfferURL while the
// registry knows the listing URL.
func groupLookupURLs(w Watch, obs Observation) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range []string{obs.OfferURL, w.URL, productURL(obs)} {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func productURL(obs Observation) string {
	if obs.Product == nil {
		return ""
	}
	return obs.Product.URL
}

// cheapestURLs returns up to n distinct candidate URLs that passed and
// carry a price in the watch's currency, cheapest first — a GBP offer
// cannot satisfy a USD target.
func cheapestURLs(out search.Output, currency string, n int) []string {
	var refs []offerRef
	seen := map[string]bool{}
	for i := range out.Candidates {
		c := &out.Candidates[i]
		if !c.Passed || c.ExtractionFailed || c.NeedsRender || seen[c.URL] {
			continue
		}
		if c.Product.PriceMinor == nil || c.Product.Currency != currency {
			continue
		}
		seen[c.URL] = true
		refs = append(refs, offerRef{url: c.URL, price: *c.Product.PriceMinor})
	}
	slices.SortFunc(refs, func(a, b offerRef) int {
		return cmp.Compare(a.price, b.price)
	})
	urls := make([]string, 0, min(n, len(refs)))
	for _, r := range refs[:min(n, len(refs))] {
		urls = append(urls, r.url)
	}
	return urls
}

// confirmLive re-reads each URL live and returns the cheapest product
// that still passes the watch's criteria, or the last confirm error.
func (o *SearcherObserver) confirmLive(ctx context.Context, urls []string, w Watch) (*extract.Product, string, string) {
	var live *extract.Product
	var liveURL, lastErr string
	for _, u := range urls {
		cout, cerr := o.s.MatchURL(ctx, u, w.Criteria)
		if cerr != nil {
			lastErr = cerr.Error()
			continue
		}
		lc := passedProduct(cout)
		if lc == nil || lc.Currency != w.Currency {
			continue
		}
		if live == nil || *lc.PriceMinor < *live.PriceMinor {
			live, liveURL = lc, u
		}
	}
	return live, liveURL, lastErr
}

// offerRef is a passed search candidate queued for live confirmation.
type offerRef struct {
	url   string
	price int64
}

// passedProduct returns the first judged candidate that still passes and
// extracted — the live-confirm leg requires the winner to satisfy the
// watch's criteria again, not merely to have fetched.
func passedProduct(out search.Output) *extract.Product {
	for i := range out.Candidates {
		c := &out.Candidates[i]
		if c.Passed && !c.ExtractionFailed && !c.NeedsRender &&
			c.Product.PriceMinor != nil {
			return &c.Product
		}
	}
	return nil
}

// emptyObservation maps a product-less match result to an honest outcome:
// a fetch-tier disposition (page never yielded parseable data — wall,
// budget, render failure) is transient fetch_failed and must NOT count
// toward unverifiable; a reached-but-empty page ("incomplete", "invalid",
// "llm_budget") is the real extract_empty — listing gone or too thin to
// read (issue #112).
func emptyObservation(out search.Output) Observation {
	for i := range out.Candidates {
		if oc := out.Candidates[i].Outcome; oc != "" {
			switch oc {
			case "fetch_failed", "over_budget", "render_deferred", "render_failed":
				return Observation{
					Outcome: OutcomeFetchFailed,
					Detail:  "extract: " + oc,
				}
			default:
				return Observation{
					Outcome: OutcomeExtractEmpty,
					Detail:  "fetched but no product extracted — listing may be gone (extract: " + oc + ")",
				}
			}
		}
	}
	return Observation{
		Outcome: OutcomeExtractEmpty,
		Detail:  "fetched but no product extracted — listing may be gone",
	}
}

func firstProduct(out search.Output) *extract.Product {
	for i := range out.Candidates {
		c := &out.Candidates[i]
		if !c.ExtractionFailed && !c.NeedsRender &&
			(c.Product.PriceMinor != nil || c.Product.Name != "") {
			return &c.Product
		}
	}
	return nil
}
