package watch

import (
	"context"
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

// SearcherObserver backs the checker with search.Searcher: MatchURL for
// offer watches (same listing), SearchDetailed for query watches.
type SearcherObserver struct {
	s *search.Searcher
}

// NewSearcherObserver wires the concrete pipeline.
func NewSearcherObserver(s *search.Searcher) *SearcherObserver {
	return &SearcherObserver{s: s}
}

// Observe dispatches on watch kind: query → re-search, offer → re-fetch.
func (o *SearcherObserver) Observe(ctx context.Context, w Watch) Observation {
	if w.Kind == KindQuery {
		return o.observeQuery(ctx, w)
	}
	return o.observeOffer(ctx, w)
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
		return Observation{
			Outcome: OutcomeExtractEmpty,
			Detail:  "fetched but no product extracted — listing may be gone",
		}
	}
	if w.VariantSel != "" {
		return observeVariant(w, p)
	}
	offerURL := p.BuyURL
	if offerURL == "" {
		offerURL = w.URL
	}
	cur := p.Currency
	if cur == "" {
		cur = w.Currency
	}
	return Observation{
		PriceMinor:   p.PriceMinor,
		Currency:     cur,
		Availability: p.Availability,
		OfferURL:     offerURL,
		OfferID:      w.OfferID,
		Outcome:      OutcomeOK,
		Product:      p,
	}
}

// observeVariant resolves the watch's pinned configuration inside the
// extracted variant matrix (issue #115). Fail-closed: a selector that
// matches nothing is a no_offers observation — never a silent fallthrough
// to the listing's min-price SKU, which would watch the wrong product.
func observeVariant(w Watch, p *extract.Product) Observation {
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
	return Observation{
		PriceMinor:   price,
		Currency:     cur,
		Availability: avail,
		OfferURL:     offerURL,
		OfferID:      w.OfferID,
		Outcome:      OutcomeOK,
		Product:      &scoped,
	}
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

// observeQuery re-runs the watch's search and takes the cheapest passed
// offer in the watch's currency — currency mismatch is a hard skip, a
// GBP offer cannot satisfy a USD target.
func (o *SearcherObserver) observeQuery(ctx context.Context, w Watch) Observation {
	out, err := o.s.SearchDetailed(ctx, w.Query, w.Criteria, 5)
	if err != nil {
		return Observation{Outcome: OutcomeFetchFailed, Detail: err.Error()}
	}
	var best *extract.Product
	var bestURL, bestID string
	for i := range out.Candidates {
		c := &out.Candidates[i]
		if !c.Passed || c.ExtractionFailed || c.NeedsRender {
			continue
		}
		p := &c.Product
		if p.PriceMinor == nil || p.Currency != w.Currency {
			continue
		}
		if best == nil || *p.PriceMinor < *best.PriceMinor {
			best = p
			bestURL = c.URL
			bestID = p.OfferID
		}
	}
	if best == nil {
		return Observation{Outcome: OutcomeNoOffers, Detail: "no passed offer in currency " + w.Currency}
	}
	return Observation{
		PriceMinor:   best.PriceMinor,
		Currency:     best.Currency,
		Availability: best.Availability,
		OfferURL:     bestURL,
		OfferID:      bestID,
		Outcome:      OutcomeOK,
		Product:      best,
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
