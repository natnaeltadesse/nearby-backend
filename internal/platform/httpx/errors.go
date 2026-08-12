// Package httpx holds the HTTP plumbing shared by every router group: the
// error envelope, JSON encode/decode helpers and generic middleware.
package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// Error codes. The mobile and web clients switch on these strings, so treat
// them as part of the API contract: additive only (see spec §13).
const (
	CodeInvalidCredentials = "INVALID_CREDENTIALS"
	CodeEmailTaken         = "EMAIL_TAKEN"
	CodeUnauthenticated    = "UNAUTHENTICATED"
	CodeForbidden          = "FORBIDDEN"
	CodeNotAMember         = "NOT_A_MEMBER"
	CodeTokenExpired       = "TOKEN_EXPIRED"
	CodeInvalidToken       = "INVALID_TOKEN"
	CodeValidationError    = "VALIDATION_ERROR"
	CodeOrgRequired        = "ORG_REQUIRED"
	CodeNotFound           = "NOT_FOUND"
	CodeSlotTaken          = "SLOT_TAKEN"
	CodeOutsideHours       = "OUTSIDE_HOURS"
	CodeInvalidTransition  = "INVALID_TRANSITION"
	CodeServiceInactive    = "SERVICE_INACTIVE"
	CodeProviderInactive   = "PROVIDER_INACTIVE"
	CodeConflict           = "CONFLICT"
	CodeInternal           = "INTERNAL_ERROR"
)

// Error is an API error carrying everything needed to render the envelope.
// Handlers return it; WriteError renders it.
type Error struct {
	Status  int
	Code    string
	Message string
	// Details is optional per-field context for VALIDATION_ERROR. It is an
	// additive extra: clients that only read {message, code} are unaffected.
	Details map[string]string
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches an internal error for logging. It is never serialized.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

// WithDetails attaches per-field validation context.
func (e *Error) WithDetails(details map[string]string) *Error {
	e.Details = details
	return e
}

func newError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// --- constructors, one per situation the API actually produces ---

// Unauthenticated reports a missing or unusable credential.
func Unauthenticated(message string) *Error {
	return newError(http.StatusUnauthorized, CodeUnauthenticated, message)
}

// InvalidCredentials reports a failed sign-in.
func InvalidCredentials() *Error {
	// Deliberately does not distinguish "no such email" from "wrong password".
	return newError(http.StatusUnauthorized, CodeInvalidCredentials, "Incorrect email or password")
}

// TokenExpired tells a client to refresh rather than to sign in again.
func TokenExpired() *Error {
	return newError(http.StatusUnauthorized, CodeTokenExpired, "Your session has expired")
}

// InvalidToken reports a credential that is malformed, forged or revoked.
func InvalidToken() *Error {
	return newError(http.StatusUnauthorized, CodeInvalidToken, "Invalid token")
}

// Forbidden reports an authenticated caller who may not do this.
func Forbidden(message string) *Error {
	return newError(http.StatusForbidden, CodeForbidden, message)
}

// NotAMember reports a caller who does not belong to the requested org.
func NotAMember() *Error {
	return newError(http.StatusForbidden, CodeNotAMember, "You are not a member of this organization")
}

// OrgRequired reports a missing x-organization-id header on an /org route.
func OrgRequired() *Error {
	return newError(http.StatusBadRequest, CodeOrgRequired, "x-organization-id header is required")
}

// EmailTaken reports a duplicate sign-up.
func EmailTaken() *Error {
	return newError(http.StatusConflict, CodeEmailTaken, "That email is already registered")
}

// NotFound reports a missing resource. It is also the correct answer for a
// resource that exists but belongs to someone else: confirming existence is
// itself a leak.
func NotFound(what string) *Error {
	return newError(http.StatusNotFound, CodeNotFound, fmt.Sprintf("%s not found", what))
}

// Validation reports a well-formed request the server will not accept.
func Validation(message string) *Error {
	return newError(http.StatusUnprocessableEntity, CodeValidationError, message)
}

// SlotTaken reports that every eligible resource was reserved first.
func SlotTaken() *Error {
	return newError(http.StatusConflict, CodeSlotTaken, "That slot was just taken")
}

// OutsideHours reports a start that is not on the bookable grid: closed,
// inside the lead time, or off the slot step.
func OutsideHours() *Error {
	return newError(http.StatusUnprocessableEntity, CodeOutsideHours, "That time is outside the provider's opening hours")
}

// InvalidTransition reports a lifecycle event the booking's status forbids.
func InvalidTransition(from, to string) *Error {
	return newError(http.StatusConflict, CodeInvalidTransition,
		fmt.Sprintf("A %s booking cannot become %s", from, to))
}

// ServiceInactive reports a service that is not currently bookable.
func ServiceInactive() *Error {
	return newError(http.StatusUnprocessableEntity, CodeServiceInactive, "That service is not currently bookable")
}

// ProviderInactive reports a provider that is not accepting bookings.
func ProviderInactive() *Error {
	return newError(http.StatusUnprocessableEntity, CodeProviderInactive, "That provider is not currently accepting bookings")
}

// Conflict reports a request that collides with existing state.
func Conflict(message string) *Error {
	return newError(http.StatusConflict, CodeConflict, message)
}

// Internal wraps an unexpected failure. The cause is logged, never sent.
func Internal(cause error) *Error {
	return newError(http.StatusInternalServerError, CodeInternal,
		"Something went wrong on our end").WithCause(cause)
}

// AsError extracts an *Error from err, or wraps it as an internal one.
func AsError(err error) *Error {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return Internal(err)
}
