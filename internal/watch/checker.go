package watch

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/quarryn/internal/extract"
)

// ConditionEvaluator is the subjective gate behind Watch.ConditionText
// (issue #96): a fired deterministic trigger still has to satisfy the
// free-form condition — "sold by the brand store", "still under
// warranty". The match service implements it in prod; nil disables the
// gate (condition watches record observations but never notify).
type ConditionEvaluator interface {
	EvaluateCondition(ctx context.Context, condition string, p extract.Product) (bool, error)
}

// Checker is the watch scheduler: a ticker over the due-scan, one
// observation + state transition + optional notify per watch. Single
// goroutine — the fleet is single-instance, and per-tick caps keep one
// stuck watch from starving the rest.
type Checker struct {
	Store     Storer
	Observer  Observer
	Notify    Notifier
	Evaluator ConditionEvaluator // nil → condition watches never fire
	Now       func() time.Time   // test seam; nil → time.Now
	// Bounds.
	Tick        time.Duration // WATCH_TICK; the idle scan period
	MaxPerTick  int           // WATCH_MAX_PER_TICK
	OfferBudget time.Duration // per-check ctx for offer watches
	QueryBudget time.Duration // per-check ctx for query watches
}

// Run drives the loop until ctx ends. Errors per watch are logged and
// recorded — a failing watch never kills the loop.
func (c *Checker) Run(ctx context.Context) {
	tick := c.Tick
	if tick <= 0 {
		tick = 15 * time.Minute
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	c.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sweep(ctx)
		}
	}
}

// CheckOnce is the synchronous single-watch entry — used by the API's
// check_now action and by tests. Returns the watch's post-check state so
// the caller reports what actually happened, not the stale row.
func (c *Checker) CheckOnce(ctx context.Context, w Watch) (Watch, Observation, error) {
	return c.check(ctx, w)
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) sweep(ctx context.Context) {
	due, err := c.Store.Due(ctx, c.MaxPerTick, c.now())
	if err != nil {
		slog.Error("watch: due scan", "error", err)
		return
	}
	for _, w := range due {
		budget := c.OfferBudget
		if w.Kind == KindQuery {
			budget = c.QueryBudget
		}
		func() {
			wctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			if _, _, err := c.check(wctx, w); err != nil {
				slog.Error("watch: check", "watch_id", w.ID, "error", err)
			}
		}()
	}
}

// check runs one observation through: expiry guard → observe → apply
// transition → maybe notify → persist. The watch row mutation and the
// observation insert commit together (Store.Record).
func (c *Checker) check(ctx context.Context, w Watch) (Watch, Observation, error) {
	now := c.now()

	// Expiry wins over any observation — an expired watch is final state,
	// no fetch spent on it.
	if !w.ExpiresAt.After(now) {
		w.Status = StatusExpired
		obs := Observation{Outcome: OutcomeExtractEmpty, Detail: "expired"}
		return w, obs, c.Store.Record(ctx, &w, obs)
	}

	obs := c.Observer.Observe(ctx, w)
	obs.WatchID = w.ID
	obs.TS = now

	// State transition. prevAvail is captured before the update — the
	// restock trigger reads the transition, not the settled state.
	prevAvail := w.LastAvailability
	w.LastCheckedAt = &now
	w.NextCheckAfter = now.Add(w.Interval)
	w.LastPriceMinor = obs.PriceMinor
	if obs.Availability != "" {
		w.LastAvailability = obs.Availability
	}
	switch obs.Outcome {
	case OutcomeOK:
		w.ConsecFailures = 0
		if w.Status == StatusUnverifiable {
			w.Status = StatusActive // a stale-looking listing came back
		}
		// pct-drop anchor (issue #94): the first ok observation fixes the
		// baseline — later drops measure from it.
		if w.BaselineMinor == nil && obs.PriceMinor != nil {
			b := *obs.PriceMinor
			w.BaselineMinor = &b
		}
	case OutcomeExtractEmpty:
		w.ConsecFailures++
		if w.Kind == KindOffer && w.ConsecFailures >= unverifiableAfter {
			w.Status = StatusUnverifiable
			obs.Detail += " — marking unverifiable (stale listing?)"
		}
	default: // fetch_failed, no_offers — transient, don't count toward unverifiable
	}

	if !w.shouldNotify(prevAvail, obs) {
		return w, obs, c.Store.Record(ctx, &w, obs)
	}
	return w, obs, c.gateAndNotify(ctx, &w, obs, prevAvail, now)
}

