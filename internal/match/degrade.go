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
//	ctx_deadline       — caller ctx expired while queued or in flight
//	over_candidate_cap — eligible but beyond MaxJeffCandidates (a budget
//	                     decision — does NOT set Result.Degraded)
const (
	reasonSaturated    = "jeff_saturated"
	reasonUnavailable  = "jeff_unavailable"
	reasonTimeout      = "jeff_timeout"
	reasonHTTPError    = "jeff_http_error"
	reasonNoAnswer     = "jeff_no_answer"
	reasonUnconfigured = "jeff_unconfigured"
	reasonCtxDeadline  = "ctx_deadline"
	reasonOverCap      = "over_candidate_cap"
)

// classifyJeffError maps a jeff call failure to its degrade reason.
// parentCtx distinguishes "our per-call deadline fired" (jeff_timeout)
// from "the caller's ctx died mid-flight" (ctx_deadline).
func classifyJeffError(err error, parentCtx context.Context) string {
	var se *jeff.StatusError
	switch {
	case errors.As(err, &se):
		if se.StatusCode == 429 || se.StatusCode == 529 {
			return reasonSaturated
		}
		if se.StatusCode >= 500 {
			return reasonUnavailable
		}
		return reasonHTTPError
	case errors.Is(err, jeff.ErrNoAnswer):
		return reasonNoAnswer
	case isDeadline(err):
		if parentCtx.Err() != nil {
			return reasonCtxDeadline
		}
		return reasonTimeout
	case errors.Is(err, context.Canceled):
		return reasonCtxDeadline
	default:
		// Transport failure, decode error, DNS — jeff is unreachable or
		// speaking garbage; both are service-side unavailability.
		return reasonUnavailable
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
// Budget cap and ctx-queue markings are data, not degrade events; only
// jeff-side reasons bump the degrade counter.
func (m *Matcher) markUnjudged(res *Result, idx int, reason string) {
	res.Candidates[idx].UnjudgedReason = reason
	if reason != reasonOverCap {
		jeffDegradedTotal.WithLabelValues(reason).Inc()
	}
}

// summarizeDegrade aggregates per-candidate unjudged markings into the
// Result-level degrade surface after all Ask goroutines have settled.
// Any jeff-side failure sets Degraded + a counted reason and warns once
// per Match call (ADR-5: degradation is loud).
func summarizeDegrade(res *Result) {
	counts := map[string]int{}
	unjudged := 0
	for _, jc := range res.Candidates {
		r := jc.UnjudgedReason
		if r == "" || r == reasonOverCap {
			continue
		}
		unjudged++
		counts[r]++
	}
	if unjudged == 0 {
		return
	}
	// Dominant reason first for a stable, readable summary.
	reasons := make([]string, 0, len(counts))
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

func joinCounts(counts map[string]int, reasons []string) string {
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", r, counts[r]))
	}
	return strings.Join(parts, ",")
}
