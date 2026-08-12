// Package admin serves /api/v1/admin/*: auth + platform role == admin.
//
// A platform admin acts on all tenants and belongs to none, so this subtree
// deliberately does not go through the org middleware. Nothing here reads
// `x-organization-id`; scope comes from explicit path and query parameters.
package admin

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/booking"
	"github.com/nearby/booking-backend/internal/catalog"
	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/tenant"
)

// Handler serves the platform-admin routes.
type Handler struct {
	tenant  *tenant.Service
	catalog *catalog.Catalog
	booking *booking.Service
	queries *db.Queries
}

// New builds the admin handler.
func New(
	tenantService *tenant.Service,
	cat *catalog.Catalog,
	bookingService *booking.Service,
	pool *pgxpool.Pool,
) *Handler {
	return &Handler{
		tenant:  tenantService,
		catalog: cat,
		booking: bookingService,
		queries: db.New(pool),
	}
}

// Routes returns the /admin subtree.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/categories", func(r chi.Router) {
		r.Get("/", httpx.H(h.listCategories))
		r.Post("/", httpx.H(h.createCategory))
		r.Get("/{categoryID}", httpx.H(h.getCategory))
		r.Patch("/{categoryID}", httpx.H(h.updateCategory))
		r.Delete("/{categoryID}", httpx.H(h.deleteCategory))

		// Attribute definitions: the mechanism that makes a new vertical a
		// data insert rather than a migration and an app release.
		r.Get("/{categoryID}/attributes", httpx.H(h.listAttributes))
		r.Post("/{categoryID}/attributes", httpx.H(h.createAttribute))
		r.Patch("/{categoryID}/attributes/{attributeID}", httpx.H(h.updateAttribute))
		r.Delete("/{categoryID}/attributes/{attributeID}", httpx.H(h.deleteAttribute))
	})

	r.Route("/providers", func(r chi.Router) {
		r.Get("/", httpx.H(h.listProviders))
		r.Post("/", httpx.H(h.createProvider))
		r.Get("/{providerID}", httpx.H(h.getProvider))
		r.Post("/{providerID}/approve", httpx.H(h.setStatus(tenant.StatusActive)))
		r.Post("/{providerID}/suspend", httpx.H(h.setStatus(tenant.StatusSuspended)))
	})

	r.Get("/bookings", httpx.H(h.listBookings))
	r.Get("/users", httpx.H(h.listUsers))

	return r
}

// --- categories -----------------------------------------------------------

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) error {
	// Admins see inactive categories too — that is the point of the surface.
	categories, err := h.catalog.ListCategories(r.Context(), false)
	if err != nil {
		return err
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"categories": categories})
	return nil
}

func (h *Handler) getCategory(w http.ResponseWriter, r *http.Request) error {
	categoryID, err := httpx.URLParamUUID(r, "categoryID")
	if err != nil {
		return err
	}

	category, err := h.catalog.GetCategory(r.Context(), categoryID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, category)
	return nil
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) error {
	var in catalog.CreateCategoryInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.catalog.CreateCategory(r.Context(), in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) error {
	categoryID, err := httpx.URLParamUUID(r, "categoryID")
	if err != nil {
		return err
	}

	var in catalog.UpdateCategoryInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.catalog.UpdateCategory(r.Context(), categoryID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) error {
	categoryID, err := httpx.URLParamUUID(r, "categoryID")
	if err != nil {
		return err
	}

	if err := h.catalog.DeleteCategory(r.Context(), categoryID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- attributes -----------------------------------------------------------

func (h *Handler) listAttributes(w http.ResponseWriter, r *http.Request) error {
	categoryID, err := httpx.URLParamUUID(r, "categoryID")
	if err != nil {
		return err
	}

	attributes, err := h.catalog.ListAttributes(r.Context(), categoryID, strings.TrimSpace(r.URL.Query().Get("appliesTo")))
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{"attributes": attributes})
	return nil
}

func (h *Handler) createAttribute(w http.ResponseWriter, r *http.Request) error {
	categoryID, err := httpx.URLParamUUID(r, "categoryID")
	if err != nil {
		return err
	}

	var in catalog.CreateAttributeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.catalog.CreateAttribute(r.Context(), categoryID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

func (h *Handler) updateAttribute(w http.ResponseWriter, r *http.Request) error {
	attributeID, err := httpx.URLParamUUID(r, "attributeID")
	if err != nil {
		return err
	}

	var in catalog.UpdateAttributeInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	updated, err := h.catalog.UpdateAttribute(r.Context(), attributeID, in)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, updated)
	return nil
}

func (h *Handler) deleteAttribute(w http.ResponseWriter, r *http.Request) error {
	attributeID, err := httpx.URLParamUUID(r, "attributeID")
	if err != nil {
		return err
	}

	if err := h.catalog.DeleteAttribute(r.Context(), attributeID); err != nil {
		return err
	}

	httpx.NoContent(w)
	return nil
}

// --- providers ------------------------------------------------------------

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 25)
	if err != nil {
		return err
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "", tenant.StatusPending, tenant.StatusActive, tenant.StatusSuspended:
	default:
		return httpx.Validation("status must be one of: pending, active, suspended")
	}

	providers, total, err := h.tenant.ListProviders(r.Context(), tenant.ListProvidersInput{
		Status: status,
		Search: strings.TrimSpace(r.URL.Query().Get("search")),
		Limit:  page.Limit,
		Offset: page.Offset,
	})
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"providers": providers,
		"total":     total,
		"limit":     page.Limit,
		"offset":    page.Offset,
	})
	return nil
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) error {
	providerID, err := httpx.URLParamUUID(r, "providerID")
	if err != nil {
		return err
	}

	provider, err := h.tenant.GetProvider(r.Context(), providerID)
	if err != nil {
		return err
	}

	members, err := h.tenant.ListMembers(r.Context(), providerID)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"provider": provider,
		"members":  members,
	})
	return nil
}

