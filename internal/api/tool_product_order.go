package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-product-search/internal/money"
	"github.com/anatolykoptev/go-product-search/internal/orders"
)

// product_order is the order-tracking surface (issue #57): emails land
// via ingest_eml (MCP) or POST /api/v1/orders/ingest (REST, forward-
// friendly raw body). The order graph lives in PG.
//
// orderStorer is the api seam — *orders.Store in prod, fake in tests.
type orderStorer interface {
	Upsert(ctx context.Context, o *orders.Order) (bool, error)
	List(ctx context.Context, activeOnly bool) ([]orders.Order, error)
	Get(ctx context.Context, id int64) (orders.Order, []orders.Event, error)
	Mark(ctx context.Context, id int64, status string) (bool, error)
}

type orderArgs struct {
	Action     string `json:"action" jsonschema:"ingest_eml|list|get|mark"`
	OrderID    int64  `json:"order_id,omitempty" jsonschema:"order id for get/mark"`
	EML        string `json:"eml,omitempty" jsonschema:"raw RFC822 message; base64 if binary-safe transport needed"`
	EMLBase64  bool   `json:"eml_base64,omitempty" jsonschema:"decode eml from base64"`
	ActiveOnly bool   `json:"active_only,omitempty" jsonschema:"list: hide delivered/returned/cancelled"`
	Status     string `json:"status,omitempty" jsonschema:"mark: delivered|returned|cancelled"`
}

