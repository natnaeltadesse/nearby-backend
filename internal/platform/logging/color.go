package logging

import (
	"io"
	"os"
	"strings"
)

// ANSI SGR codes. Kept as raw strings rather than pulled from a dependency —
// this is the entire colour vocabulary the log output needs.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
	ansiGray   = "\x1b[90m"
)

// ColorMode decides whether log output carries ANSI escapes.
type ColorMode string

// Colour modes, settable through LOG_COLOR.
const (
	// ColorAuto enables colour only when writing to a terminal.
	ColorAuto ColorMode = "auto"
	// ColorAlways forces colour on, for a pipeline that renders ANSI itself.
	ColorAlways ColorMode = "always"
	// ColorNever forces colour off.
	ColorNever ColorMode = "never"
)

// ParseColorMode reads a LOG_COLOR value, defaulting to auto.
func ParseColorMode(raw string) ColorMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ColorAlways), "true", "1", "yes":
		return ColorAlways
	case string(ColorNever), "false", "0", "no":
		return ColorNever
	default:
		return ColorAuto
	}
}

// wantsColor decides whether to emit escapes for a given writer.
//
// NO_COLOR wins over everything except an explicit `always`: it is the
// informal cross-tool standard (https://no-color.org) and a user who exports
// it has asked for plain output from every program they run.
func wantsColor(mode ColorMode, w io.Writer) bool {
	switch mode {
	case ColorNever:
		return false
	case ColorAlways:
		return true
	}

	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	// A dumb terminal cannot render escapes.
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

// isTerminal reports whether w is a character device.
//
// Without this, redirecting the API's output to a file or piping it into a log
// shipper would fill it with escape sequences.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// painter applies colour, or does nothing when colour is off. Having one type
// answer both cases keeps every call site free of `if useColor` branches.
type painter bool

func (p painter) wrap(codes, text string) string {
	if !p || text == "" {
		return text
	}
	return codes + text + ansiReset
}

func (p painter) dim(text string) string  { return p.wrap(ansiDim, text) }
func (p painter) gray(text string) string { return p.wrap(ansiGray, text) }
func (p painter) bold(text string) string { return p.wrap(ansiBold, text) }

// statusColor maps an HTTP status onto the colour the eye expects: green is
// fine, yellow is the caller's fault, red is ours.
func statusColor(status int) string {
	switch {
	case status >= 500:
		return ansiRed
	case status >= 400:
		return ansiYellow
	case status >= 300:
		return ansiCyan
	case status >= 200:
		return ansiGreen
	default:
		return ansiGray
	}
}
