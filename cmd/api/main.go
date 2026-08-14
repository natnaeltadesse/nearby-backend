// Command api serves the booking platform's HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/booking"
	"github.com/nearby/booking-backend/internal/catalog"
	httpapi "github.com/nearby/booking-backend/internal/http"
	"github.com/nearby/booking-backend/internal/media"
	"github.com/nearby/booking-backend/internal/platform/config"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/logging"
	"github.com/nearby/booking-backend/internal/scheduling"
	"github.com/nearby/booking-backend/internal/tenant"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if config failed, so use stderr.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Spec §7: times are always RFC3339 UTC on the wire.
	//
	// pgx decodes a timestamptz into time.Local no matter what the session
	// timezone is, and time.Now() is local too, so without this the same
	// instant serializes differently depending on which machine the process
	// runs on. Setting the process zone once, before anything reads a clock,
	// makes `Z` the only offset the API can emit.
	//
	// Slot generation is unaffected: it always works in the provider's own
	// timezone, loaded explicitly, never in the process default.
	time.Local = time.UTC

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.Env, cfg.LogLevel, logging.ParseColorMode(cfg.LogColor))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	logger.Info("connected to database", slog.Int("maxConns", int(cfg.DatabaseMaxConns)))

	// Wiring, in dependency order. The only non-obvious edge is
	// scheduling -> catalog: the scheduler declares a ServiceResolver port and
	// the catalog implements it, so the slot generator never learns what a
	// category is.
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.JWTIssuer, cfg.AccessTokenTTL)

	// Verification codes have nowhere to go until an SMS or email provider is
	// configured, so for now they are written to the log. Replacing this one
	// line with a real auth.CodeSender is the entire integration.
	codeSender := auth.NewLogCodeSender(logger)

	authService := auth.NewService(pool, issuer, codeSender, logger,
		cfg.RefreshTokenTTLWeb, cfg.RefreshTokenTTLMobile)
	// Storage is a port: everything above it is written against the interface,
	// so this block is the whole of the local-vs-hosted decision.
	var mediaStorage media.Storage
	var localMedia *media.LocalStorage

	cloudinary := media.NewCloudinaryStorage(
		cfg.CloudinaryCloudName, cfg.CloudinaryAPIKey,
		cfg.CloudinaryAPISecret, cfg.CloudinaryUploadFolder)

	if cloudinary.Configured() {
		mediaStorage = cloudinary
		logger.Info("media: uploads go to cloudinary",
			slog.String("cloud", cfg.CloudinaryCloudName))
	} else {
		local, err := media.NewLocalStorage(cfg.MediaLocalDir, "/media")
		if err != nil {
			logger.Error("media: cannot prepare upload directory",
				slog.String("error", err.Error()))
			os.Exit(1)
		}
		mediaStorage, localMedia = local, local
		// Warn, not info: this is fine on a laptop and wrong in production,
		// and the difference is invisible until the container restarts.
		logger.Warn("media: uploads are on local disk — ephemeral and single-node; set CLOUDINARY_* for hosted storage",
			slog.String("dir", cfg.MediaLocalDir))
	}

	mediaService := media.NewService(pool, mediaStorage, logger)
	tenantService := tenant.NewService(pool, cfg.DefaultTimezone, cfg.MinLeadMinutes)
	catalogService := catalog.New(pool)
	scheduler := scheduling.New(pool, catalogService, scheduling.Options{
		SlotStepMinutes: cfg.SlotStepMinutes,
		CacheTTL:        cfg.AvailabilityCacheTTL,
	})
	bookingService := booking.NewService(pool, catalogService, scheduler)

	handler := httpapi.NewRouter(cfg, logger, pool, httpapi.Services{
		Auth:       authService,
		Tenant:     tenantService,
		Catalog:    catalogService,
		Scheduler:  scheduler,
		Booking:    bookingService,
		Media:      mediaService,
		LocalMedia: localMedia,
		Issuer:     issuer,
	})

	server := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("api listening",
			slog.String("addr", server.Addr),
			slog.String("env", cfg.Env),
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received; draining connections")
	}

	// Give in-flight requests a chance to finish. A booking INSERT cut off
	// mid-transaction is not a disaster — the transaction rolls back — but a
	// customer seeing a network error for a booking that succeeded is worse
	// than waiting a few seconds.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.Info("shutdown complete")
	return nil
}
