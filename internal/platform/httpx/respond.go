package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nearby/booking-backend/internal/platform/logging"
)

// envelope is the error shape every client's interceptor expects (spec §7).
type envelope struct {
	Message string            `json:"message"`
	Code    string            `json:"code"`
	Details map[string]string `json:"details,omitempty"`
}

// JSON writes v as the response body.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil || status == http.StatusNoContent {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already out; all that is left is to record it.
		logging.FromContext(r.Context()).Error("write response body", slog.Any("error", err))
	}
}

// NoContent ends a request with 204.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// WriteError renders err as the error envelope. 5xx causes are logged with
// their internal detail and never leak into the response.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := AsError(err)

	if apiErr.Status >= http.StatusInternalServerError {
		logging.FromContext(r.Context()).Error("request failed",
			slog.String("code", apiErr.Code),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Any("error", apiErr.Error()),
		)
	} else {
		logging.FromContext(r.Context()).Debug("request rejected",
			slog.String("code", apiErr.Code),
			slog.Int("status", apiErr.Status),
			slog.String("path", r.URL.Path),
		)
	}

	JSON(w, r, apiErr.Status, envelope{
		Message: apiErr.Message,
		Code:    apiErr.Code,
		Details: apiErr.Details,
	})
}

// Handler is a http.HandlerFunc that may fail. Wrapping handlers in H keeps
// the `if err != nil { render; return }` boilerplate out of every one of them.
type Handler func(http.ResponseWriter, *http.Request) error

// H adapts a Handler into a http.HandlerFunc.
func H(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}
