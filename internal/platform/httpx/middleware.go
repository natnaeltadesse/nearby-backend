package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/nearby/booking-backend/internal/platform/logging"
)

// RequestLogger attaches a request-scoped logger to the context and logs one
// line per completed request.
func RequestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := middleware.GetReqID(r.Context())

			logger := base.With(slog.String("requestId", reqID))
			ctx := logging.WithLogger(r.Context(), logger)

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(ctx))

			logger.Info("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("took", time.Since(start)),
			)
		})
	}
}

// Recoverer turns a panic into a 500 in the standard envelope instead of a
// dropped connection.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// http.ErrAbortHandler is how a handler says "drop this
				// connection on purpose"; swallowing it would turn a
				// deliberate abort into a 500.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				logging.FromContext(r.Context()).Error("panic recovered",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				WriteError(w, r, Internal(nil))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// NotFoundHandler renders unmatched routes in the standard envelope.
func NotFoundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, NotFound("Route"))
	}
}

// MethodNotAllowedHandler renders 405 in the standard envelope.
func MethodNotAllowedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, &Error{
			Status:  http.StatusMethodNotAllowed,
			Code:    CodeNotFound,
			Message: "That method is not allowed on this route",
		})
	}
}
