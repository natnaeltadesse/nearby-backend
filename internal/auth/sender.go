package auth

import (
	"context"
	"log/slog"
)

// Verification channels.
const (
	ChannelEmail = "email"
	ChannelPhone = "phone"
)

// CodeSender delivers a one-time code to the address it was issued for.
//
// This is the seam an SMS or email provider drops into: the service knows only
// that a code has to reach a destination, never how. Swapping the
// implementation in cmd/api is the whole change.
type CodeSender interface {
	SendCode(ctx context.Context, channel, destination, code string) error
}

// LogCodeSender writes codes to the log instead of delivering them, so the
// flow is usable end to end before a provider is wired up.
//
// It logs at Warn, not Info, precisely because it should be noisy: a code in a
// log file is a code anyone with log access can use, and this must never be
// the implementation running in production.
type LogCodeSender struct {
	logger *slog.Logger
}

// NewLogCodeSender builds the development stand-in.
func NewLogCodeSender(logger *slog.Logger) *LogCodeSender {
	return &LogCodeSender{logger: logger}
}

// SendCode prints the code where a developer can read it.
func (s *LogCodeSender) SendCode(ctx context.Context, channel, destination, code string) error {
	s.logger.WarnContext(ctx, "verification code not delivered — no sender configured",
		slog.String("channel", channel),
		slog.String("destination", destination),
		slog.String("code", code),
	)
	return nil
}
