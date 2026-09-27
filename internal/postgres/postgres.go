// Package postgres is the service's relational store — the fleet-shared
// postgres container (DATABASE_URL). First consumer: the ADR-10 feedback
// outcome log, moved off the JSONL file so outcomes become queryable
// (joins, aggregates) for calibration.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/retry"
	"github.com/anatolykoptev/quarryn/internal/postgres/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// DB wraps the pool; all access goes through methods on it.
type DB struct {
	pool *pgxpool.Pool
}

// New connects with retry (the shared postgres may still be starting) and
// applies embedded migrations before handing out the pool.
func New(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := retry.Do(ctx, retry.Options{
		MaxAttempts:  5,
		InitialDelay: time.Second,
		MaxDelay:     10 * time.Second,
		Jitter:       true,
		OnRetry: func(attempt int, err error) {
			slog.Warn("postgres not ready, retrying", slog.Int("attempt", attempt), slog.Any("error", err))
		},
	}, func() (*pgxpool.Pool, error) {
		p, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			return nil, err
		}
		if err = p.Ping(ctx); err != nil {
			p.Close()
			return nil, err
		}
		return p, nil
	})
	if err != nil {
		return nil, fmt.Errorf("pg connect: %w", err)
	}
	if err := runMigrations(databaseURL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases the pool.
func (d *DB) Close() { d.pool.Close() }

// Pool exposes the underlying pool for new stateful features
// (watches, #53) that live under this connection's migrations.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// AppendFeedback inserts one ADR-10 outcome row. ts arrives already
// normalized (UTC RFC3339) from the caller's clock so tests keep control.
func (d *DB) AppendFeedback(ctx context.Context, ts time.Time, requestID, pickedURL, verdict string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO feedback (ts, request_id, picked_url, verdict) VALUES ($1, $2, $3, $4)`,
		ts.UTC(), requestID, pickedURL, verdict)
	if err != nil {
		return fmt.Errorf("feedback insert: %w", err)
	}
	return nil
}

// Ping exposes liveness for /healthz-adjacent checks.
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

func runMigrations(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("sql.Open: %w", err)
	}
	defer func() { _ = db.Close() }()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose.SetDialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("goose.Up: %w", err)
	}
	return nil
}
