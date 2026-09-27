package watch

import (
	"context"

	"github.com/anatolykoptev/go-product-search/internal/extract"
	"github.com/anatolykoptev/go-product-search/internal/search"
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
	}
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
