package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Storer is the checker's persistence seam — *Store on pg in prod, a
// fake in tests.
type Storer interface {
	Due(ctx context.Context, limit int, now time.Time) ([]Watch, error)
	Record(ctx context.Context, w *Watch, obs Observation) error
}

// Store is the pg-backed watch persistence. One writer (the checker
// goroutine) plus the API handlers; watches are small and the fleet is
// single-instance, so plain UPDATEs suffice — no row leasing needed.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pool; migrations are the caller's concern (the shared
// postgres.Run already applied them at startup).
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Create inserts a watch, validating invariant shapes at the SQL layer
// (CHECK constraints) and in the API layer before that.
func (s *Store) Create(ctx context.Context, w *Watch) error {
	crit, err := json.Marshal(w.Criteria)
	if err != nil {
		return fmt.Errorf("marshal criteria: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO watches (kind, offer_id, native_id, url, label, query,
			criteria, target_price_minor, currency, interval_minutes,
			expires_at, next_check_after, notify_on, target_pct,
			condition_text)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now(),$12,$13,$14)
		RETURNING id, created_at`,
		w.Kind, w.OfferID, w.NativeID, w.URL, w.Label, w.Query,
		crit, w.TargetPriceMinor, w.Currency,
		int(w.Interval.Minutes()), w.ExpiresAt,
		w.NotifyOn, w.TargetPct, w.ConditionText).
		Scan(&w.ID, &w.CreatedAt)
	return err
}

// List returns watches; inactive ones only when includeInactive.
func (s *Store) List(ctx context.Context, includeInactive bool) ([]Watch, error) {
	q := `SELECT id, created_at, expires_at, kind, offer_id, native_id,
		url, label, query, criteria, target_price_minor, currency,
		interval_minutes, status, last_checked_at, next_check_after,
		last_price_minor, last_availability, consec_failures,
		notify_pending, last_notify_attempt_at, notified_price_minor,
		notified_at, notify_count, notify_on, target_pct,
		baseline_price_minor, condition_text, pending_trigger
		FROM watches`
	if !includeInactive {
		q += ` WHERE status = 'active'`
	}
	q += ` ORDER BY id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Get fetches one watch by id — check_now and revalidation read single
// rows, not the whole fleet.
func (s *Store) Get(ctx context.Context, id int64) (Watch, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, created_at, expires_at, kind, offer_id, native_id,
			url, label, query, criteria, target_price_minor, currency,
			interval_minutes, status, last_checked_at, next_check_after,
			last_price_minor, last_availability, consec_failures,
			notify_pending, last_notify_attempt_at, notified_price_minor,
			notified_at, notify_count, notify_on, target_pct,
			baseline_price_minor, condition_text, pending_trigger
		FROM watches WHERE id=$1`, id)
	return scanWatch(row)
}

// Cancel flips a watch to cancelled from any non-terminal status —
// an unverifiable/expired watch is cancellable too; only cancelled is final.
func (s *Store) Cancel(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE watches SET status='cancelled' WHERE id=$1 AND status<>'cancelled'`, id)
	return tag.RowsAffected() > 0, err
}

// Due returns up to limit active watches whose check window opened.
func (s *Store) Due(ctx context.Context, limit int, now time.Time) ([]Watch, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, created_at, expires_at, kind, offer_id, native_id,
			url, label, query, criteria, target_price_minor, currency,
			interval_minutes, status, last_checked_at, next_check_after,
			last_price_minor, last_availability, consec_failures,
			notify_pending, last_notify_attempt_at, notified_price_minor,
			notified_at, notify_count, notify_on, target_pct,
			baseline_price_minor, condition_text, pending_trigger
		FROM watches
		WHERE status='active' AND expires_at > $1 AND next_check_after <= $1
		ORDER BY next_check_after
		LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanWatch(r rowScanner) (Watch, error) {
	var w Watch
	var crit []byte
	var intervalMin int
	err := r.Scan(&w.ID, &w.CreatedAt, &w.ExpiresAt, &w.Kind, &w.OfferID,
		&w.NativeID, &w.URL, &w.Label, &w.Query, &crit,
		&w.TargetPriceMinor, &w.Currency, &intervalMin, &w.Status,
		&w.LastCheckedAt, &w.NextCheckAfter, &w.LastPriceMinor,
		&w.LastAvailability, &w.ConsecFailures, &w.NotifyPending,
		&w.LastNotifyAttemptAt, &w.NotifiedPriceMinor, &w.NotifiedAt,
		&w.NotifyCount, &w.NotifyOn, &w.TargetPct, &w.BaselineMinor,
		&w.ConditionText, &w.PendingTrigger)
	if err != nil {
		return w, err
	}
	w.Interval = time.Duration(intervalMin) * time.Minute
	if len(crit) > 0 {
		if err := json.Unmarshal(crit, &w.Criteria); err != nil {
			return w, fmt.Errorf("criteria jsonb: %w", err)
		}
	}
	return w, nil
}

// Record persists an observation and applies its state transition to the
// watch row in one transaction — an observation never lands without its
// watch-state update.
func (s *Store) Record(ctx context.Context, w *Watch, obs Observation) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO watch_observations
			(watch_id, price_minor, currency, availability, offer_url,
			 offer_id, outcome, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		w.ID, obs.PriceMinor, obs.Currency, obs.Availability,
		obs.OfferURL, obs.OfferID, obs.Outcome, obs.Detail)
	if err != nil {
		return fmt.Errorf("insert observation: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE watches SET
			status=$2, last_checked_at=$3, next_check_after=$4,
			last_price_minor=$5, last_availability=$6,
			consec_failures=$7, notify_pending=$8,
			last_notify_attempt_at=$9, notified_price_minor=$10,
			notified_at=$11, notify_count=$12,
			baseline_price_minor=$13,
			pending_trigger=$14
		WHERE id=$1`,
		w.ID, w.Status, w.LastCheckedAt, w.NextCheckAfter,
		w.LastPriceMinor, w.LastAvailability, w.ConsecFailures,
		w.NotifyPending, w.LastNotifyAttemptAt, w.NotifiedPriceMinor,
		w.NotifiedAt, w.NotifyCount, w.BaselineMinor, w.PendingTrigger)
	if err != nil {
		return fmt.Errorf("update watch: %w", err)
	}
	return tx.Commit(ctx)
}

// History returns a watch's recorded observations newest-first — the
// ok rows double as the price/availability history (issue #95). Failed
// outcomes are included: "extract_empty" gaps are part of the honest
// record (a listing that disappeared, then came back).
func (s *Store) History(ctx context.Context, id int64, limit int) ([]Observation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ts, price_minor, currency, availability, offer_url,
			offer_id, outcome, detail
		FROM watch_observations
		WHERE watch_id = $1
		ORDER BY ts DESC
		LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(&o.TS, &o.PriceMinor, &o.Currency,
			&o.Availability, &o.OfferURL, &o.OfferID, &o.Outcome,
			&o.Detail); err != nil {
			return nil, err
		}
		o.WatchID = id
		out = append(out, o)
	}
	return out, rows.Err()
}
