package logging

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// devHandler renders one compact, coloured line per record:
//
//	15:04:05.000 INF request  GET /api/v1/me/bookings 201 751B 67ms
//
// It exists because slog's TextHandler escapes non-printable bytes, so ANSI
// codes smuggled in as attribute values arrive as a literal `\x1b[32m`. Colour
// therefore has to be applied by the handler that owns the formatting.
//
// Development only. Production keeps the JSON handler, where colour would
// corrupt the output for whatever parses it.
type devHandler struct {
	opts  slog.HandlerOptions
	paint painter

	// mu and out are shared by every handler derived through WithAttrs or
	// WithGroup, so concurrent requests cannot interleave mid-line.
	mu  *sync.Mutex
	out io.Writer

	// attrs are already rendered and group-prefixed, so WithAttrs costs
	// nothing on the logging path.
	attrs  []renderedAttr
	groups []string
}

type renderedAttr struct {
	key   string
	value string
	// status carries the HTTP status when this attr is one, so the request
	// line can be coloured by it.
	status int
}

func newDevHandler(out io.Writer, opts slog.HandlerOptions, mode ColorMode) *devHandler {
	return &devHandler{
		opts:  opts,
		paint: painter(wantsColor(mode, out)),
		mu:    &sync.Mutex{},
		out:   out,
	}
}

func (h *devHandler) Enabled(_ context.Context, level slog.Level) bool {
	minimum := slog.LevelInfo
	if h.opts.Level != nil {
		minimum = h.opts.Level.Level()
	}
	return level >= minimum
}

func (h *devHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := h.clone()
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, h.render(attr, h.groups)...)
	}
	return clone
}

func (h *devHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := h.clone()
	clone.groups = append(clone.groups, name)
	return clone
}

func (h *devHandler) clone() *devHandler {
	return &devHandler{
		opts:   h.opts,
		paint:  h.paint,
		mu:     h.mu,
		out:    h.out,
		attrs:  append(make([]renderedAttr, 0, len(h.attrs)+4), h.attrs...),
		groups: append(make([]string, 0, len(h.groups)+1), h.groups...),
	}
}

func (h *devHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := make([]renderedAttr, 0, len(h.attrs)+record.NumAttrs())
	attrs = append(attrs, h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, h.render(attr, h.groups)...)
		return true
	})

	// The status drives the accent colour for the whole line, so it has to be
	// known before anything is written.
	status := 0
	for _, attr := range attrs {
		if attr.status != 0 {
			status = attr.status
			break
		}
	}

	var b strings.Builder

	timestamp := record.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	b.WriteString(h.paint.gray(timestamp.Format("15:04:05.000")))
	b.WriteByte(' ')
	b.WriteString(h.levelBadge(record.Level))
	b.WriteByte(' ')
	b.WriteString(h.message(record, status))

	for _, attr := range attrs {
		b.WriteByte(' ')
		b.WriteString(h.paint.dim(attr.key + "="))
		b.WriteString(attr.value)
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

// message colours the record's message by status when there is one, so a line
// reads as pass or fail before any of the detail is parsed.
func (h *devHandler) message(record slog.Record, status int) string {
	if status == 0 {
		return record.Message
	}
	return h.paint.wrap(statusColor(status), record.Message)
}

func (h *devHandler) levelBadge(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return h.paint.wrap(ansiBold+ansiRed, "ERR")
	case level >= slog.LevelWarn:
		return h.paint.wrap(ansiBold+ansiYellow, "WRN")
	case level >= slog.LevelInfo:
		return h.paint.wrap(ansiBold+ansiBlue, "INF")
	default:
		return h.paint.wrap(ansiDim, "DBG")
	}
}

// render flattens one attr into printable key/value pairs, expanding groups.
func (h *devHandler) render(attr slog.Attr, groups []string) []renderedAttr {
	attr.Value = attr.Value.Resolve()

	if attr.Equal(slog.Attr{}) {
		return nil
	}

	if attr.Value.Kind() == slog.KindGroup {
		nested := attr.Value.Group()
		if len(nested) == 0 {
			return nil
		}
		inner := groups
		if attr.Key != "" {
			inner = append(append([]string{}, groups...), attr.Key)
		}
		out := make([]renderedAttr, 0, len(nested))
		for _, child := range nested {
			out = append(out, h.render(child, inner)...)
		}
		return out
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}

	return []renderedAttr{h.renderScalar(key, attr)}
}

func (h *devHandler) renderScalar(key string, attr slog.Attr) renderedAttr {
	out := renderedAttr{key: key}

	switch key {
	case "status":
		if status, ok := asInt(attr.Value); ok {
			out.status = status
			out.value = h.paint.wrap(ansiBold+statusColor(status), strconv.Itoa(status))
			return out
		}
	case "method":
		out.value = h.paint.bold(attr.Value.String())
		return out
	case "error", "panic":
		out.value = h.paint.wrap(ansiRed, quoteIfNeeded(attr.Value.String()))
		return out
	case "requestId", "stack":
		// Present when needed, but never the thing you are scanning for.
		out.value = h.paint.gray(quoteIfNeeded(attr.Value.String()))
		return out
	}

	out.value = quoteIfNeeded(attr.Value.String())
	return out
}

func asInt(value slog.Value) (int, bool) {
	switch value.Kind() {
	case slog.KindInt64:
		return int(value.Int64()), true
	case slog.KindUint64:
		return int(value.Uint64()), true
	case slog.KindFloat64:
		return int(value.Float64()), true
	case slog.KindAny:
		if n, ok := value.Any().(int); ok {
			return n, true
		}
	}
	return 0, false
}

// quoteIfNeeded keeps whitespace-bearing values from breaking the key=value
// shape, matching what TextHandler would do.
func quoteIfNeeded(text string) string {
	if text == "" {
		return `""`
	}
	if strings.ContainsAny(text, " \t\n\"=") {
		return strconv.Quote(text)
	}
	return text
}

var _ slog.Handler = (*devHandler)(nil)