// createProvider onboards a tenant directly, for businesses signed up offline.
// Self-registration by a prospective owner lives at POST /me/providers.
func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) error {
	var in tenant.CreateProviderInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}

	created, err := h.tenant.CreateProvider(r.Context(), in, nil)
	if err != nil {
		return err
	}

	httpx.JSON(w, r, http.StatusCreated, created)
	return nil
}

// setStatus builds the approve and suspend handlers, which differ only by the
// status they move the provider to.
func (h *Handler) setStatus(status string) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		providerID, err := httpx.URLParamUUID(r, "providerID")
		if err != nil {
			return err
		}

		updated, err := h.tenant.SetStatus(r.Context(), providerID, status)
		if err != nil {
			return err
		}

		httpx.JSON(w, r, http.StatusOK, updated)
		return nil
	}
}

// --- bookings and users ---------------------------------------------------

func (h *Handler) listBookings(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 25)
	if err != nil {
		return err
	}

	var providerID *uuid.UUID
	if id, ok, err := httpx.QueryUUID(r, "providerId"); err != nil {
		return err
	} else if ok {
		providerID = &id
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))

	rows, err := h.queries.ListAllBookings(r.Context(), db.ListAllBookingsParams{
		Status:       status,
		ProviderID:   providerID,
		ResultLimit:  page.Limit,
		ResultOffset: page.Offset,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	total, err := h.queries.CountAllBookings(r.Context(), db.CountAllBookingsParams{
		Status:     status,
		ProviderID: providerID,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	bookings := make([]adminBooking, 0, len(rows))
	for _, row := range rows {
		bookings = append(bookings, adminBooking{
			ID: row.ID, Code: row.Code, Status: row.Status,
			ProviderID: row.ProviderID, ProviderSlug: row.ProviderSlug, ProviderName: row.ProviderName,
			ServiceName: row.ServiceName, PriceCents: row.PriceCents, Currency: row.Currency,
			DurationMinutes: row.DurationMinutes,
			StartsAt:        row.StartsAt.UTC(), EndsAt: row.EndsAt.UTC(),
			CustomerName: row.CustomerUserName, CustomerEmail: row.CustomerUserEmail,
			CreatedAt: row.CreatedAt,
		})
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"bookings": bookings,
		"total":    total,
		"limit":    page.Limit,
		"offset":   page.Offset,
	})
	return nil
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 25)
	if err != nil {
		return err
	}

	search := strings.TrimSpace(r.URL.Query().Get("search"))

	rows, err := h.queries.ListUsers(r.Context(), db.ListUsersParams{
		Search:       search,
		ResultLimit:  page.Limit,
		ResultOffset: page.Offset,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	total, err := h.queries.CountUsers(r.Context(), search)
	if err != nil {
		return httpx.Internal(err)
	}

	users := make([]adminUser, 0, len(rows))
	for _, row := range rows {
		users = append(users, adminUser{
			ID: row.ID, Name: row.Name, Email: row.Email, Phone: row.Phone,
			PlatformRole: row.PlatformRole, EmailVerified: row.EmailVerified,
			CreatedAt: row.CreatedAt,
		})
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"users":  users,
		"total":  total,
		"limit":  page.Limit,
		"offset": page.Offset,
	})
	return nil
}
