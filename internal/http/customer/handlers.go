// Package customer serves /api/v1/me/*: auth required, scoped by the JWT
// subject and nothing else.
//
// A customer belongs to no tenant, so no route here consults
// `x-organization-id`. Ownership is enforced per row: every read and write is
// filtered by the caller's user id inside the service layer.
package customer

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/booking"
	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/tenant"
)

// Handler serves the customer routes.
type Handler struct {
	auth    *auth.Service
	tenant  *tenant.Service
	booking *booking.Service
}

// New builds the customer handler.
func New(authService *auth.Service, tenantService *tenant.Service, bookingService *booking.Service) *Handler {
	return &Handler{auth: authService, tenant: tenantService, booking: bookingService}
}

// Routes returns the /me subtree.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/profile", httpx.H(h.getProfile))
	r.Patch("/profile", httpx.H(h.updateProfile))

	r.Get("/bookings", httpx.H(h.listBookings))
	r.Post("/bookings", httpx.H(h.createBooking))
	r.Get("/bookings/{bookingID}", httpx.H(h.getBooking))
	r.Post("/bookings/{bookingID}/cancel", httpx.H(h.cancelBooking))

	r.Post("/devices", httpx.H(h.registerDevice))

	// Becoming provider staff: either register a business (which makes you its
	// owner, pending platform approval) or accept an invitation to an existing
	// one. Both live here because the caller is not yet a member of any org, so
	// there is no org scope to mount them under.
	r.Post("/providers", httpx.H(h.registerProvider))
	r.Post("/invitations/accept", httpx.H(h.acceptInvitation))

	return r
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	user, memberships, err := h.auth.CurrentSession(r.Context(), identity.UserID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"user":        user,
		"memberships": memberships,
	})
	return nil
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	var in auth.UpdateProfileInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	user, err := h.auth.UpdateProfile(r.Context(), identity.UserID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, user)
	return nil
}

func (h *Handler) listBookings(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	page, err := httpx.ParsePage(r, 20)
	if err != nil {
		return err
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	bookings, total, err := h.booking.ListForCustomer(r.Context(), identity.UserID, status, page)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"bookings": bookings,
		"total":    total,
		"limit":    page.Limit,
		"offset":   page.Offset,
	})
	return nil
}

// createBooking is the endpoint that can return 409 SLOT_TAKEN.
func (h *Handler) createBooking(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	var in booking.CreateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	// A customer books for themselves. The walk-in fields are staff-only and
	// are ignored here rather than trusted from a customer's payload.
	in.CustomerName = nil
	in.CustomerPhone = nil
	in.ResourceID = nil

	userID := identity.UserID
	created, err := h.booking.Create(r.Context(), &userID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) getBooking(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}
	bookingID, err := httpx.URLParamUUID(r, "bookingID")
	if err != nil {
		return err
	}

	found, err := h.booking.GetForCustomer(r.Context(), identity.UserID, bookingID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, found)
	return nil
}

func (h *Handler) cancelBooking(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}
	bookingID, err := httpx.URLParamUUID(r, "bookingID")
	if err != nil {
		return err
	}

	var in booking.TransitionInput
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}

	cancelled, err := h.booking.CancelAsCustomer(r.Context(), identity.UserID, bookingID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, cancelled)
	return nil
}

type registerDeviceRequest struct {
	FCMToken string `json:"fcmToken"`
}

// registerDevice stores the push token milestone 10 will deliver to.
func (h *Handler) registerDevice(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	var in registerDeviceRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	if err := h.auth.RegisterDevice(r.Context(), identity.UserID, in.FCMToken); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// registerProvider lets a signed-in user register their business. The provider
// starts `pending` and the caller becomes its owner; a platform admin decides
// whether it goes live.
func (h *Handler) registerProvider(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	var in tenant.CreateProviderInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	userID := identity.UserID
	created, err := h.tenant.CreateProvider(r.Context(), in, &userID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

type acceptInvitationRequest struct {
	Token string `json:"token"`
}

func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) error {
	identity, err := auth.MustIdentity(r.Context())
	if err != nil {
		return err
	}

	var in acceptInvitationRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	member, err := h.tenant.AcceptInvitation(r.Context(), in.Token, identity.UserID, identity.Email)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, member)
	return nil
}
