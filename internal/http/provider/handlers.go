// Package provider serves /api/v1/org/*: auth + `x-organization-id` + a
// database membership check, all applied by the middleware this subtree is
// mounted behind.
//
// Every handler reads its provider id from the verified org scope in the
// context, never from the request. There is deliberately no code path here
// that accepts a provider id from a body or a path parameter.
package provider

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/booking"
	"github.com/nearby/booking-backend/internal/catalog"
	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/scheduling"
	"github.com/nearby/booking-backend/internal/tenant"
)

// Handler serves the org routes.
type Handler struct {
	tenant    *tenant.Service
	catalog   *catalog.Catalog
	scheduler *scheduling.Scheduler
	booking   *booking.Service
}

// New builds the org handler.
func New(
	tenantService *tenant.Service,
	cat *catalog.Catalog,
	scheduler *scheduling.Scheduler,
	bookingService *booking.Service,
) *Handler {
	return &Handler{tenant: tenantService, catalog: cat, scheduler: scheduler, booking: bookingService}
}

// Routes returns the /org subtree.
//
// Staff can work the bookings inbox; changing the catalog, the schedule or the
// team needs admin or owner. That split is why RequireOrgRole appears on some
// routes and not others.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	manage := tenant.RequireOrgRole(tenant.RoleAdmin)
	own := tenant.RequireOrgRole(tenant.RoleOwner)

	r.Get("/profile", httpx.H(h.getProfile))
	r.With(manage).Patch("/profile", httpx.H(h.updateProfile))

	r.Route("/services", func(r chi.Router) {
		r.Get("/", httpx.H(h.listServices))
		r.With(manage).Post("/", httpx.H(h.createService))
		r.Get("/{serviceID}", httpx.H(h.getService))
		r.With(manage).Patch("/{serviceID}", httpx.H(h.updateService))
		r.With(manage).Delete("/{serviceID}", httpx.H(h.deleteService))

		r.Route("/{serviceID}/option-groups", func(r chi.Router) {
			r.Get("/", httpx.H(h.listOptionGroups))
			r.With(manage).Post("/", httpx.H(h.createOptionGroup))
			r.With(manage).Patch("/{groupID}", httpx.H(h.updateOptionGroup))
			r.With(manage).Delete("/{groupID}", httpx.H(h.deleteOptionGroup))

			r.With(manage).Post("/{groupID}/options", httpx.H(h.createOption))
			r.With(manage).Patch("/{groupID}/options/{optionID}", httpx.H(h.updateOption))
			r.With(manage).Delete("/{groupID}/options/{optionID}", httpx.H(h.deleteOption))
		})
	})

	r.Route("/resources", func(r chi.Router) {
		r.Get("/", httpx.H(h.listResources))
		r.With(manage).Post("/", httpx.H(h.createResource))
		r.With(manage).Patch("/{resourceID}", httpx.H(h.updateResource))
		r.With(manage).Delete("/{resourceID}", httpx.H(h.deleteResource))
		r.With(manage).Put("/{resourceID}/services/{serviceID}", httpx.H(h.attachService))
		r.With(manage).Delete("/{resourceID}/services/{serviceID}", httpx.H(h.detachService))
	})

	r.Route("/business-hours", func(r chi.Router) {
		r.Get("/", httpx.H(h.listHours))
		r.With(manage).Post("/", httpx.H(h.createHours))
		r.With(manage).Patch("/{hoursID}", httpx.H(h.updateHours))
		r.With(manage).Delete("/{hoursID}", httpx.H(h.deleteHours))
	})

	r.Route("/schedule-exceptions", func(r chi.Router) {
		r.Get("/", httpx.H(h.listExceptions))
		r.With(manage).Post("/", httpx.H(h.createException))
		r.With(manage).Delete("/{exceptionID}", httpx.H(h.deleteException))
	})

	r.Route("/bookings", func(r chi.Router) {
		r.Get("/", httpx.H(h.listBookings))
		// Staff enter walk-ins, so this is not gated on `manage`.
		r.Post("/", httpx.H(h.createWalkIn))
		r.Get("/{bookingID}", httpx.H(h.getBooking))
		r.Post("/{bookingID}/confirm", httpx.H(h.transition(booking.EventConfirm)))
		r.Post("/{bookingID}/start", httpx.H(h.transition(booking.EventStart)))
		r.Post("/{bookingID}/complete", httpx.H(h.transition(booking.EventComplete)))
		r.Post("/{bookingID}/no-show", httpx.H(h.transition(booking.EventNoShow)))
		r.Post("/{bookingID}/cancel", httpx.H(h.transition(booking.EventCancelByProvider)))
	})

	r.Route("/members", func(r chi.Router) {
		r.Get("/", httpx.H(h.listMembers))
		r.With(own).Patch("/{userID}", httpx.H(h.updateMemberRole))
		r.With(own).Delete("/{userID}", httpx.H(h.removeMember))
	})

	r.Route("/invitations", func(r chi.Router) {
		r.With(manage).Get("/", httpx.H(h.listInvitations))
		r.With(manage).Post("/", httpx.H(h.createInvitation))
		r.With(manage).Delete("/{invitationID}", httpx.H(h.revokeInvitation))
	})

	r.Get("/stats/summary", httpx.H(h.stats))

	return r
}

