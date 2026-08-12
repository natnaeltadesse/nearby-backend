// Command worker runs background jobs.
//
// Milestone 10 moves this onto `river` for booking reminders (T-2h) and push
// fan-out. Until then it runs the one job that already has work to do: expiring
// refresh tokens accumulate forever otherwise, and they are the one table that
// grows with every sign-in rather than with the business.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/config"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/logging"
)

const cleanupInterval = time.Hour

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Same reason as cmd/api: everything this process logs or writes should be
	// in UTC regardless of the host's zone.
	time.Local = time.UTC

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.Env, cfg.LogLevel, logging.ParseColorMode(cfg.LogColor)).With(slog.String("component", "worker"))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	queries := db.New(pool)
	logger.Info("worker started", slog.Duration("cleanupInterval", cleanupInterval))

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	// Run once at boot so a restart does not wait a full interval.
	pruneRefreshTokens(ctx, queries, logger)

	for {
		select {
		case <-ctx.Done():
			logger.Info("worker shutting down")
			return nil
		case <-ticker.C:
			pruneRefreshTokens(ctx, queries, logger)
		}
	}
}

// pruneRefreshTokens deletes tokens that expired long enough ago to be useless
// even as an audit trail. The 30-day grace lives in the query.
func pruneRefreshTokens(ctx context.Context, queries *db.Queries, logger *slog.Logger) {
	deleted, err := queries.DeleteExpiredRefreshTokens(ctx)
	if err != nil {
		logger.Error("prune refresh tokens", slog.Any("error", err))
		return
	}
	if deleted > 0 {
		logger.Info("pruned expired refresh tokens", slog.Int64("deleted", deleted))
	}
}
