// Package public serves /api/v1/public/*: no auth required, no org scope.
//
// This is the surface the customer app browses. Everything here reads across
// tenants, which is exactly why it cannot live behind the org middleware.
package public

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/catalog"
	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/scheduling"
	"github.com/nearby/booking-backend/internal/tenant"
)

// Handler serves the public routes.
type Handler struct {
	catalog   *catalog.Catalog
	tenant    *tenant.Service
	scheduler *scheduling.Scheduler
}

// New builds the public handler.
func New(cat *catalog.Catalog, tenantService *tenant.Service, scheduler *scheduling.Scheduler) *Handler {
	return &Handler{catalog: cat, tenant: tenantService, scheduler: scheduler}
}

// Routes returns the /public subtree.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/categories", httpx.H(h.listCategories))
	r.Get("/categories/{categoryID}/attributes", httpx.H(h.listCategoryAttributes))

	// GET /providers (the geo + attribute search) is milestone 8; it is
	// deliberately absent rather than stubbed.
	r.Get("/providers/{slug}", httpx.H(h.getProvider))
	r.Get("/providers/{providerID}/services", httpx.H(h.listProviderServices))

	r.Get("/services/{serviceID}", httpx.H(h.getService))
	r.Get("/services/{serviceID}/availability", httpx.H(h.availability))

	return r
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) error {
	// The public surface only ever shows active categories.
	categories, err := h.catalog.ListCategories(r.Context(), true)
	if err != nil {
		return err
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"categories": categories})
	return nil
}

// listCategoryAttributes drives the dynamic forms on both clients: adding a
// vertical is a data insert, and the apps pick it up without a release.
func (h *Handler) listCategoryAttributes(w http.ResponseWriter, r *http.Request) error {
	categoryID, err := httpx.URLParamUUID(r, "categoryID")
	if err != nil {
		return err
	}

	// ?appliesTo=service|booking|provider narrows the set; absent means all.
	appliesTo := strings.TrimSpace(r.URL.Query().Get("appliesTo"))
	switch appliesTo {
	case "", catalog.AppliesToService, catalog.AppliesToBooking, catalog.AppliesToProvider:
	default:
		return httpx.Validation("appliesTo must be one of: service, booking, provider")
	}

	if _, err := h.catalog.GetCategory(r.Context(), categoryID); err != nil {
		return err
	}

	attributes, err := h.catalog.ListAttributes(r.Context(), categoryID, appliesTo)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"attributes": attributes})
	return nil
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) error {
	slug := chi.URLParam(r, "slug")

	provider, err := h.tenant.GetProviderBySlug(r.Context(), slug)
	if err != nil {
		return err
	}
	// A pending or suspended provider is not public. Reporting it as missing
	// avoids leaking that a business applied and was rejected.
	if provider.Status != tenant.StatusActive {
		return httpx.NotFound("Provider")
	}

	httpx.JSON(w, r, http.StatusOK, provider)
	return nil
}

func (h *Handler) listProviderServices(w http.ResponseWriter, r *http.Request) error {
	providerID, err := httpx.URLParamUUID(r, "providerID")
	if err != nil {
		return err
	}

	provider, err := h.tenant.GetProvider(r.Context(), providerID)
	if err != nil {
		return err
	}
	if provider.Status != tenant.StatusActive {
		return httpx.NotFound("Provider")
	}

	services, err := h.catalog.ListServices(r.Context(), providerID, true)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"services": services})
	return nil
}

// getService returns a service with its option groups, which is what the
// client needs to render the booking form and its add-ons.
func (h *Handler) getService(w http.ResponseWriter, r *http.Request) error {
	serviceID, err := httpx.URLParamUUID(r, "serviceID")
	if err != nil {
		return err
	}

	service, err := h.catalog.GetServiceDetail(r.Context(), serviceID)
	if err != nil {
		return err
	}
	if !service.IsActive {
		return httpx.NotFound("Service")
	}

	provider, err := h.tenant.GetProvider(r.Context(), service.ProviderID)
	if err != nil {
		return err
	}
	if provider.Status != tenant.StatusActive {
		return httpx.NotFound("Service")
	}

	httpx.JSON(w, r, http.StatusOK, service)
	return nil
}

// availability answers GET /services/:id/availability?date=&optionIds=.
//
// The option ids matter: add-ons change the duration, and a 45-minute wash
// with wax needs a 65-minute hole, not a 45-minute one.
func (h *Handler) availability(w http.ResponseWriter, r *http.Request) error {
	serviceID, err := httpx.URLParamUUID(r, "serviceID")
	if err != nil {
		return err
	}

	date, ok, err := httpx.QueryDate(r, "date")
	if err != nil {
		return err
	}
	if !ok {
		return httpx.Validation("date is required, formatted as YYYY-MM-DD")
	}

	optionIDs, err := parseOptionIDs(r.URL.Query().Get("optionIds"))
	if err != nil {
		return err
	}

	slots, err := h.scheduler.Availability(r.Context(), serviceID, date, optionIDs)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, availabilityResponse{
		Date:  date.Format(time.DateOnly),
		Slots: slots,
	})
	return nil
}

type availabilityResponse struct {
	Date  string            `json:"date"`
	Slots []scheduling.Slot `json:"slots"`
}

// parseOptionIDs reads the comma-separated ?optionIds= parameter.
func parseOptionIDs(raw string) ([]uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	ids := make([]uuid.UUID, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, httpx.Validation("optionIds must be a comma-separated list of UUIDs")
		}
		ids = append(ids, id)
	}
	return ids, nil
}
