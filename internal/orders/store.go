package orders

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the pg-backed order graph. Single writer (ingest path) plus
// API reads — no leasing needed.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the shared pool; migrations ran in postgres.New.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Upsert inserts the order or merges a re-sent email's new fields into
// the existing row. Returns (order, created) — created=false means the
// (retailer, order_no) pair already existed.
func (s *Store) Upsert(ctx context.Context, o *Order) (created bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
		INSERT INTO orders (retailer_domain, order_no, status, placed_at,
			total_minor, currency, label, email_from, subject, return_by,
			tracking_no, carrier, track_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (retailer_domain, order_no) WHERE order_no <> ''
		DO UPDATE SET
			total_minor  = COALESCE(orders.total_minor,  EXCLUDED.total_minor),
			currency     = COALESCE(NULLIF(orders.currency,''), EXCLUDED.currency),
			tracking_no  = COALESCE(NULLIF(orders.tracking_no,''), EXCLUDED.tracking_no),
			carrier      = COALESCE(NULLIF(orders.carrier,''), EXCLUDED.carrier),
			track_url    = COALESCE(NULLIF(orders.track_url,''), EXCLUDED.track_url),
			return_by    = COALESCE(orders.return_by, EXCLUDED.return_by),
			status       = CASE WHEN orders.status='confirmed'
			                  AND EXCLUDED.tracking_no<>''
			                  THEN 'shipped' ELSE orders.status END
		RETURNING id, created_at, (xmax = 0)`,
		o.RetailerDomain, o.OrderNo, o.Status, o.PlacedAt,
		o.TotalMinor, o.Currency, o.Label, o.EmailFrom, o.Subject,
		o.ReturnBy, o.TrackingNo, o.Carrier, o.TrackURL).
		Scan(&o.ID, &o.CreatedAt, &created)
	if err != nil {
		return false, err
	}
	kind := EventMerged
	if created {
		kind = EventParsed
	}
	if o.Unparsed() {
		kind = EventUnparsed
	}
	if err := s.addEventTx(ctx, tx, o.ID, kind, o.eventDetail()); err != nil {
		return false, err
	}
	return created, tx.Commit(ctx)
}

// Unparsed is the row-level marker — the email landed but yielded no
// structured fields; the row exists so nothing is silently dropped.
func (o *Order) Unparsed() bool {
	return o.OrderNo == "" && o.TotalMinor == nil
}

func (o *Order) eventDetail() string {
	d := o.Subject
	if o.TrackingNo != "" {
		d += " | tracking " + o.Carrier + " " + o.TrackingNo
	}
	return d
}

func (s *Store) addEventTx(ctx context.Context, tx pgx.Tx, orderID int64, kind, detail string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO order_events (order_id, kind, detail) VALUES ($1,$2,$3)`,
		orderID, kind, detail)
	return err
}

// Mark is the operator status override (delivered/returned/cancelled)
// until carrier tracking exists.
func (s *Store) Mark(ctx context.Context, id int64, status string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE orders SET status=$2 WHERE id=$1 AND status<>$2`, id, status)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	err = s.addEventTx(ctx, tx, id, EventMarked, "status → "+status)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// List returns orders, newest first; activeOnly hides terminal states.
func (s *Store) List(ctx context.Context, activeOnly bool) ([]Order, error) {
	q := `SELECT id, created_at, retailer_domain, order_no, status,
		placed_at, total_minor, currency, label, email_from, subject,
		return_by, tracking_no, carrier, track_url FROM orders`
	if activeOnly {
		q += ` WHERE status NOT IN ('delivered','returned','cancelled')`
	}
	q += ` ORDER BY coalesce(placed_at, created_at) DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Get returns one order with its event history.
func (s *Store) Get(ctx context.Context, id int64) (Order, []Event, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, created_at, retailer_domain, order_no, status,
			placed_at, total_minor, currency, label, email_from, subject,
			return_by, tracking_no, carrier, track_url
		FROM orders WHERE id=$1`, id)
	o, err := scanOrder(row)
	if err != nil {
		return o, nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, order_id, ts, kind, detail FROM order_events
		 WHERE order_id=$1 ORDER BY ts`, id)
	if err != nil {
		return o, nil, err
	}
	defer rows.Close()
	var ev []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OrderID, &e.TS, &e.Kind, &e.Detail); err != nil {
			return o, nil, err
		}
		ev = append(ev, e)
	}
	return o, ev, rows.Err()
}

func scanOrder(r interface{ Scan(...any) error }) (Order, error) {
	var o Order
	err := r.Scan(&o.ID, &o.CreatedAt, &o.RetailerDomain, &o.OrderNo,
		&o.Status, &o.PlacedAt, &o.TotalMinor, &o.Currency, &o.Label,
		&o.EmailFrom, &o.Subject, &o.ReturnBy, &o.TrackingNo, &o.Carrier,
		&o.TrackURL)
	return o, err
}

var _ = time.Now
