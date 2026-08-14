package catalog

import (
	"context"
	"errors"
	"strings"
	"time"

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

// ListServices returns a provider's services, unpaginated and sorted by name.
// The public surface uses it, where the whole active catalog is the answer.
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

// Sort keys accepted by ListServicesPage. Anything else is a client error
// rather than a silent fallback: a table that quietly ignores the column you
// clicked is worse than one that tells you it cannot sort by it.
const (
	SortByName     = "name"
	SortByCategory = "category"
	SortByPrice    = "price"
	SortByDuration = "duration"
	SortByCreated  = "created"
)

// ValidServiceSort reports whether key is a sortable column.
func ValidServiceSort(key string) bool {
	switch key {
	case SortByName, SortByCategory, SortByPrice, SortByDuration, SortByCreated:
		return true
	}
	return false
}

// ListServicesInput is the org catalog list: search, filter, sort, paginate.
type ListServicesInput struct {
	Search string
	// Nil means "either"; the org surface shows inactive services too, so this
	// is a filter rather than the public surface's fixed activeOnly.
	IsActive   *bool
	CategoryID *uuid.UUID
	SortBy     string
	SortDesc   bool
	Limit      int32
	Offset     int32
}

// ListServicesPage answers the provider dashboard's table. Searching, sorting
// and paging all happen in Postgres, so the client never has to hold the whole
// catalog to filter it, and the numbers it shows are the real totals rather
// than the size of whatever page it happens to have.
func (c *Catalog) ListServicesPage(
	ctx context.Context,
	providerID uuid.UUID,
	in ListServicesInput,
) ([]Service, int64, error) {
	sortBy := in.SortBy
	if sortBy == "" {
		sortBy = SortByName
	}
	if !ValidServiceSort(sortBy) {
		return nil, 0, httpx.Validation(
			"sort must be one of: name, category, price, duration, created")
	}

	if in.Limit <= 0 {
		in.Limit = 25
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	rows, err := c.queries.ListServicesPage(ctx, db.ListServicesPageParams{
		ProviderID:   providerID,
		IsActive:     in.IsActive,
		CategoryID:   in.CategoryID,
		Search:       strings.TrimSpace(in.Search),
		SortBy:       sortBy,
		SortDesc:     in.SortDesc,
		ResultLimit:  in.Limit,
		ResultOffset: in.Offset,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	total, err := c.queries.CountServicesPage(ctx, db.CountServicesPageParams{
		ProviderID: providerID,
		IsActive:   in.IsActive,
		CategoryID: in.CategoryID,
		Search:     strings.TrimSpace(in.Search),
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	services := make([]Service, 0, len(rows))
	for _, row := range rows {
		attributes, err := decodeAttributes(row.Attributes)
		if err != nil {
			return nil, 0, httpx.Internal(err)
		}

		services = append(services, Service{
			ID:              row.ID,
			ProviderID:      row.ProviderID,
			CategoryID:      row.CategoryID,
			CategoryName:    row.CategoryName,
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
		})
	}
	return services, total, nil
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

// CatalogCategoryCount is one bar in the dashboard's category breakdown.
type CatalogCategoryCount struct {
	CategoryID   uuid.UUID `json:"categoryId"`
	CategoryName *string   `json:"categoryName,omitempty"`
	Count        int64     `json:"count"`
}

// CatalogStats are the headline figures above the services table.
//
// Deliberately unfiltered: these describe the whole catalog, while the table
// under them describes whatever the current query is. Mixing the two would
// make the cards move every time someone typed in the search box, which is
// the opposite of what a stable reference figure is for.
type CatalogStats struct {
	Total  int64 `json:"total"`
	Active int64 `json:"active"`
	Hidden int64 `json:"hidden"`

	PriceMinCents int32 `json:"priceMinCents"`
	PriceMaxCents int32 `json:"priceMaxCents"`
	PriceAvgCents int32 `json:"priceAvgCents"`

	DurationMinMinutes int32 `json:"durationMinMinutes"`
	DurationMaxMinutes int32 `json:"durationMaxMinutes"`
	DurationAvgMinutes int32 `json:"durationAvgMinutes"`

	ByCategory []CatalogCategoryCount `json:"byCategory"`
	Growth     CatalogGrowth          `json:"growth"`
}

// CatalogGrowth is the rolling window behind the growth chart.
type CatalogGrowth struct {
	// PriorTotal is what existed before the window opened, so a running total
	// drawn from Months starts at the truth instead of at zero.
	PriorTotal int64                `json:"priorTotal"`
	Months     []CatalogMonthlyAdds `json:"months"`
}

// CatalogMonthlyAdds is one month's worth of new services.
type CatalogMonthlyAdds struct {
	// Month is the first day of the month, as a plain date.
	Month string `json:"month"`
	Added int64  `json:"added"`
}

// growthWindowMonths is how far back the growth chart looks. Six points is
// enough to read a direction without the marks getting too thin to hit.
const growthWindowMonths = 5

// CatalogStats aggregates a provider's catalog in the database rather than
// shipping every row to the client to be counted there.
func (c *Catalog) CatalogStats(ctx context.Context, providerID uuid.UUID) (*CatalogStats, error) {
	row, err := c.queries.ServiceCatalogStats(ctx, providerID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	byCategory, err := c.queries.ServiceCountByCategory(ctx, providerID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	stats := &CatalogStats{
		Total:              row.Total,
		Active:             row.Active,
		Hidden:             row.Total - row.Active,
		PriceMinCents:      row.PriceMinCents,
		PriceMaxCents:      row.PriceMaxCents,
		PriceAvgCents:      row.PriceAvgCents,
		DurationMinMinutes: row.DurationMinMinutes,
		DurationMaxMinutes: row.DurationMaxMinutes,
		DurationAvgMinutes: row.DurationAvgMinutes,
		ByCategory:         make([]CatalogCategoryCount, 0, len(byCategory)),
	}

	for _, entry := range byCategory {
		stats.ByCategory = append(stats.ByCategory, CatalogCategoryCount{
			CategoryID:   entry.CategoryID,
			CategoryName: entry.CategoryName,
			Count:        entry.ServiceCount,
		})
	}

	priorTotal, err := c.queries.ServicesCreatedBefore(ctx, db.ServicesCreatedBeforeParams{
		ProviderID: providerID,
		MonthsBack: growthWindowMonths,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	monthly, err := c.queries.ServicesAddedByMonth(ctx, db.ServicesAddedByMonthParams{
		ProviderID: providerID,
		MonthsBack: growthWindowMonths,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	// Months with nothing added return no row, but a gap in a time series
	// reads as a change in slope that never happened — so the window is
	// filled in here rather than left for each client to reinvent.
	added := make(map[string]int64, len(monthly))
	for _, entry := range monthly {
		added[entry.Month.Format("2006-01-02")] = entry.Added
	}

	start := time.Now().UTC()
	start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, -growthWindowMonths, 0)

	stats.Growth = CatalogGrowth{
		PriorTotal: priorTotal,
		Months:     make([]CatalogMonthlyAdds, 0, growthWindowMonths+1),
	}
	for i := 0; i <= growthWindowMonths; i++ {
		key := start.AddDate(0, i, 0).Format("2006-01-02")
		stats.Growth.Months = append(stats.Growth.Months, CatalogMonthlyAdds{
			Month: key,
			Added: added[key],
		})
	}

	return stats, nil
}