type orderOut struct {
	OK      bool         `json:"ok"`
	Created bool         `json:"created,omitempty"`
	Order   *orderEntry  `json:"order,omitempty"`
	Orders  []orderEntry `json:"orders,omitempty"`
	Events  []orderEvent `json:"events,omitempty"`
	Count   int          `json:"count,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type orderEntry struct {
	ID         int64   `json:"id"`
	Retailer   string  `json:"retailer"`
	OrderNo    string  `json:"order_no,omitempty"`
	Status     string  `json:"status"`
	PlacedAt   *string `json:"placed_at,omitempty"`
	Total      *int64  `json:"total_minor,omitempty"`
	Currency   string  `json:"currency,omitempty"`
	TotalHuman string  `json:"total_human,omitempty"`
	Label      string  `json:"label,omitempty"`
	ReturnBy   *string `json:"return_by,omitempty"`
	TrackingNo string  `json:"tracking_no,omitempty"`
	Carrier    string  `json:"carrier,omitempty"`
	TrackURL   string  `json:"track_url,omitempty"`
	Subject    string  `json:"subject,omitempty"`
}

type orderEvent struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

func toOrderEntry(o orders.Order) orderEntry {
	e := orderEntry{
		ID:         o.ID,
		Retailer:   o.RetailerDomain,
		OrderNo:    o.OrderNo,
		Status:     o.Status,
		Total:      o.TotalMinor,
		Currency:   o.Currency,
		Label:      o.Label,
		TrackingNo: o.TrackingNo,
		Carrier:    o.Carrier,
		TrackURL:   o.TrackURL,
		Subject:    o.Subject,
	}
	if o.PlacedAt != nil {
		s := o.PlacedAt.UTC().Format(time.RFC3339)
		e.PlacedAt = &s
	}
	if o.ReturnBy != nil {
		s := o.ReturnBy.UTC().Format("2006-01-02")
		e.ReturnBy = &s
	}
	if o.TotalMinor != nil && o.Currency != "" {
		e.TotalHuman = o.Currency + " " + money.Format(*o.TotalMinor, o.Currency)
	}
	return e
}

func (d deps) handleOrder(ctx context.Context, args orderArgs) (*mcp.CallToolResult, error) {
	if d.orderStore == nil {
		return errResult("order store unavailable — database not configured"), nil
	}
	switch args.Action {
	case "ingest_eml":
		return d.orderIngest(ctx, args)
	case "list":
		return d.orderList(ctx, args)
	case "get":
		return d.orderGet(ctx, args)
	case "mark":
		return d.orderMark(ctx, args)
	default:
		return errResult(`unknown action (want ingest_eml|list|get|mark)`), nil
	}
}

func (d deps) orderIngest(ctx context.Context, args orderArgs) (*mcp.CallToolResult, error) {
	raw := []byte(args.EML)
	if args.EMLBase64 {
		b, err := base64.StdEncoding.DecodeString(args.EML)
		if err != nil {
			return errResult("eml_base64 decode: " + err.Error()), nil
		}
		raw = b
	}
	if len(raw) < 20 {
		return errResult("eml too short / empty"), nil
	}
	o, created, err := orders.Ingest(ctx, d.orderStore, raw)
	if err != nil {
		return errResult("ingest: " + err.Error()), nil
	}
	e := toOrderEntry(*o)
	out := orderOut{OK: true, Created: created, Order: &e}
	return jsonResult(out)
}

func (d deps) orderList(ctx context.Context, args orderArgs) (*mcp.CallToolResult, error) {
	os_, err := d.orderStore.List(ctx, args.ActiveOnly)
	if err != nil {
		return errResult(err.Error()), nil
	}
	out := orderOut{OK: true, Count: len(os_)}
	for _, o := range os_ {
		out.Orders = append(out.Orders, toOrderEntry(o))
	}
	return jsonResult(out)
}

func (d deps) orderGet(ctx context.Context, args orderArgs) (*mcp.CallToolResult, error) {
	if args.OrderID == 0 {
		return errResult("order_id required"), nil
	}
	o, evs, err := d.orderStore.Get(ctx, args.OrderID)
	if err != nil {
		return errResult(err.Error()), nil
	}
	e := toOrderEntry(o)
	out := orderOut{OK: true, Order: &e}
	for _, ev := range evs {
		out.Events = append(out.Events, orderEvent{
			TS: ev.TS.UTC().Format(time.RFC3339), Kind: ev.Kind, Detail: ev.Detail,
		})
	}
	return jsonResult(out)
}

func (d deps) orderMark(ctx context.Context, args orderArgs) (*mcp.CallToolResult, error) {
	if args.OrderID == 0 || args.Status == "" {
		return errResult("order_id and status required"), nil
	}
	switch args.Status {
	case orders.StatusDelivered, orders.StatusReturned,
		orders.StatusCancelled, orders.StatusConfirmed:
	default:
		return errResult("status must be delivered|returned|cancelled|confirmed"), nil
	}
	ok, err := d.orderStore.Mark(ctx, args.OrderID, args.Status)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if !ok {
		return errResult(fmt.Sprintf("order %d not found or already %s", args.OrderID, args.Status)), nil
	}
	return jsonResult(orderOut{OK: true})
}

// OrdersIngestHandler is the REST twin: POST /api/v1/orders/ingest with a
// raw RFC822 body — the shape mail filters and forward scripts produce.
// OrdersIngestHandler is the REST twin: POST /api/v1/orders/ingest with a
// raw RFC822 body — the shape mail filters and forward scripts produce.
func OrdersIngestHandler(store orderStorer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, `{"error":"order store unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil || len(raw) < 20 {
			http.Error(w, `{"error":"empty or unreadable eml body"}`, http.StatusBadRequest)
			return
		}
		o, created, err := orders.Ingest(r.Context(), store, raw)
		w.Header().Set("content-type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "created": created, "order": toOrderEntry(*o),
		})
	}
}

func registerProductOrder(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name: "product_order",
		Description: "Order tracking (issue #57): ingest a raw order-confirmation " +
			"email (ingest_eml), list/get the order graph, mark delivery/return. " +
			"Parses order no, total, tracking number, computes return-by from the " +
			"retailer window table. Tracking status is a public deep link, not a " +
			"carrier API — mark advances delivered/returned.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args orderArgs) (*mcp.CallToolResult, error) {
		return d.handleOrder(ctx, args)
	})
}
