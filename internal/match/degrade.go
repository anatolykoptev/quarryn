package match

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"

	"github.com/anatolykoptev/go-kit/jeff"
)

// Degrade reason vocabulary (ADR-5) — bounded labels doubling as metric
// values and candidate UnjudgedReason strings:
//
//	jeff_saturated     — HTTP 429/529 (jeff queue full / shedding load)
//	jeff_unavailable   — transport error, 5xx, or undecodable response
//	jeff_timeout       — per-call deadline hit while the caller ctx lives
//	jeff_http_error    — other non-2xx (e.g. 401/403 misconfig) — still a
//	                     degrade: the service is unusable either way
//	jeff_no_answer     — 200 OK but an answer entry is missing
//	jeff_unconfigured  — no JEFF_URL; matcher built in degrade mode
//	ctx_deadline       — caller ctx expired while queued or in flight —
//	                     caller cancellation is not a jeff failure: it
//	                     marks the candidate but never bumps
//	                     jeff_degraded_total or sets Result.Degraded
//	over_candidate_cap — eligible but beyond MaxJeffCandidates (a budget
//	                     decision — does NOT set Result.Degraded)
const (
	ReasonJeffSaturated    ReasonCode = "jeff_saturated"
	ReasonJeffUnavailable  ReasonCode = "jeff_unavailable"
	ReasonJeffTimeout      ReasonCode = "jeff_timeout"
	ReasonJeffHTTPError    ReasonCode = "jeff_http_error"
	ReasonJeffNoAnswer     ReasonCode = "jeff_no_answer"
	ReasonJeffUnconfigured ReasonCode = "jeff_unconfigured"
	ReasonCtxDeadline      ReasonCode = "ctx_deadline"
	ReasonOverCandidateCap ReasonCode = "over_candidate_cap"
)

// classifyJeffError maps a jeff call failure to its degrade reason.
// parentCtx distinguishes "our per-call deadline fired" (jeff_timeout)
// from "the caller's ctx died mid-flight" (ctx_deadline).
func classifyJeffError(err error, parentCtx context.Context) ReasonCode {
	var se *jeff.StatusError
	switch {
	case errors.As(err, &se):
		if se.StatusCode == 429 || se.StatusCode == 529 {
			return ReasonJeffSaturated
		}
		if se.StatusCode >= 500 {
			return ReasonJeffUnavailable
		}
		return ReasonJeffHTTPError
	case errors.Is(err, jeff.ErrNoAnswer):
		return ReasonJeffNoAnswer
	case isDeadline(err):
		if parentCtx.Err() != nil {
			return ReasonCtxDeadline
		}
		return ReasonJeffTimeout
	case errors.Is(err, context.Canceled):
		return ReasonCtxDeadline
	default:
		// Transport failure, decode error, DNS — jeff is unreachable or
		// speaking garbage; both are service-side unavailability.
		return ReasonJeffUnavailable
	}
}

// isDeadline reports whether err is a deadline failure — caller ctx, the
// http.Client timeout, or the per-call bound.
func isDeadline(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// markUnjudged flags one candidate as not-jeff-judged and counts the
// degrade. Writes only res.Candidates[idx] — disjoint per goroutine.
func (m *Matcher) markUnjudged(res *Result, idx int, reason ReasonCode) {
	jc := &res.Candidates[idx]
	jc.UnjudgedReason = reason
	switch reason {
	case ReasonOverCandidateCap, ReasonJeffUnconfigured:
		// jeff was never owed a verdict (budget cap / degrade-mode
		// matcher) — Passed keeps its prefilter outcome.
	default:
		// An owed verdict that never arrived is not a pass.
		jc.Passed = false
	}
	if isDegradeTrigger(reason) {
		jeffDegradedTotal.WithLabelValues(string(reason)).Inc()
	}
}

// isDegradeTrigger reports whether an unjudged reason is a jeff-side
// failure — the only markings that bump jeff_degraded_total and feed
// Result.Degraded. over_candidate_cap is a budget decision and
// ctx_deadline is the caller's own cancellation: data, not service
// failures.
func isDegradeTrigger(reason ReasonCode) bool {
	switch reason {
	case "", ReasonOverCandidateCap, ReasonCtxDeadline:
		return false
	}
	return true
}

// summarizeDegrade aggregates per-candidate unjudged markings into the
// Result-level degrade surface after all Ask goroutines have settled.
// Any jeff-side failure sets Degraded + a counted reason and warns once
// per Match call (ADR-5: degradation is loud).
func summarizeDegrade(res *Result) {
	counts := map[ReasonCode]int{}
	unjudged := 0
	for _, jc := range res.Candidates {
		r := jc.UnjudgedReason
		if !isDegradeTrigger(r) {
			continue
		}
		unjudged++
		counts[r]++
	}
	if unjudged == 0 {
		return
	}
	// Dominant reason first for a stable, readable summary.
	reasons := make([]ReasonCode, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(a, b int) bool {
		return counts[reasons[a]] > counts[reasons[b]] ||
			(counts[reasons[a]] == counts[reasons[b]] && reasons[a] < reasons[b])
	})
	res.Degraded = true
	res.DegradeReason = fmt.Sprintf("%s (%d candidates unjudged: %s)",
		reasons[0], unjudged, joinCounts(counts, reasons))
	slog.Warn("match: degraded to deterministic ranking",
		"reason", reasons[0], "unjudged", unjudged, "detail", joinCounts(counts, reasons))
}

func joinCounts(counts map[ReasonCode]int, reasons []ReasonCode) string {
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", r, counts[r]))
	}
	return strings.Join(parts, ",")
}
