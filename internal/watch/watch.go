// Package watch implements price watches (issue #53, northcinder's
// watches/checker.ts): a user pins an offer or a query, the checker
// re-observes it through the existing budgeted fetch path and notifies
// via the governed dozor webhook on target-price hits.
//
// LAW: a check ends in a notification and nothing else — there is no
// purchase path anywhere in the repo, and this package's only outbound
// HTTP call site is notify.go (architecturally pinned by a test).
package watch

import "time"

// Kind enumerates the two watch shapes from the spec.
type Kind string

const (
	// KindOffer re-fetches one pinned URL — same listing only, never a
	// substitute.
	KindOffer Kind = "offer"
	// KindQuery re-runs the search and watches the cheapest passed offer
	// in the watch's currency.
	KindQuery Kind = "query"
)

// Watch status lifecycle.
const (
	StatusActive       = "active"
	StatusExpired      = "expired"
	StatusCancelled    = "cancelled"
	StatusUnverifiable = "unverifiable"
)

// Observation outcomes — the per-check ledger vocabulary.
const (
	OutcomeOK           = "ok"
	OutcomeFetchFailed  = "fetch_failed"
	OutcomeExtractEmpty = "extract_empty"
	OutcomeNoOffers     = "no_offers"
)

// Floors keep the watch fleet inside the wowa/jeff budget: an offer check
// is one page fetch, a query check is a whole pipeline run.
const (
	MinOfferInterval = 60 * time.Minute
	MinQueryInterval = 6 * time.Hour
	// unverifiableAfter: consecutive extract_empty observations before the
	// watch stops consuming check budget — the spec's "report unverifiable
	// instead of silently watching a stale listing".
	unverifiableAfter = 2
)

// Watch is one row of the watches table.
type Watch struct {
	ID               int64
	CreatedAt        time.Time
	ExpiresAt        time.Time
	Kind             Kind
	OfferID          string // codec identity (url|… fallback allowed)
	NativeID         bool   // offer_id carries a native listing id
	URL              string // offer: re-fetch target; query: last winner
	Label            string
	Query            string
	Criteria         []string
	TargetPriceMinor int64
	Currency         string
	Interval         time.Duration
	Status           string

	LastCheckedAt    *time.Time
	NextCheckAfter   time.Time
	LastPriceMinor   *int64
	LastAvailability string
	ConsecFailures   int

	NotifyPending       bool
	LastNotifyAttemptAt *time.Time
	NotifiedPriceMinor  *int64
	NotifiedAt          *time.Time
	NotifyCount         int
}

// Observation is one check's outcome — also the price-history row.
type Observation struct {
	WatchID      int64
	TS           time.Time
	PriceMinor   *int64
	Currency     string
	Availability string
	OfferURL     string
	OfferID      string
	Outcome      string
	Detail       string
}

// Due reports whether the watch wants a check now.
func (w Watch) Due(now time.Time) bool {
	return w.Status == StatusActive &&
		w.ExpiresAt.After(now) &&
		!w.NextCheckAfter.After(now)
}

// shouldNotify applies the target hit + the 1%-of-target dedupe bucket:
// a price at/under target notifies; a further drop re-notifies only when
// it exceeds 1% of the target — jitter inside the bucket doesn't.
// notify_pending always overrides (at-least-once beats dedupe).
func (w Watch) shouldNotify(obs Observation) bool {
	if w.NotifyPending {
		return true
	}
	if obs.Outcome != OutcomeOK || obs.PriceMinor == nil {
		return false
	}
	price := *obs.PriceMinor
	if price > w.TargetPriceMinor {
		return false
	}
	if w.NotifiedPriceMinor == nil {
		return true
	}
	return *w.NotifiedPriceMinor-price > w.TargetPriceMinor/100
}
