package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	List(ctx context.Context, owner string, includeInactive bool) ([]watch.Watch, error)
	Get(ctx context.Context, owner string, id int64) (watch.Watch, error)
	Cancel(ctx context.Context, owner string, id int64) (bool, error)
	CountByOwner(ctx context.Context, owner string) (int, error)
	History(ctx context.Context, id int64, limit int) ([]watch.Observation, error)
}
type watchArgs struct {
	Action   string   `json:"action" jsonschema:"add|list|get|cancel|check_now"`
	Kind     string   `json:"kind,omitempty" jsonschema:"offer|query for add"`
	WatchID  int64    `json:"watch_id,omitempty" jsonschema:"watch id for get/cancel/check_now"`
	URL      string   `json:"url,omitempty" jsonschema:"offer: page to re-fetch"`
	OfferID  string   `json:"offer_id,omitempty" jsonschema:"offer: stable id from a search result (optional)"`
	Query    string   `json:"query,omitempty" jsonschema:"query: search text"`
	Criteria []string `json:"criteria,omitempty" jsonschema:"query: product_search criteria"`
	Label    string   `json:"label,omitempty" jsonschema:"human name for notifications"`
	// target_price in major units (search-consistent); stored as minor.
	// Required when notify_on=price|any unless target_pct is set.
	TargetPrice     float64 `json:"target_price,omitempty" jsonschema:"notify when price <= this"`
	TargetPct       int     `json:"target_pct,omitempty" jsonschema:"notify when price drops >= this % from the watch's first observed price (1-99)"`
	NotifyOn        string  `json:"notify_on,omitempty" jsonschema:"price|restock|any — default price; restock fires on unbuyable→buyable transitions"`
	Condition       string  `json:"condition,omitempty" jsonschema:"free-form gate evaluated per check by the match service (needs jeff); a trigger only notifies when it passes"`
	Currency        string  `json:"currency,omitempty" jsonschema:"ISO 4217"`
	TTLHours        int     `json:"ttl_hours,omitempty" jsonschema:"watch lifetime; default 720 (30d)"`
	IntervalMinutes int     `json:"interval_minutes,omitempty" jsonschema:"check cadence; floors 60 offer / 360 query"`
	IncludeInactive bool    `json:"include_inactive,omitempty" jsonschema:"list: include cancelled/expired"`
	History         bool    `json:"history,omitempty" jsonschema:"list/get: attach recorded price+availability history"`
	HistoryLimit    int     `json:"history_limit,omitempty" jsonschema:"history rows per watch; default 100, max 500"`
	// Owner scopes the row to a tenant ("tg:<chat_id>" for bot users).
	// Empty = unscoped fleet caller. The bot always stamps its user.
	Owner string `json:"owner,omitempty" jsonschema:"tenant owner, e.g. tg:<chat_id>; scopes add/list/get/cancel/check_now"`
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
	ID               int64          `json:"id"`
	Kind             string         `json:"kind"`
	Status           string         `json:"status"`
	URL              string         `json:"url,omitempty"`
	OfferID          string         `json:"offer_id,omitempty"`
	NativeID         bool           `json:"native_id,omitempty"`
	Query            string         `json:"query,omitempty"`
	Criteria         []string       `json:"criteria,omitempty"`
	Label            string         `json:"label,omitempty"`
	NotifyOn         string         `json:"notify_on"`
	TargetPriceMinor *int64         `json:"target_price_minor,omitempty"`
	TargetPct        *int           `json:"target_pct,omitempty"`
	BaselineMinor    *int64         `json:"baseline_price_minor,omitempty"`
	ConditionText    string         `json:"condition,omitempty"`
	Currency         string         `json:"currency"`
	IntervalMinutes  int            `json:"interval_minutes"`
	ExpiresAt        string         `json:"expires_at"`
	LastCheckedAt    *string        `json:"last_checked_at,omitempty"`
	LastPriceMinor   *int64         `json:"last_price_minor,omitempty"`
	LastAvailability string         `json:"last_availability,omitempty"`
	NotifyCount      int            `json:"notify_count"`
	NotifyPending    bool           `json:"notify_pending,omitempty"`
	Owner            string         `json:"owner,omitempty"`
	History          []historyEntry `json:"history,omitempty"`
}

