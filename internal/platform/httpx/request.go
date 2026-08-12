package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// Decode reads a JSON body into dst, rejecting unknown fields so a typo in a
// client payload surfaces as 422 rather than being silently dropped.
func Decode(r *http.Request, dst any) error {
	if r.Body == nil {
		return Validation("A request body is required")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	// A second value in the body means the client sent something we did not ask for.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Validation("Request body must contain a single JSON object")
	}
	return nil
}

func decodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		return Validation(fmt.Sprintf("Malformed JSON at position %d", syntaxErr.Offset))
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return Validation(fmt.Sprintf("Field %q must be a %s", typeErr.Field, typeErr.Type))
		}
		return Validation("Malformed JSON: wrong type")
	case errors.Is(err, io.EOF):
		return Validation("A request body is required")
	case errors.As(err, &maxBytesErr):
		return Validation("Request body is too large")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return Validation(fmt.Sprintf("Unknown field %s", field))
	default:
		return Validation("Malformed JSON")
	}
}

// URLParamUUID reads a chi path parameter as a UUID.
func URLParamUUID(r *http.Request, name string) (uuid.UUID, error) {
	raw := chi.URLParam(r, name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, Validation(fmt.Sprintf("%s must be a UUID", name))
	}
	return id, nil
}

// QueryUUID reads an optional UUID query parameter. ok is false when absent.
func QueryUUID(r *http.Request, name string) (id uuid.UUID, ok bool, err error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return uuid.Nil, false, nil
	}
	id, parseErr := uuid.Parse(raw)
	if parseErr != nil {
		return uuid.Nil, false, Validation(fmt.Sprintf("%s must be a UUID", name))
	}
	return id, true, nil
}

// QueryDate reads a required-format YYYY-MM-DD query parameter.
func QueryDate(r *http.Request, name string) (d time.Time, ok bool, err error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return time.Time{}, false, nil
	}
	parsed, parseErr := time.Parse(time.DateOnly, raw)
	if parseErr != nil {
		return time.Time{}, false, Validation(fmt.Sprintf("%s must be a date as YYYY-MM-DD", name))
	}
	return parsed, true, nil
}

// Page is a limit/offset window with sane bounds already applied.
type Page struct {
	Limit  int32
	Offset int32
}

// ParsePage reads ?limit=&offset=, clamping limit to [1, 100].
func ParsePage(r *http.Request, defaultLimit int) (Page, error) {
	q := r.URL.Query()
	limit := defaultLimit
	offset := 0

	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return Page{}, Validation("limit must be a positive integer")
		}
		limit = min(parsed, 100)
	}
	if raw := q.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return Page{}, Validation("offset must be zero or a positive integer")
		}
		offset = parsed
	}
	return Page{Limit: int32(limit), Offset: int32(offset)}, nil
}
