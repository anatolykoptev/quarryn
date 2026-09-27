// Package orders ingests order-confirmation emails (.eml) into a local
// order graph: parse → dedupe by (retailer, order_no) → return-window
// and carrier-tracking metadata. Issue #57 — northcinder's orders
// package, deterministic-first.
//
// No outbound calls live here: ingestion is local, carrier status needs
// a future Tracker implementation — v1 exposes the public track deep
// link instead of pretending to poll carriers.
package orders

import "time"

// Status lifecycle — v1 infers confirmed→shipped from parsed fields;
// delivered/returned arrive via the mark action until a Tracker exists.
const (
	StatusConfirmed = "confirmed"
	StatusShipped   = "shipped"
	StatusDelivered = "delivered"
	StatusReturned  = "returned"
	StatusCancelled = "cancelled"
)

// Event kinds appended to order_events.
const (
	EventParsed        = "parsed"         // first successful ingest
	EventMerged        = "merged"         // re-sent email added new fields
	EventStatusChanged = "status_changed" // auto or manual transition
	EventMarked        = "marked"         // operator override via API
	EventUnparsed      = "unparsed"       // email landed but yielded nothing
)

// Order is one row of the orders table.
type Order struct {
	ID             int64
	CreatedAt      time.Time
	RetailerDomain string
	OrderNo        string
	Status         string
	PlacedAt       *time.Time // email date — the purchase timestamp
	TotalMinor     *int64
	Currency       string
	Label          string // retailer name or first item line
	EmailFrom      string
	Subject        string
	ReturnBy       *time.Time // placed_at + retailer return window
	TrackingNo     string
	Carrier        string // ups|usps|fedex|dhl|""
	TrackURL       string // public carrier deep link
}

// Event is one row of order_events — the append-only history.
type Event struct {
	ID      int64
	OrderID int64
	TS      time.Time
	Kind    string
	Detail  string
}

// Parsed is what ParseEmail extracts before it becomes a row — nil fields
// stay nil, never invented.
type Parsed struct {
	RetailerDomain string
	RetailerName   string
	OrderNo        string
	TotalMinor     *int64
	Currency       string
	PlacedAt       *time.Time
	Label          string
	EmailFrom      string
	Subject        string
	TrackingNo     string
	Carrier        string
	TrackURL       string
	// UnparsedReason, when set, means the email produced no structured
	// fields — the row still lands (EventUnparsed) so nothing is dropped.
	UnparsedReason string
}
