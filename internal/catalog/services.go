package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// CreateServiceInput describes a new bookable service.
type CreateServiceInput struct {
	CategoryID      uuid.UUID      `json:"categoryId"`
	Name            string         `json:"name"`
	Description     *string        `json:"description"`
	PriceCents      int32          `json:"priceCents"`
	Currency        *string        `json:"currency"`
	DurationMinutes int32          `json:"durationMinutes"`
	BufferMinutes   *int32         `json:"bufferMinutes"`
	Attributes      map[string]any `json:"attributes"`
	IsActive        *bool          `json:"isActive"`
}

// CreateService adds a service to a provider, validating its attributes
// against the chosen category's definitions first.
func (c *Catalog) CreateService(ctx context.Context, providerID uuid.UUID, in CreateServiceInput) (*Service, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Validation("Name is required")
	}
	if in.PriceCents < 0 {
		return nil, httpx.Validation("priceCents cannot be negative")
	}
	if in.DurationMinutes <= 0 {
		return nil, httpx.Validation("durationMinutes must be greater than zero")
	}
	if in.BufferMinutes != nil && *in.BufferMinutes < 0 {
		return nil, httpx.Validation("bufferMinutes cannot be negative")
	}

	attributes, err := c.validateServiceAttributes(ctx, in.CategoryID, in.Attributes)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeAttributes(attributes)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	currency := "ETB"
	if in.Currency != nil && strings.TrimSpace(*in.Currency) != "" {
		currency = strings.ToUpper(strings.TrimSpace(*in.Currency))
	}

	row, err := c.queries.CreateService(ctx, db.CreateServiceParams{
		ProviderID:      providerID,
		CategoryID:      in.CategoryID,
		Name:            name,
		Description:     trimPtr(in.Description),
		PriceCents:      in.PriceCents,
		Currency:        currency,
		DurationMinutes: in.DurationMinutes,
		BufferMinutes:   valueOr(in.BufferMinutes, 0),
		Attributes:      encoded,
		IsActive:        valueOr(in.IsActive, true),
	})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return nil, httpx.Validation("categoryId does not refer to an existing category")
		}
		return nil, httpx.Internal(err)
	}

	return serviceFromRow(row)
}

// GetService reads one service without its option tree.
func (c *Catalog) GetService(ctx context.Context, id uuid.UUID) (*Service, error) {
	row, err := c.queries.GetService(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Service")
		}
		return nil, httpx.Internal(err)
	}
	return serviceFromRow(row)
}

// GetServiceDetail reads a service together with its option groups — the shape
// GET /public/services/:id returns.
func (c *Catalog) GetServiceDetail(ctx context.Context, id uuid.UUID) (*Service, error) {
	service, err := c.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	groups, err := c.LoadOptionGroups(ctx, id)
	if err != nil {
		return nil, err
	}
	service.OptionGroups = groups
	return service, nil
}

// GetServiceWithProvider reads a service plus the provider facts that booking
// and availability need, in one round trip.
func (c *Catalog) GetServiceWithProvider(ctx context.Context, id uuid.UUID) (*ServiceWithProvider, error) {
	row, err := c.queries.GetServiceWithProvider(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Service")
		}
		return nil, httpx.Internal(err)
	}

	attributes, err := decodeAttributes(row.Attributes)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	return &ServiceWithProvider{
		Service: Service{
			ID:              row.ID,
			ProviderID:      row.ProviderID,
			CategoryID:      row.CategoryID,
			Name:            row.Name,
			Description:     row.Description,
			PriceCents:      row.PriceCents,
			Currency:        row.Currency,
			DurationMinutes: row.DurationMinutes,
			BufferMinutes:   row.BufferMinutes,
			Attributes:      attributes,
			ImageURL:        row.ImageUrl,
			IsActive:        row.IsActive,
		},
		ProviderSlug:   row.ProviderSlug,
		ProviderName:   row.ProviderName,
		ProviderStatus: row.ProviderStatus,
		BookingMode:    row.BookingMode,
		Timezone:       row.Timezone,
		MinLeadMinutes: row.MinLeadMinutes,
	}, nil
}

