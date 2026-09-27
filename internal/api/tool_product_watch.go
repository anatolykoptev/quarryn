package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	enginesources "github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/httputil"
	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/quarryn/internal/match"
	"github.com/anatolykoptev/quarryn/internal/money"
	"github.com/anatolykoptev/quarryn/internal/sources"
	"github.com/anatolykoptev/quarryn/internal/watch"
)

// product_watch is the stateful watch surface (issue #53). CRUD + a
// synchronous check_now; the background Checker ticks separately.
//
// watchStorer is the api seam — *watch.Store in prod, a fake in tests.
type watchStorer interface {
	Create(ctx context.Context, w *watch.Watch) error
	List(ctx context.Context, includeInactive bool) ([]watch.Watch, error)
	Get(ctx context.Context, id int64) (watch.Watch, error)
	Cancel(ctx context.Context, id int64) (bool, error)
}
type watchArgs struct {
	Action   string   `json:"action" jsonschema:"add|list|cancel|check_now"`
	Kind     string   `json:"kind,omitempty" jsonschema:"offer|query for add"`
	WatchID  int64    `json:"watch_id,omitempty" jsonschema:"watch id for cancel/check_now"`
	URL      string   `json:"url,omitempty" jsonschema:"offer: page to re-fetch"`
	OfferID  string   `json:"offer_id,omitempty" jsonschema:"offer: stable id from a search result (optional)"`
	Query    string   `json:"query,omitempty" jsonschema:"query: search text"`
	Criteria []string `json:"criteria,omitempty" jsonschema:"query: product_search criteria"`
	Label    string   `json:"label,omitempty" jsonschema:"human name for notifications"`
	// target_price in major units (search-consistent); stored as minor.
	TargetPrice     float64 `json:"target_price,omitempty" jsonschema:"notify when price <= this"`
	Currency        string  `json:"currency,omitempty" jsonschema:"ISO 4217"`
	TTLHours        int     `json:"ttl_hours,omitempty" jsonschema:"watch lifetime; default 720 (30d)"`
	IntervalMinutes int     `json:"interval_minutes,omitempty" jsonschema:"check cadence; floors 60 offer / 360 query"`
	IncludeInactive bool    `json:"include_inactive,omitempty" jsonschema:"list: include cancelled/expired"`
}

type watchOut struct {
	OK          bool         `json:"ok"`
	Watches     []watchEntry `json:"watches,omitempty"`
	Watch       *watchEntry  `json:"watch,omitempty"`
	Count       int          `json:"count,omitempty"`
	Observation string       `json:"observation,omitempty"`
	Error       string       `json:"error,omitempty"`
}

type watchEntry struct {
	ID               int64    `json:"id"`
	Kind             string   `json:"kind"`
	Status           string   `json:"status"`
	URL              string   `json:"url,omitempty"`
	OfferID          string   `json:"offer_id,omitempty"`
	NativeID         bool     `json:"native_id,omitempty"`
	Query            string   `json:"query,omitempty"`
	Criteria         []string `json:"criteria,omitempty"`
	Label            string   `json:"label,omitempty"`
	TargetPriceMinor int64    `json:"target_price_minor"`
	Currency         string   `json:"currency"`
	IntervalMinutes  int      `json:"interval_minutes"`
	ExpiresAt        string   `json:"expires_at"`
	LastCheckedAt    *string  `json:"last_checked_at,omitempty"`
	LastPriceMinor   *int64   `json:"last_price_minor,omitempty"`
	LastAvailability string   `json:"last_availability,omitempty"`
	NotifyCount      int      `json:"notify_count"`
	NotifyPending    bool     `json:"notify_pending,omitempty"`
}

func toWatchEntry(w watch.Watch) watchEntry {
	e := watchEntry{
		ID:               w.ID,
		Kind:             string(w.Kind),
		Status:           w.Status,
		URL:              w.URL,
		OfferID:          w.OfferID,
		NativeID:         w.NativeID,
		Query:            w.Query,
		Criteria:         w.Criteria,
		Label:            w.Label,
		TargetPriceMinor: w.TargetPriceMinor,
		Currency:         w.Currency,
		IntervalMinutes:  int(w.Interval.Minutes()),
		ExpiresAt:        w.ExpiresAt.UTC().Format(time.RFC3339),
		LastPriceMinor:   w.LastPriceMinor,
		LastAvailability: w.LastAvailability,
		NotifyCount:      w.NotifyCount,
		NotifyPending:    w.NotifyPending,
	}
	if w.LastCheckedAt != nil {
		s := w.LastCheckedAt.UTC().Format(time.RFC3339)
		e.LastCheckedAt = &s
	}
	return e
}

func (d deps) handleWatch(ctx context.Context, args watchArgs) watchOut {
	if d.watchStore == nil {
		return watchOut{Error: "watch store unavailable — database not configured"}
	}
	switch args.Action {
	case "add":
		return d.watchAdd(ctx, args)
	case "list":
		return d.watchList(ctx, args)
	case "cancel":
		return d.watchCancel(ctx, args)
	case "check_now":
		return d.watchCheckNow(ctx, args)
	default:
		return watchOut{Error: `unknown action ` + strconv.Quote(args.Action) + ` (want add|list|cancel|check_now)`}
	}
}