// --- profile --------------------------------------------------------------

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	provider, err := h.tenant.GetProvider(r.Context(), org.ProviderID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"provider": provider,
		"role":     org.Role,
	})
	return nil
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in tenant.UpdateProviderInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.tenant.UpdateProvider(r.Context(), org.ProviderID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

// --- services -------------------------------------------------------------

func (h *Handler) listServices(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	// Staff see inactive services too; the public surface does not.
	services, err := h.catalog.ListServices(r.Context(), org.ProviderID, false)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"services": services})
	return nil
}

func (h *Handler) createService(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in catalog.CreateServiceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.catalog.CreateService(r.Context(), org.ProviderID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) getService(w http.ResponseWriter, r *http.Request) error {
	org, serviceID, err := h.orgAndParam(r, "serviceID")
	if err != nil {
		return err
	}

	service, err := h.catalog.GetServiceDetail(r.Context(), serviceID)
	if err != nil {
		return err
	}
	if service.ProviderID != org.ProviderID {
		return httpx.NotFound("Service")
	}

	httpx.JSON(w, r, http.StatusOK, service)
	return nil
}

func (h *Handler) updateService(w http.ResponseWriter, r *http.Request) error {
	org, serviceID, err := h.orgAndParam(r, "serviceID")
	if err != nil {
		return err
	}

	var in catalog.UpdateServiceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.catalog.UpdateService(r.Context(), org.ProviderID, serviceID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteService(w http.ResponseWriter, r *http.Request) error {
	org, serviceID, err := h.orgAndParam(r, "serviceID")
	if err != nil {
		return err
	}

	if err := h.catalog.DeleteService(r.Context(), org.ProviderID, serviceID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- option groups --------------------------------------------------------

func (h *Handler) listOptionGroups(w http.ResponseWriter, r *http.Request) error {
	org, serviceID, err := h.orgAndParam(r, "serviceID")
	if err != nil {
		return err
	}

	service, err := h.catalog.GetService(r.Context(), serviceID)
	if err != nil {
		return err
	}
	if service.ProviderID != org.ProviderID {
		return httpx.NotFound("Service")
	}

	groups, err := h.catalog.LoadOptionGroups(r.Context(), serviceID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"optionGroups": groups})
	return nil
}

func (h *Handler) createOptionGroup(w http.ResponseWriter, r *http.Request) error {
	org, serviceID, err := h.orgAndParam(r, "serviceID")
	if err != nil {
		return err
	}

	var in catalog.CreateOptionGroupInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.catalog.CreateOptionGroup(r.Context(), org.ProviderID, serviceID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) updateOptionGroup(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "serviceID", "groupID")
	if err != nil {
		return err
	}

	var in catalog.UpdateOptionGroupInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.catalog.UpdateOptionGroup(r.Context(), org.ProviderID, ids[0], ids[1], in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteOptionGroup(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "serviceID", "groupID")
	if err != nil {
		return err
	}

	if err := h.catalog.DeleteOptionGroup(r.Context(), org.ProviderID, ids[0], ids[1]); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

func (h *Handler) createOption(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "serviceID", "groupID")
	if err != nil {
		return err
	}

	var in catalog.CreateOptionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.catalog.CreateOption(r.Context(), org.ProviderID, ids[0], ids[1], in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) updateOption(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "serviceID", "groupID", "optionID")
	if err != nil {
		return err
	}

	var in catalog.UpdateOptionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.catalog.UpdateOption(r.Context(), org.ProviderID, ids[0], ids[1], ids[2], in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteOption(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "serviceID", "groupID", "optionID")
	if err != nil {
		return err
	}

	if err := h.catalog.DeleteOption(r.Context(), org.ProviderID, ids[0], ids[1], ids[2]); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- resources ------------------------------------------------------------

func (h *Handler) listResources(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	resources, err := h.scheduler.ListResources(r.Context(), org.ProviderID, false)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"resources": resources})
	return nil
}

func (h *Handler) createResource(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in scheduling.CreateResourceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.scheduler.CreateResource(r.Context(), org.ProviderID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) updateResource(w http.ResponseWriter, r *http.Request) error {
	org, resourceID, err := h.orgAndParam(r, "resourceID")
	if err != nil {
		return err
	}

	var in scheduling.UpdateResourceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.scheduler.UpdateResource(r.Context(), org.ProviderID, resourceID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteResource(w http.ResponseWriter, r *http.Request) error {
	org, resourceID, err := h.orgAndParam(r, "resourceID")
	if err != nil {
		return err
	}

	if err := h.scheduler.DeleteResource(r.Context(), org.ProviderID, resourceID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

func (h *Handler) attachService(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "resourceID", "serviceID")
	if err != nil {
		return err
	}

	if err := h.scheduler.AttachService(r.Context(), org.ProviderID, ids[0], ids[1]); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

func (h *Handler) detachService(w http.ResponseWriter, r *http.Request) error {
	org, ids, err := h.orgAndParams(r, "resourceID", "serviceID")
	if err != nil {
		return err
	}

	if err := h.scheduler.DetachService(r.Context(), org.ProviderID, ids[0], ids[1]); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- hours and exceptions -------------------------------------------------

func (h *Handler) listHours(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	hours, err := h.scheduler.ListHours(r.Context(), org.ProviderID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"businessHours": hours})
	return nil
}

func (h *Handler) createHours(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in scheduling.HoursInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.scheduler.CreateHours(r.Context(), org.ProviderID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) updateHours(w http.ResponseWriter, r *http.Request) error {
	org, hoursID, err := h.orgAndParam(r, "hoursID")
	if err != nil {
		return err
	}

	var in scheduling.UpdateHoursInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.scheduler.UpdateHours(r.Context(), org.ProviderID, hoursID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteHours(w http.ResponseWriter, r *http.Request) error {
	org, hoursID, err := h.orgAndParam(r, "hoursID")
	if err != nil {
		return err
	}

	if err := h.scheduler.DeleteHours(r.Context(), org.ProviderID, hoursID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

func (h *Handler) listExceptions(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	from, hasFrom, err := httpx.QueryDate(r, "from")
	if err != nil {
		return err
	}
	to, hasTo, err := httpx.QueryDate(r, "to")
	if err != nil {
		return err
	}

	var fromPtr, toPtr *time.Time
	if hasFrom {
		fromPtr = &from
	}
	if hasTo {
		toPtr = &to
	}

	exceptions, err := h.scheduler.ListExceptions(r.Context(), org.ProviderID, fromPtr, toPtr)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"scheduleExceptions": exceptions})
	return nil
}

func (h *Handler) createException(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in scheduling.ExceptionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.scheduler.CreateException(r.Context(), org.ProviderID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) deleteException(w http.ResponseWriter, r *http.Request) error {
	org, exceptionID, err := h.orgAndParam(r, "exceptionID")
	if err != nil {
		return err
	}

	if err := h.scheduler.DeleteException(r.Context(), org.ProviderID, exceptionID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- bookings -------------------------------------------------------------

func (h *Handler) listBookings(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	page, err := httpx.ParsePage(r, 50)
	if err != nil {
		return err
	}

	in := booking.ListForProviderInput{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Page:   page,
	}

	if resourceID, ok, err := httpx.QueryUUID(r, "resourceId"); err != nil {
		return err
	} else if ok {
		in.ResourceID = &resourceID
	}

	// ?date= narrows to a single local day at the provider's timezone, which
	// is what the inbox actually wants.
	if date, ok, err := httpx.QueryDate(r, "date"); err != nil {
		return err
	} else if ok {
		provider, err := h.tenant.GetProvider(r.Context(), org.ProviderID)
		if err != nil {
			return err
		}
		location, err := time.LoadLocation(provider.Timezone)
		if err != nil {
			location = time.UTC
		}
		year, month, day := date.Date()
		start := time.Date(year, month, day, 0, 0, 0, 0, location)
		end := start.AddDate(0, 0, 1)
		in.From, in.To = &start, &end
	}

	bookings, total, err := h.booking.ListForProvider(r.Context(), org.ProviderID, in)
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

// createWalkIn is a booking entered by staff on behalf of someone standing at
// the counter. It has no customer user id — just a name and phone — which is
// why `bookings.customer_id` is nullable.
func (h *Handler) createWalkIn(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in booking.CreateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.booking.Create(r.Context(), nil, in)
	if err != nil {
		return err
	}
	if created.ProviderID != org.ProviderID {
		// The service belongs to another tenant. Reject rather than let one
		// org write a booking into another's calendar.
		return httpx.Forbidden("That service belongs to a different organization")
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) getBooking(w http.ResponseWriter, r *http.Request) error {
	org, bookingID, err := h.orgAndParam(r, "bookingID")
	if err != nil {
		return err
	}

	found, err := h.booking.GetForProvider(r.Context(), org.ProviderID, bookingID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, found)
	return nil
}

// transition builds the handler for one lifecycle event. The confirm / start /
// complete / no-show / cancel routes differ only by which event they apply, so
// they share one implementation and one ownership check.
func (h *Handler) transition(event booking.Event) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		org, bookingID, err := h.orgAndParam(r, "bookingID")
		if err != nil {
			return err
		}

		// Prove the booking is this org's before touching its state.
		if _, err := h.booking.GetForProvider(r.Context(), org.ProviderID, bookingID); err != nil {
			return err
		}

		var in booking.TransitionInput
		if r.ContentLength > 0 {
			if err := httpx.Decode(r, &in); err != nil {
				return err
			}
		}

		updated, err := h.booking.Transition(r.Context(), bookingID, event, in)
		if err != nil {
			return err
		}

		httpx.JSON(w, r, http.StatusOK, updated)
		return nil
	}
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	from, hasFrom, err := httpx.QueryDate(r, "from")
	if err != nil {
		return err
	}
	to, hasTo, err := httpx.QueryDate(r, "to")
	if err != nil {
		return err
	}

	var fromPtr, toPtr *time.Time
	if hasFrom {
		fromPtr = &from
	}
	if hasTo {
		end := to.AddDate(0, 0, 1) // inclusive of the end date
		toPtr = &end
	}

	stats, err := h.booking.Stats(r.Context(), org.ProviderID, fromPtr, toPtr)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, stats)
	return nil
}

// --- members and invitations ----------------------------------------------

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	members, err := h.tenant.ListMembers(r.Context(), org.ProviderID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"members": members})
	return nil
}

type updateMemberRequest struct {
	Role string `json:"role"`
}

func (h *Handler) updateMemberRole(w http.ResponseWriter, r *http.Request) error {
	org, userID, err := h.orgAndParam(r, "userID")
	if err != nil {
		return err
	}

	var in updateMemberRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	member, err := h.tenant.UpdateMemberRole(r.Context(), org.ProviderID, userID, in.Role)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, member)
	return nil
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) error {
	org, userID, err := h.orgAndParam(r, "userID")
	if err != nil {
		return err
	}

	if err := h.tenant.RemoveMember(r.Context(), org.ProviderID, userID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

type createInvitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *Handler) createInvitation(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	var in createInvitationRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	// The response carries the plaintext token exactly once — only its hash is
	// stored, so it cannot be retrieved again.
	invitation, err := h.tenant.CreateInvitation(r.Context(), org.ProviderID, in.Email, in.Role)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, invitation)
	return nil
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) error {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return err
	}

	invitations, err := h.tenant.ListInvitations(r.Context(), org.ProviderID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"invitations": invitations})
	return nil
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) error {
	org, invitationID, err := h.orgAndParam(r, "invitationID")
	if err != nil {
		return err
	}

	if err := h.tenant.RevokeInvitation(r.Context(), org.ProviderID, invitationID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- helpers --------------------------------------------------------------

func (h *Handler) orgAndParam(r *http.Request, name string) (tenant.Org, uuid.UUID, error) {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return tenant.Org{}, uuid.Nil, err
	}
	id, err := httpx.URLParamUUID(r, name)
	if err != nil {
		return tenant.Org{}, uuid.Nil, err
	}
	return org, id, nil
}

func (h *Handler) orgAndParams(r *http.Request, names ...string) (tenant.Org, []uuid.UUID, error) {
	org, err := tenant.MustOrg(r.Context())
	if err != nil {
		return tenant.Org{}, nil, err
	}

	ids := make([]uuid.UUID, 0, len(names))
	for _, name := range names {
		id, err := httpx.URLParamUUID(r, name)
		if err != nil {
			return tenant.Org{}, nil, err
		}
		ids = append(ids, id)
	}
	return org, ids, nil
}
