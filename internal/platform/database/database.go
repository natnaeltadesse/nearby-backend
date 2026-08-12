// Package database owns the pgx connection pool.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens and verifies a pool against databaseURL.
func Connect(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}

	// Pin the session timezone so every timestamptz comes back as UTC and
	// therefore marshals as RFC3339 with a `Z`, which spec §7 requires on the
	// wire. Without this the offset follows whatever zone the server process
	// happens to sit in, and two deployments would serialize the same instant
	// differently. Provider-local slot math is unaffected: it works from the
	// provider's own timezone column, never from the session's.
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// IsExclusionViolation reports whether err is the GiST exclusion constraint on
// `bookings` firing — i.e. someone else took the slot between our availability
// read and our INSERT. Callers turn this into 409 SLOT_TAKEN.
func IsExclusionViolation(err error) bool {
	return hasCode(err, pgerrcode.ExclusionViolation)
}

// IsUniqueViolation reports whether err is a duplicate-key error.
func IsUniqueViolation(err error) bool {
	return hasCode(err, pgerrcode.UniqueViolation)
}

// IsForeignKeyViolation reports whether err is a missing-reference error.
func IsForeignKeyViolation(err error) bool {
	return hasCode(err, pgerrcode.ForeignKeyViolation)
}

// IsRetryable reports whether err is a transient concurrency failure that the
// same transaction could succeed at if simply run again.
//
// This matters for booking inserts. When several sessions insert overlapping
// ranges at the same instant, the GiST exclusion constraint does not always
// resolve into a clean 23P01 for the losers: one of them can end up waiting on
// another's uncommitted row and Postgres breaks the cycle with a deadlock
// instead. That is a scheduling artefact of contention, not a bug in the
// caller, so it must never surface as a 500.
func IsRetryable(err error) bool {
	return hasCode(err, pgerrcode.DeadlockDetected) ||
		hasCode(err, pgerrcode.SerializationFailure)
}

// ConstraintName returns the constraint that a Postgres error names, or "".
// Used to tell two unique indexes on the same table apart.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