func (d deps) watchAdd(ctx context.Context, args watchArgs) watchOut {
	if args.TargetPrice <= 0 {
		return watchOut{Error: "target_price must be > 0"}
	}
	cur := strings.ToUpper(strings.TrimSpace(args.Currency))
	if len(cur) != 3 {
		return watchOut{Error: "currency must be ISO 4217 (3 letters)"}
	}
	for _, r := range cur {
		if r < 'A' || r > 'Z' {
			return watchOut{Error: "currency must be ISO 4217 (3 letters)"}
		}
	}
	targetMinor, ok := money.ToMinor(strconv.FormatFloat(args.TargetPrice, 'f', -1, 64), cur)
	if !ok {
		return watchOut{Error: "invalid currency " + strconv.Quote(cur)}
	}
	ttl := time.Duration(args.TTLHours) * time.Hour
	if args.TTLHours <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	if ttl > 90*24*time.Hour {
		return watchOut{Error: "ttl_hours > 2160 (90d): watches must be bounded"}
	}
	w := watch.Watch{
		Label:            args.Label,
		TargetPriceMinor: targetMinor,
		Currency:         cur,
		ExpiresAt:        time.Now().Add(ttl),
		Status:           watch.StatusActive,
	}
	var werr string
	switch args.Kind {
	case string(watch.KindOffer):
		werr = watchAddOffer(ctx, args, &w)
	case string(watch.KindQuery):
		werr = watchAddQuery(args, &w)
	default:
		return watchOut{Error: `kind must be "offer" or "query"`}
	}
	if werr != "" {
		return watchOut{Error: werr}
	}
	if err := d.watchStore.Create(ctx, &w); err != nil {
		return watchOut{Error: "store: " + err.Error()}
	}
	e := toWatchEntry(w)
	return watchOut{OK: true, Watch: &e}
}

// watchAddOffer fills the offer-kind fields: pinned URL (SSRF-checked) and
// the codec identity — caller-supplied or url-derived.
func watchAddOffer(ctx context.Context, args watchArgs, w *watch.Watch) string {
	if args.URL == "" {
		return "url required for kind=offer"
	}
	if err := httputil.CheckRawURL(ctx, args.URL); err != nil {
		return "url: " + err.Error()
	}
	w.Kind = watch.KindOffer
	w.URL = args.URL
	if args.OfferID != "" {
		head, _, ok := sources.ParseOfferID(args.OfferID)
		if !ok {
			return "malformed offer_id"
		}
		w.OfferID = args.OfferID
		w.NativeID = head != "url"
	} else {
		w.OfferID = sources.OfferID(enginesources.Result{URL: args.URL})
	}
	return watchInterval(args.IntervalMinutes, watch.MinOfferInterval, w)
}

// watchAddQuery fills the query-kind fields: search text + criteria,
// validated eagerly — a malformed criterion would never match and the
// watch would silently observe forever.
func watchAddQuery(args watchArgs, w *watch.Watch) string {
	if args.Query == "" {
		return "query required for kind=query"
	}
	if _, err := match.PlanCriteria(args.Criteria); err != nil {
		return "criteria: " + err.Error()
	}
	w.Kind = watch.KindQuery
	w.Query = args.Query
	w.Criteria = args.Criteria
	return watchInterval(args.IntervalMinutes, watch.MinQueryInterval, w)
}

// watchInterval applies the caller's cadence or the kind's floor-default.
func watchInterval(minutes int, floor time.Duration, w *watch.Watch) string {
	if minutes <= 0 {
		w.Interval = floor
		return ""
	}
	w.Interval = time.Duration(minutes) * time.Minute
	if w.Interval < floor {
		return fmt.Sprintf("interval below %s floor %dm", w.Kind, int(floor.Minutes()))
	}
	return ""
}

func (d deps) watchList(ctx context.Context, args watchArgs) watchOut {
	ws, err := d.watchStore.List(ctx, args.IncludeInactive)
	if err != nil {
		return watchOut{Error: err.Error()}
	}
	out := watchOut{OK: true, Count: len(ws)}
	for _, w := range ws {
		out.Watches = append(out.Watches, toWatchEntry(w))
	}
	return out
}

func (d deps) watchCancel(ctx context.Context, args watchArgs) watchOut {
	if args.WatchID == 0 {
		return watchOut{Error: "watch_id required"}
	}
	ok, err := d.watchStore.Cancel(ctx, args.WatchID)
	if err != nil {
		return watchOut{Error: err.Error()}
	}
	if !ok {
		return watchOut{Error: fmt.Sprintf("watch %d not found or already inactive", args.WatchID)}
	}
	return watchOut{OK: true}
}

// check_now runs a single synchronous check — the operator's live probe.
func (d deps) watchCheckNow(ctx context.Context, args watchArgs) watchOut {
	if args.WatchID == 0 {
		return watchOut{Error: "watch_id required"}
	}
	if d.checker == nil {
		return watchOut{Error: "checker not running"}
	}
	w, err := d.watchStore.Get(ctx, args.WatchID)
	if err != nil {
		return watchOut{Error: fmt.Sprintf("watch %d: %v", args.WatchID, err)}
	}
	updated, obs, err := d.checker.CheckOnce(ctx, w)
	if err != nil {
		return watchOut{Error: "check: " + err.Error()}
	}
	e := toWatchEntry(updated)
	// The observation surfaces in the response — check_now is the live
	// probe, its outcome is the payload.
	return watchOut{OK: true, Watch: &e, Watches: nil,
		Error: "", Count: 0, Observation: obs.Outcome}
}

func registerProductWatch(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name: "product_watch",
		Description: "Price watches (issue #53): kind=offer re-fetches one pinned " +
			"URL; kind=query re-runs the search for the cheapest matching offer. " +
			"Notifies via an Alertmanager webhook on a target-price hit — at-least-once ledger, " +
			"1%-of-target dedupe, bounded lifetime (ttl_hours). check_now runs " +
			"one synchronous check. Notification only — never a purchase.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args watchArgs) (*mcp.CallToolResult, error) {
		return jsonResult(d.handleWatch(ctx, args))
	})
}