// gateAndNotify runs the subjective condition gate on a firing trigger,
// then notifies. The gate's verdict split (issue #96, review #101): a
// deliberate rejection suppresses the alert for good — the observation
// commits the transition, no retry owed; an evaluation ERROR is
// retryable — the availability update rolls back so a restock
// transition re-arms next check instead of being swallowed.
func (c *Checker) gateAndNotify(ctx context.Context, w *Watch, obs Observation, prevAvail string, now time.Time) error {
	if w.ConditionText != "" && !w.NotifyPending {
		pass, retryable, detail := c.evalCondition(ctx, *w, obs)
		if !pass {
			if retryable {
				w.LastAvailability = prevAvail
			}
			obs.Detail = detail
			return c.Store.Record(ctx, w, obs)
		}
	}
	return c.notify(ctx, w, obs, now, w.trigger(prevAvail, obs))
}

// evalCondition runs the subjective gate. Returns (pass, retryable,
// detail): retryable marks "couldn't evaluate" (nil evaluator, missing
// product, transport error) — the caller must not commit the restock
// transition for a check that never reached a verdict. A clean
// rejection is permanent.
func (c *Checker) evalCondition(ctx context.Context, w Watch, obs Observation) (bool, bool, string) {
	if c.Evaluator == nil || obs.Product == nil {
		return false, true, "condition unevaluated: match service unavailable"
	}
	ok, err := c.Evaluator.EvaluateCondition(ctx, w.ConditionText, *obs.Product)
	if err != nil {
		return false, true, "condition unevaluated: " + err.Error()
	}
	if !ok {
		return false, false, "condition not met: " + w.ConditionText
	}
	return true, false, ""
}

// notify is the boundary where a check ends — the only outbound effect.
// Pending overrides dedupe: at-least-once means a recorded-but-undelivered
// alert retries until the notifier acks. kind names the trigger that
// fired ("price"|"restock"); pending retries recompute it from the fresh
// observation and may pass "".
func (c *Checker) notify(ctx context.Context, w *Watch, obs Observation, now time.Time, kind string) error {
	// A fresh fire captures its trigger; a retry of a pending alert reuses
	// the captured one — recomputing against the settled observation
	// would label a restock retry "unknown" (Devin Review #101).
	if w.PendingTrigger == "" {
		w.PendingTrigger = kind
	}
	kind = w.PendingTrigger
	w.NotifyPending = true
	w.LastNotifyAttemptAt = &now
	// Persist the pending flag BEFORE sending — a crash between send and
	// ledger-clear must retry, not lose the notification.
	if err := c.Store.Record(ctx, w, obs); err != nil {
		return err
	}
	retryAfter, nerr := c.Notify.Notify(ctx, *w, obs, kind)
	if nerr != nil {
		// Pending stays set; retryAfter pushes the next attempt out.
		w.NextCheckAfter = now.Add(retryAfter)
		return c.Store.Record(ctx, w, Observation{
			Outcome: OutcomeFetchFailed,
			Detail:  "notify failed: " + nerr.Error(),
		})
	}
	w.NotifyPending = false
	w.PendingTrigger = ""
	w.NotifiedPriceMinor = obs.PriceMinor
	w.NotifiedAt = &now
	w.NotifyCount++
	return c.Store.Record(ctx, w, obs)
}
