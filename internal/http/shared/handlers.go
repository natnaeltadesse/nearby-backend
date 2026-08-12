// Package shared serves /api/v1/auth/*: the routes that belong to no surface
// because they are what a client uses before it has a session, or to inspect
// the one it has.
package shared

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// Handler serves the auth routes.
type Handler struct {
	auth   *auth.Service
	issuer *auth.TokenIssuer
}

// New builds the auth handler.
func New(authService *auth.Service, issuer *auth.TokenIssuer) *Handler {
	return &Handler{auth: authService, issuer: issuer}
}

// Routes returns the /auth subtree.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Post("/sign-up", httpx.H(h.signUp))
	r.Post("/sign-in", httpx.H(h.signIn))
	r.Post("/refresh", httpx.H(h.refresh))
	r.Post("/sign-out", httpx.H(h.signOut))

	// The only authenticated route here: everything else is how you get a token.
	r.With(auth.RequireAuth(h.issuer)).Get("/session", httpx.H(h.session))
	r.With(auth.RequireAuth(h.issuer)).Post("/sign-out-everywhere", httpx.H(h.signOutEverywhere))

	return r
}

func (h *Handler) signUp(w http.ResponseWriter, r *http.Request) error {
	var in auth.SignUpInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.ClientType == "" {
		in.ClientType = clientTypeFromRequest(r)
	}

	session, err := h.auth.SignUp(r.Context(), in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, session)
	return nil
}

func (h *Handler) signIn(w http.ResponseWriter, r *http.Request) error {
	var in auth.SignInInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.ClientType == "" {
		in.ClientType = clientTypeFromRequest(r)
	}

	session, err := h.auth.SignIn(r.Context(), in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, session)
	return nil
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var in refreshRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	session, err := h.auth.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, session)
	return nil
}

func (h *Handler) signOut(w http.ResponseWriter, r *http.Request) error {
	var in refreshRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	if err := h.auth.SignOut(r.Context(), in.RefreshToken); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

func (h *Handler) signOutEverywhere(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}
	if err := h.auth.SignOutEverywhere(r.Context(), identity.UserID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// sessionResponse lets a client rehydrate without spending a refresh token.
type sessionResponse struct {
	User        auth.User         `json:"user"`
	Memberships []auth.Membership `json:"memberships"`
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	// Re-read rather than echoing the token's claims: a membership granted or
	// revoked since the token was issued should show up here immediately.
	user, memberships, err := h.auth.CurrentSession(r.Context(), identity.UserID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, sessionResponse{User: user, Memberships: memberships})
	return nil
}

// clientTypeFromRequest guesses the client when the body does not say.
// Expo sets a recognisable User-Agent; anything else is treated as web, which
// is the shorter-lived and therefore safer default.
func clientTypeFromRequest(r *http.Request) string {
	if r.Header.Get("x-client-type") == auth.ClientMobile {
		return auth.ClientMobile
	}
	return auth.ClientWeb
}