// ListServices returns a provider's services.
func (c *Catalog) ListServices(ctx context.Context, providerID uuid.UUID, activeOnly bool) ([]Service, error) {
	rows, err := c.queries.ListServicesByProvider(ctx, db.ListServicesByProviderParams{
		ProviderID: providerID,
		ActiveOnly: activeOnly,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	services := make([]Service, 0, len(rows))
	for _, row := range rows {
		service, err := serviceFromRow(row)
		if err != nil {
			return nil, err
		}
		services = append(services, *service)
	}
	return services, nil
}

// UpdateServiceInput patches a service.
type UpdateServiceInput struct {
	CategoryID      *uuid.UUID     `json:"categoryId"`
	Name            *string        `json:"name"`
	Description     *string        `json:"description"`
	PriceCents      *int32         `json:"priceCents"`
	Currency        *string        `json:"currency"`
	DurationMinutes *int32         `json:"durationMinutes"`
	BufferMinutes   *int32         `json:"bufferMinutes"`
	Attributes      map[string]any `json:"attributes"`
	IsActive        *bool          `json:"isActive"`
}

// UpdateService applies a partial update, re-validating attributes whenever
// either the attributes or the category change.
func (c *Catalog) UpdateService(ctx context.Context, providerID, id uuid.UUID, in UpdateServiceInput) (*Service, error) {
	if in.PriceCents != nil && *in.PriceCents < 0 {
		return nil, httpx.Validation("priceCents cannot be negative")
	}
	if in.DurationMinutes != nil && *in.DurationMinutes <= 0 {
		return nil, httpx.Validation("durationMinutes must be greater than zero")
	}
	if in.BufferMinutes != nil && *in.BufferMinutes < 0 {
		return nil, httpx.Validation("bufferMinutes cannot be negative")
	}

	existing, err := c.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.ProviderID != providerID {
		// Do not confirm that a service belonging to someone else exists.
		return nil, httpx.NotFound("Service")
	}

	var encoded []byte
	if in.Attributes != nil || in.CategoryID != nil {
		categoryID := existing.CategoryID
		if in.CategoryID != nil {
			categoryID = *in.CategoryID
		}
		values := in.Attributes
		if values == nil {
			values = existing.Attributes
		}

		attributes, err := c.validateServiceAttributes(ctx, categoryID, values)
		if err != nil {
			return nil, err
		}
		if encoded, err = encodeAttributes(attributes); err != nil {
			return nil, httpx.Internal(err)
		}
	}

	var currency *string
	if in.Currency != nil {
		upper := strings.ToUpper(strings.TrimSpace(*in.Currency))
		currency = &upper
	}

	row, err := c.queries.UpdateService(ctx, db.UpdateServiceParams{
		ID:              id,
		ProviderID:      providerID,
		CategoryID:      in.CategoryID,
		Name:            trimPtr(in.Name),
		Description:     trimPtr(in.Description),
		PriceCents:      in.PriceCents,
		Currency:        currency,
		DurationMinutes: in.DurationMinutes,
		BufferMinutes:   in.BufferMinutes,
		Attributes:      encoded,
		IsActive:        in.IsActive,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Service")
		}
		if database.IsForeignKeyViolation(err) {
			return nil, httpx.Validation("categoryId does not refer to an existing category")
		}
		return nil, httpx.Internal(err)
	}

	return serviceFromRow(row)
}

// DeleteService removes a service. Bookings keep their own snapshot of it, so
// history survives the deletion.
func (c *Catalog) DeleteService(ctx context.Context, providerID, id uuid.UUID) error {
	affected, err := c.queries.DeleteService(ctx, db.DeleteServiceParams{ID: id, ProviderID: providerID})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Service")
	}
	return nil
}

// validateServiceAttributes runs the category's `service`-scoped definitions
// over the supplied values.
func (c *Catalog) validateServiceAttributes(ctx context.Context, categoryID uuid.UUID, values map[string]any) (map[string]any, error) {
	defs, err := c.ListAttributes(ctx, categoryID, AppliesToService)
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]any{}
	}
	return ValidateAttributes(defs, values)
}

// ValidateBookingAttributes runs the category's `booking`-scoped definitions.
// booking/ calls this at reservation time; it is the only catalog entry point
// that module needs, and it hands back plain values rather than anything
// category-aware.
func (c *Catalog) ValidateBookingAttributes(ctx context.Context, categoryID uuid.UUID, values map[string]any) (map[string]any, error) {
	defs, err := c.ListAttributes(ctx, categoryID, AppliesToBooking)
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]any{}
	}
	return ValidateAttributes(defs, values)
}

func serviceFromRow(row db.Service) (*Service, error) {
	attributes, err := decodeAttributes(row.Attributes)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return &Service{
		ID:              row.ID,
		ProviderID:      row.ProviderID,
		CategoryID:      row.CategoryID,
		Name:            row.Name,
		Description:     row.Description,
		PriceCents:      row.PriceCents,
		Currency:        row.Currency,
		DurationMinutes: row.DurationMinutes,
		BufferMinutes:   row.BufferMinutes,
		Attributes:      attributes,
		ImageURL:        row.ImageUrl,
		IsActive:        row.IsActive,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}
