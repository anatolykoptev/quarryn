// Package watch implements price watches (issue #53, northcinder's
// watches/checker.ts): a user pins an offer or a query, the checker
// re-observes it through the existing budgeted fetch path and notifies
// via an Alertmanager webhook on target-price hits.
//
// LAW: a check ends in a notification and nothing else — there is no
// purchase path anywhere in the repo, and this package's only outbound
// HTTP call site is notify.go (architecturally pinned by a test).
package watch

import (
	"time"

	"github.com/anatolykoptev/quarryn/internal/extract"
)

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

// NotifyOn selects which triggers can fire a check (issue #93): price
// hits only, restock transitions only, or either.
const (
	NotifyPrice   = "price"
	NotifyRestock = "restock"
	NotifyAny     = "any"
)

// buyable/unbuyable split the availability enum (extract/validate.go) on
// the line a restock crosses: out_of_stock|discontinued → any orderable
// state fires the restock trigger.
var unbuyable = map[string]bool{"out_of_stock": true, "discontinued": true}
var buyable = map[string]bool{"in_stock": true, "pre_order": true, "backorder": true, "limited": true}

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
	ID        int64
	CreatedAt time.Time
	ExpiresAt time.Time
	Kind      Kind
	OfferID   string // codec identity (url|… fallback allowed)
	NativeID  bool   // offer_id carries a native listing id
	URL       string // offer: re-fetch target; query: last winner
	Label     string
	Query     string
	Criteria  []string
	// Triggers — at least one applies (SQL CHECK): absolute price target,
	// percent-drop from baseline, or a notify_on mode that includes
	// restock. TargetPriceMinor is nil for pct-only and pure restock
	// watches.
	TargetPriceMinor *int64
	TargetPct        *int   // % drop from BaselineMinor (issue #94)
	BaselineMinor    *int64 // pct anchor — first ok observation sets it
	NotifyOn         string // price | restock | any (issue #93)
	// ConditionText gates a fired trigger through the match service
	// (issue #96); empty = deterministic only.
	ConditionText string
	Currency      string
	Interval      time.Duration
	Status        string

	LastCheckedAt    *time.Time
	NextCheckAfter   time.Time
	LastPriceMinor   *int64
	LastAvailability string
	ConsecFailures   int

	NotifyPending       bool
	PendingTrigger      string // which trigger the pending alert owes (restock|price)
	LastNotifyAttemptAt *time.Time
	NotifiedPriceMinor  *int64
	NotifiedAt          *time.Time
	NotifyCount         int
}

// Observation is one check's outcome — also the price-history row.
// Product is populated by the observer for the condition evaluator; it
// is transient (never persisted, never marshalled).
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
	Product      *extract.Product `json:"-"`
}

// Due reports whether the watch wants a check now.
func (w Watch) Due(now time.Time) bool {
	return w.Status == StatusActive &&
		w.ExpiresAt.After(now) &&
		!w.NextCheckAfter.After(now)
}

// effectiveTarget is the price threshold that fires — the looser of the
// absolute target and the pct-of-baseline drop (issue #94: whichever
// fires first). 0 = no price trigger configured.
func (w Watch) effectiveTarget() int64 {
	var tgt int64
	if w.TargetPriceMinor != nil {
		tgt = *w.TargetPriceMinor
	}
	if w.TargetPct != nil && w.BaselineMinor != nil {
		if pct := *w.BaselineMinor * int64(100-*w.TargetPct) / 100; pct > tgt {
			tgt = pct
		}
	}
	return tgt
}

// trigger reports which trigger an ok observation fires — "restock" or
// "price" — given the watch's pre-check availability (the transition is
// only visible before LastAvailability updates). Restock wins: a
// simultaneous price hit must not dedupe-suppress the transition, and
// the restock label is the more informative alert. Empty = nothing.
// Restock is offer-kind only — a query watch re-picks the cheapest offer
// per check, so cross-listing availability flips are not restocks.
func (w Watch) trigger(prevAvail string, obs Observation) string {
	if obs.Outcome != OutcomeOK {
		return ""
	}
	if w.Kind == KindOffer &&
		(w.NotifyOn == NotifyRestock || w.NotifyOn == NotifyAny) &&
		unbuyable[prevAvail] && buyable[obs.Availability] {
		return "restock"
	}
	if (w.NotifyOn == NotifyPrice || w.NotifyOn == NotifyAny) && w.priceHit(obs) {
		return "price"
	}
	return ""
}

func (w Watch) priceHit(obs Observation) bool {
	if obs.PriceMinor == nil {
		return false
	}
	tgt := w.effectiveTarget()
	return tgt > 0 && *obs.PriceMinor <= tgt
}

// shouldNotify applies the trigger + dedupe. Price hits re-notify only
// past the 1%-of-effective-target bucket — jitter inside doesn't. A
// restock needs no bucket: the availability transition dedupes itself.
// notify_pending always overrides (at-least-once beats dedupe).
func (w Watch) shouldNotify(prevAvail string, obs Observation) bool {
	if w.NotifyPending {
		return true
	}
	switch w.trigger(prevAvail, obs) {
	case "price":
		if w.NotifiedPriceMinor == nil {
			return true
		}
		return *w.NotifiedPriceMinor-*obs.PriceMinor > w.effectiveTarget()/100
	case "restock":
		return true
	}
	return false
}
