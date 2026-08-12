package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lineFor(t *testing.T, mode ColorMode, log func(*slog.Logger)) string {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(newDevHandler(&buf, slog.HandlerOptions{Level: slog.LevelDebug}, mode))
	log(logger)

	return strings.TrimRight(buf.String(), "\n")
}

func requestLine(t *testing.T, mode ColorMode, status int) string {
	t.Helper()

	return lineFor(t, mode, func(logger *slog.Logger) {
		logger.Info("request",
			slog.String("method", "GET"),
			slog.String("path", "/api/v1/me/bookings"),
			slog.Int("status", status),
		)
	})
}

func TestDevHandlerColoursByStatus(t *testing.T) {
	cases := map[int]string{
		200: ansiGreen,
		201: ansiGreen,
		304: ansiCyan,
		401: ansiYellow,
		409: ansiYellow,
		500: ansiRed,
		503: ansiRed,
	}

	for status, want := range cases {
		line := requestLine(t, ColorAlways, status)
		assert.Contains(t, line, want+"request", "status %d should tint the message", status)
		assert.Contains(t, line, ansiBold+want, "status %d should tint the code itself", status)
	}
}

// The whole point of the handler: slog's TextHandler escapes ESC, so colour
// has to survive as real bytes rather than as a literal \x1b.
func TestDevHandlerEmitsRealEscapes(t *testing.T) {
	line := requestLine(t, ColorAlways, 200)

	assert.Contains(t, line, "\x1b[", "escapes must be raw")
	assert.NotContains(t, line, `\x1b`, "escapes must not be text-escaped")
}

func TestDevHandlerCanBePlain(t *testing.T) {
	line := requestLine(t, ColorNever, 500)

	assert.NotContains(t, line, "\x1b", "no escapes when colour is off")
	assert.Contains(t, line, "request")
	assert.Contains(t, line, "status=500")
	assert.Contains(t, line, "method=GET")
}

// Colour is decoration; the key=value payload has to be identical either way,
// so switching it off never costs information.
func TestDevHandlerCarriesTheSameContentBothWays(t *testing.T) {
	plain := requestLine(t, ColorNever, 404)
	coloured := requestLine(t, ColorAlways, 404)

	stripped := stripANSI(coloured)
	assert.Equal(t, plain, stripped)
}

func TestDevHandlerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newDevHandler(&buf, slog.HandlerOptions{Level: slog.LevelWarn}, ColorNever))

	logger.Info("ignored")
	logger.Debug("also ignored")
	require.Empty(t, buf.String())

	logger.Warn("kept")
	assert.Contains(t, buf.String(), "kept")
	assert.Contains(t, buf.String(), "WRN")
}

func TestDevHandlerKeepsAttrsFromWith(t *testing.T) {
	line := lineFor(t, ColorNever, func(logger *slog.Logger) {
		logger.With(slog.String("requestId", "abc-123")).
			Info("request", slog.Int("status", 200))
	})

	assert.Contains(t, line, "requestId=abc-123")
	assert.Contains(t, line, "status=200")
}

func TestDevHandlerFlattensGroups(t *testing.T) {
	line := lineFor(t, ColorNever, func(logger *slog.Logger) {
		logger.WithGroup("db").Info("query", slog.String("table", "bookings"))
	})

	assert.Contains(t, line, "db.table=bookings")

	inline := lineFor(t, ColorNever, func(logger *slog.Logger) {
		logger.Info("query", slog.Group("db", slog.Int("rows", 3)))
	})
	assert.Contains(t, inline, "db.rows=3")
}

// Values with spaces would otherwise break the key=value shape a reader (or a
// grep) relies on.
func TestDevHandlerQuotesAwkwardValues(t *testing.T) {
	line := lineFor(t, ColorNever, func(logger *slog.Logger) {
		logger.Error("failed", slog.String("detail", "deadlock detected"))
	})

	assert.Contains(t, line, `detail="deadlock detected"`)
}

func TestParseColorMode(t *testing.T) {
	assert.Equal(t, ColorAlways, ParseColorMode("always"))
	assert.Equal(t, ColorAlways, ParseColorMode("TRUE"))
	assert.Equal(t, ColorNever, ParseColorMode("never"))
	assert.Equal(t, ColorNever, ParseColorMode("0"))
	assert.Equal(t, ColorAuto, ParseColorMode(""))
	assert.Equal(t, ColorAuto, ParseColorMode("nonsense"))
}

// A bytes.Buffer is not a terminal, so `auto` must stay plain.
func TestAutoIsPlainWhenNotATerminal(t *testing.T) {
	assert.NotContains(t, requestLine(t, ColorAuto, 200), "\x1b")
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