// historyEntry is one recorded observation — newest first (issue #95).
type historyEntry struct {
	TS           string `json:"ts"`
	PriceMinor   *int64 `json:"price_minor,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Availability string `json:"availability,omitempty"`
	Outcome      string `json:"outcome"`
	OfferURL     string `json:"offer_url,omitempty"`
}

func toHistoryEntry(o watch.Observation) historyEntry {
	return historyEntry{
		TS:           o.TS.UTC().Format(time.RFC3339),
		PriceMinor:   o.PriceMinor,
		Currency:     o.Currency,
		Availability: o.Availability,
		Outcome:      o.Outcome,
		OfferURL:     o.OfferURL,
	}
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
		NotifyOn:         w.NotifyOn,
		TargetPriceMinor: w.TargetPriceMinor,
		TargetPct:        w.TargetPct,
		BaselineMinor:    w.BaselineMinor,
		ConditionText:    w.ConditionText,
		Currency:         w.Currency,
		IntervalMinutes:  int(w.Interval.Minutes()),
		ExpiresAt:        w.ExpiresAt.UTC().Format(time.RFC3339),
		LastPriceMinor:   w.LastPriceMinor,
		LastAvailability: w.LastAvailability,
		NotifyCount:      w.NotifyCount,
		NotifyPending:    w.NotifyPending,
		Owner:            w.Owner,
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
	case "get":
		return d.watchGet(ctx, args)
	case "cancel":
		return d.watchCancel(ctx, args)
	case "check_now":
		return d.watchCheckNow(ctx, args)
	default:
		return watchOut{Error: `unknown action ` + strconv.Quote(args.Action) + ` (want add|list|get|cancel|check_now)`}
	}
}

func (d deps) watchAdd(ctx context.Context, args watchArgs) watchOut {
	w, werr := buildWatch(args)
	if werr != "" {
		return watchOut{Error: werr}
	}
	w.Owner = args.Owner
	if w.Owner != "" && d.watchOwnerMax > 0 {
		n, err := d.watchStore.CountByOwner(ctx, w.Owner)
		if err != nil {
			return watchOut{Error: "store: " + err.Error()}
		}
		if n >= d.watchOwnerMax {
			return watchOut{Error: fmt.Sprintf("watch cap reached: %d active per owner", d.watchOwnerMax)}
		}
	}
	// Restock needs a stable listing: a query watch re-picks the cheapest
	// offer each check, and cross-listing availability flips would report
	// false restocks (Devin Review #101). DB enforces the same shape.
	if args.Kind == string(watch.KindQuery) && w.NotifyOn != watch.NotifyPrice {
		return watchOut{Error: "notify_on=" + w.NotifyOn + " requires kind=offer — a restock transition needs a stable listing, query watches take price triggers"}
	}
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

// buildWatch validates the trigger config and fills the kind-independent
// fields (issues #93/#94/#96): notify_on picks the trigger set, a
// price-mode watch needs at least one numeric target, condition_text is
// capped. Returns the populated watch or an error string.
func buildWatch(args watchArgs) (watch.Watch, string) {
	notifyOn := args.NotifyOn
	if notifyOn == "" {
		notifyOn = watch.NotifyPrice
	}
	switch notifyOn {
	case watch.NotifyPrice, watch.NotifyRestock, watch.NotifyAny:
	default:
		return watch.Watch{}, "notify_on must be price|restock|any"
	}
	if args.TargetPct < 0 || args.TargetPct > 99 {
		return watch.Watch{}, "target_pct must be 1-99"
	}
	if notifyOn != watch.NotifyRestock && args.TargetPrice <= 0 && args.TargetPct == 0 {
		return watch.Watch{}, "notify_on=" + notifyOn + " needs target_price or target_pct"
	}
	if n := utf8.RuneCountInString(args.Condition); n > 500 {
		return watch.Watch{}, "condition too long (max 500 runes)"
	}
	cur, targetMinor, cerr := watchCurrency(args)
	if cerr != "" {
		return watch.Watch{}, cerr
	}
	ttl := time.Duration(args.TTLHours) * time.Hour
	if args.TTLHours <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	if ttl > 90*24*time.Hour {
		return watch.Watch{}, "ttl_hours > 2160 (90d): watches must be bounded"
	}
	w := watch.Watch{
		Label:            args.Label,
		NotifyOn:         notifyOn,
		TargetPriceMinor: targetMinor,
		ConditionText:    strings.TrimSpace(args.Condition),
		Currency:         cur,
		ExpiresAt:        time.Now().Add(ttl),
		Status:           watch.StatusActive,
	}
	if args.TargetPct != 0 {
		pct := args.TargetPct
		w.TargetPct = &pct
	}
	return w, ""
}

// watchCurrency validates the ISO-4217 currency and converts
// target_price (major units) to minor when present.
func watchCurrency(args watchArgs) (cur string, targetMinor *int64, err string) {
	cur = strings.ToUpper(strings.TrimSpace(args.Currency))
	if len(cur) != 3 {
		return "", nil, "currency must be ISO 4217 (3 letters)"
	}
	for _, r := range cur {
		if r < 'A' || r > 'Z' {
			return "", nil, "currency must be ISO 4217 (3 letters)"
		}
	}
	if args.TargetPrice > 0 {
		tm, ok := money.ToMinor(strconv.FormatFloat(args.TargetPrice, 'f', -1, 64), cur)
		if !ok {
			return "", nil, "invalid currency " + strconv.Quote(cur)
		}
		targetMinor = &tm
	}
	return cur, targetMinor, ""
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
	ws, err := d.watchStore.List(ctx, args.Owner, args.IncludeInactive)
	if err != nil {
		return watchOut{Error: err.Error()}
	}
	out := watchOut{OK: true, Count: len(ws)}
	for _, w := range ws {
		e := toWatchEntry(w)
		if args.History {
			e.History = d.watchHistory(ctx, w.ID, args.HistoryLimit)
		}
		out.Watches = append(out.Watches, e)
	}
	return out
}

// watchGet is the single-watch read — history attaches by default
// (bounded to history_limit).
func (d deps) watchGet(ctx context.Context, args watchArgs) watchOut {
	if args.WatchID == 0 {
		return watchOut{Error: "watch_id required"}
	}
	w, err := d.watchStore.Get(ctx, args.Owner, args.WatchID)
	if err != nil {
		return watchOut{Error: fmt.Sprintf("watch %d not found", args.WatchID)}
	}
	e := toWatchEntry(w)
	e.History = d.watchHistory(ctx, w.ID, args.HistoryLimit)
	return watchOut{OK: true, Watch: &e}
}

// watchHistory fetches the observation ledger for one watch — a history
// read failure degrades to an empty list, never fails the entry.
func (d deps) watchHistory(ctx context.Context, id int64, limit int) []historyEntry {
	obs, err := d.watchStore.History(ctx, id, limit)
	if err != nil {
		return nil
	}
	out := make([]historyEntry, 0, len(obs))
	for _, o := range obs {
		out = append(out, toHistoryEntry(o))
	}
	return out
}

func (d deps) watchCancel(ctx context.Context, args watchArgs) watchOut {
	if args.WatchID == 0 {
		return watchOut{Error: "watch_id required"}
	}
	ok, err := d.watchStore.Cancel(ctx, args.Owner, args.WatchID)
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
	w, err := d.watchStore.Get(ctx, args.Owner, args.WatchID)
	if err != nil {
		return watchOut{Error: fmt.Sprintf("watch %d not found", args.WatchID)}
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
		Description: "Price/restock watches (issues #53, #93-96): kind=offer re-fetches one pinned " +
			"URL; kind=query re-runs the search for the cheapest matching offer. " +
			"Triggers: target_price (absolute), target_pct (% drop vs first observed price), " +
			"notify_on=restock (unbuyable→buyable transitions); optional free-form condition " +
			"gated by the match service. Notifies via webhook — at-least-once ledger, " +
			"1%-of-target dedupe, bounded lifetime (ttl_hours). get/list expose recorded history; " +
			"check_now runs one synchronous check. Notification only — never a purchase.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args watchArgs) (*mcp.CallToolResult, error) {
		return jsonResult(d.handleWatch(ctx, args))
	})
}
