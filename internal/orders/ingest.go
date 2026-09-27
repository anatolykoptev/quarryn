package orders

import "context"

// Upserter is the ingest seam — *Store in prod, fakes in tests.
type Upserter interface {
	Upsert(ctx context.Context, o *Order) (bool, error)
}

// Ingest parses a raw email and upserts the order graph. It is the
// single entry both transports share — the .eml POST/MCP path today, an
// IMAP poller when mailbox creds land.
func Ingest(ctx context.Context, st Upserter, raw []byte) (*Order, bool, error) {
	p, err := ParseEmail(raw)
	if err != nil {
		return nil, false, err
	}
	o := &Order{
		RetailerDomain: p.RetailerDomain,
		OrderNo:        p.OrderNo,
		Status:         StatusConfirmed,
		PlacedAt:       p.PlacedAt,
		TotalMinor:     p.TotalMinor,
		Currency:       p.Currency,
		Label:          p.Label,
		EmailFrom:      p.EmailFrom,
		Subject:        p.Subject,
		TrackingNo:     p.TrackingNo,
		Carrier:        p.Carrier,
		TrackURL:       p.TrackURL,
	}
	if p.TrackingNo != "" {
		o.Status = StatusShipped // tracking number means it's moving
	}
	if p.PlacedAt != nil {
		o.ReturnBy = returnBy(p.RetailerDomain, *p.PlacedAt)
	}
	created, err := st.Upsert(ctx, o)
	return o, created, err
}

// Tracker is the seam for carrier status (UPS/USPS/FedEx) — v1 ships no
// implementation: track_url carries the public deep link and status
// advances by parse (shipped) or mark (delivered/returned). A future
// implementation polls carriers or webhooks and appends order_events.
type Tracker interface {
	// Status reports the carrier's last scan for a tracking number.
	Status(ctx context.Context, carrier, tracking string) (string, error)
}
