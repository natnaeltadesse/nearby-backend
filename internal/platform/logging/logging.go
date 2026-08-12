// Package logging builds the process-wide slog handler.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger: a compact, colour-coded line per record in
// development, JSON everywhere else so the log shipper has something to parse.
//
// Colour is deliberately confined to the development handler. Escape codes in
// a JSON log would corrupt every field they touched for whatever consumes it.
func New(env, level string, color ColorMode) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if env == "development" {
		handler = newDevHandler(os.Stdout, *opts, color)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey struct{}

// WithLogger returns a context carrying logger, so handlers deep in a request
// can log with the request's fields already attached.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext returns the request-scoped logger, or the default one.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
