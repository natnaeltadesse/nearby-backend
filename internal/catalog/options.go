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

// LoadOptionGroups reads a service's whole option tree, groups with their
// options nested. Two queries regardless of how many groups there are.
//
// This is what BuildQuote consumes, so availability and booking both price
// from exactly the same data.
func (c *Catalog) LoadOptionGroups(ctx context.Context, serviceID uuid.UUID) ([]OptionGroup, error) {
	groupRows, err := c.queries.ListOptionGroupsByService(ctx, serviceID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if len(groupRows) == 0 {
		return []OptionGroup{}, nil
	}

	optionRows, err := c.queries.ListOptionsByService(ctx, serviceID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	optionsByGroup := make(map[uuid.UUID][]Option, len(groupRows))
	for _, row := range optionRows {
		optionsByGroup[row.GroupID] = append(optionsByGroup[row.GroupID], Option{
			ID:                   row.ID,
			GroupID:              row.GroupID,
			Name:                 row.Name,
			PriceDeltaCents:      row.PriceDeltaCents,
			DurationDeltaMinutes: row.DurationDeltaMinutes,
			IsActive:             row.IsActive,
			SortOrder:            row.SortOrder,
		})
	}

	groups := make([]OptionGroup, 0, len(groupRows))
	for _, row := range groupRows {
		options := optionsByGroup[row.ID]
		if options == nil {
			options = []Option{}
		}
		groups = append(groups, OptionGroup{
			ID:            row.ID,
			ServiceID:     row.ServiceID,
			Name:          row.Name,
			SelectionType: row.SelectionType,
			IsRequired:    row.IsRequired,
			MinSelect:     row.MinSelect,
			MaxSelect:     row.MaxSelect,
			SortOrder:     row.SortOrder,
			Options:       options,
		})
	}
	return groups, nil
}

// QuoteFor loads a service's option tree and prices a selection against it.
// The single entry point booking/ and scheduling/ use — neither of them ever
// sees an option group.
func (c *Catalog) QuoteFor(ctx context.Context, service Service, chosen []uuid.UUID) (*Quote, error) {
	groups, err := c.LoadOptionGroups(ctx, service.ID)
	if err != nil {
		return nil, err
	}
	return BuildQuote(service, groups, chosen)
}

// CreateOptionGroupInput describes a new group of choices on a service.
type CreateOptionGroupInput struct {
	Name          string `json:"name"`
	SelectionType string `json:"selectionType"`
	IsRequired    *bool  `json:"isRequired"`
	MinSelect     *int32 `json:"minSelect"`
	MaxSelect     *int32 `json:"maxSelect"`
	SortOrder     *int32 `json:"sortOrder"`
}

// CreateOptionGroup adds a group to a service the caller's org owns.
func (c *Catalog) CreateOptionGroup(ctx context.Context, providerID, serviceID uuid.UUID, in CreateOptionGroupInput) (*OptionGroup, error) {
	if err := c.assertServiceOwned(ctx, providerID, serviceID); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Validation("Name is required")
	}
	if in.SelectionType != SelectionSingle && in.SelectionType != SelectionMulti {
		return nil, httpx.Validation("selectionType must be 'single' or 'multi'")
	}

	minSelect := valueOr(in.MinSelect, 0)
	if minSelect < 0 {
		return nil, httpx.Validation("minSelect cannot be negative")
	}
	if in.MaxSelect != nil {
		if *in.MaxSelect < 1 {
			return nil, httpx.Validation("maxSelect must be at least 1")
		}
		if minSelect > *in.MaxSelect {
			return nil, httpx.Validation("minSelect cannot exceed maxSelect")
		}
		if in.SelectionType == SelectionSingle && *in.MaxSelect != 1 {
			return nil, httpx.Validation("a single-select group cannot have maxSelect above 1")
		}
	}

	row, err := c.queries.CreateOptionGroup(ctx, db.CreateOptionGroupParams{
		ServiceID:     serviceID,
		Name:          name,
		SelectionType: in.SelectionType,
		IsRequired:    valueOr(in.IsRequired, false),
		MinSelect:     minSelect,
		MaxSelect:     in.MaxSelect,
		SortOrder:     valueOr(in.SortOrder, 0),
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	return &OptionGroup{
		ID: row.ID, ServiceID: row.ServiceID, Name: row.Name,
		SelectionType: row.SelectionType, IsRequired: row.IsRequired,
		MinSelect: row.MinSelect, MaxSelect: row.MaxSelect, SortOrder: row.SortOrder,
		Options: []Option{},
	}, nil
}

// UpdateOptionGroupInput patches a group.
type UpdateOptionGroupInput struct {
	Name          *string `json:"name"`
	SelectionType *string `json:"selectionType"`
	IsRequired    *bool   `json:"isRequired"`
	MinSelect     *int32  `json:"minSelect"`
	MaxSelect     *int32  `json:"maxSelect"`
	SortOrder     *int32  `json:"sortOrder"`
}

// UpdateOptionGroup applies a partial update to a group.
func (c *Catalog) UpdateOptionGroup(ctx context.Context, providerID, serviceID, groupID uuid.UUID, in UpdateOptionGroupInput) (*OptionGroup, error) {
	if err := c.assertServiceOwned(ctx, providerID, serviceID); err != nil {
		return nil, err
	}
	if in.SelectionType != nil && *in.SelectionType != SelectionSingle && *in.SelectionType != SelectionMulti {
		return nil, httpx.Validation("selectionType must be 'single' or 'multi'")
	}
	if in.MinSelect != nil && *in.MinSelect < 0 {
		return nil, httpx.Validation("minSelect cannot be negative")
	}
	if in.MaxSelect != nil && *in.MaxSelect < 1 {
		return nil, httpx.Validation("maxSelect must be at least 1")
	}

	row, err := c.queries.UpdateOptionGroup(ctx, db.UpdateOptionGroupParams{
		ID:            groupID,
		ServiceID:     serviceID,
		Name:          trimPtr(in.Name),
		SelectionType: in.SelectionType,
		IsRequired:    in.IsRequired,
		MinSelect:     in.MinSelect,
		MaxSelect:     in.MaxSelect,
		SortOrder:     in.SortOrder,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Option group")
		}
		// The min<=max and single-implies-max-1 rules are CHECK constraints, so
		// a partial update that breaks them surfaces here rather than above.
		if name := database.ConstraintName(err); strings.HasPrefix(name, "option_groups_") {
			return nil, httpx.Validation("That combination of selectionType, minSelect and maxSelect is not allowed")
		}
		return nil, httpx.Internal(err)
	}

	return &OptionGroup{
		ID: row.ID, ServiceID: row.ServiceID, Name: row.Name,
		SelectionType: row.SelectionType, IsRequired: row.IsRequired,
		MinSelect: row.MinSelect, MaxSelect: row.MaxSelect, SortOrder: row.SortOrder,
		Options: []Option{},
	}, nil
}

// DeleteOptionGroup removes a group and, by cascade, its options.
func (c *Catalog) DeleteOptionGroup(ctx context.Context, providerID, serviceID, groupID uuid.UUID) error {
	if err := c.assertServiceOwned(ctx, providerID, serviceID); err != nil {
		return err
	}
	affected, err := c.queries.DeleteOptionGroup(ctx, db.DeleteOptionGroupParams{
		ID:        groupID,
		ServiceID: serviceID,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Option group")
	}
	return nil
}

// CreateOptionInput describes a new choice within a group.
type CreateOptionInput struct {
	Name                 string `json:"name"`
	PriceDeltaCents      *int32 `json:"priceDeltaCents"`
	DurationDeltaMinutes *int32 `json:"durationDeltaMinutes"`
	IsActive             *bool  `json:"isActive"`
	SortOrder            *int32 `json:"sortOrder"`
}

// CreateOption adds a choice to a group.
func (c *Catalog) CreateOption(ctx context.Context, providerID, serviceID, groupID uuid.UUID, in CreateOptionInput) (*Option, error) {
	if err := c.assertGroupOwned(ctx, providerID, serviceID, groupID); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Validation("Name is required")
	}

	row, err := c.queries.CreateServiceOption(ctx, db.CreateServiceOptionParams{
		GroupID:              groupID,
		Name:                 name,
		PriceDeltaCents:      valueOr(in.PriceDeltaCents, 0),
		DurationDeltaMinutes: valueOr(in.DurationDeltaMinutes, 0),
		IsActive:             valueOr(in.IsActive, true),
		SortOrder:            valueOr(in.SortOrder, 0),
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	return &Option{
		ID: row.ID, GroupID: row.GroupID, Name: row.Name,
		PriceDeltaCents: row.PriceDeltaCents, DurationDeltaMinutes: row.DurationDeltaMinutes,
		IsActive: row.IsActive, SortOrder: row.SortOrder,
	}, nil
}

// UpdateOptionInput patches a choice.
type UpdateOptionInput struct {
	Name                 *string `json:"name"`
	PriceDeltaCents      *int32  `json:"priceDeltaCents"`
	DurationDeltaMinutes *int32  `json:"durationDeltaMinutes"`
	IsActive             *bool   `json:"isActive"`
	SortOrder            *int32  `json:"sortOrder"`
}

// UpdateOption applies a partial update to a choice.
func (c *Catalog) UpdateOption(ctx context.Context, providerID, serviceID, groupID, optionID uuid.UUID, in UpdateOptionInput) (*Option, error) {
	if err := c.assertGroupOwned(ctx, providerID, serviceID, groupID); err != nil {
		return nil, err
	}

	row, err := c.queries.UpdateServiceOption(ctx, db.UpdateServiceOptionParams{
		ID:                   optionID,
		GroupID:              groupID,
		Name:                 trimPtr(in.Name),
		PriceDeltaCents:      in.PriceDeltaCents,
		DurationDeltaMinutes: in.DurationDeltaMinutes,
		IsActive:             in.IsActive,
		SortOrder:            in.SortOrder,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Option")
		}
		return nil, httpx.Internal(err)
	}

	return &Option{
		ID: row.ID, GroupID: row.GroupID, Name: row.Name,
		PriceDeltaCents: row.PriceDeltaCents, DurationDeltaMinutes: row.DurationDeltaMinutes,
		IsActive: row.IsActive, SortOrder: row.SortOrder,
	}, nil
}

// DeleteOption removes a choice. Bookings snapshot their options, so past
// reservations keep the name and price they were made with.
func (c *Catalog) DeleteOption(ctx context.Context, providerID, serviceID, groupID, optionID uuid.UUID) error {
	if err := c.assertGroupOwned(ctx, providerID, serviceID, groupID); err != nil {
		return err
	}
	affected, err := c.queries.DeleteServiceOption(ctx, db.DeleteServiceOptionParams{
		ID:      optionID,
		GroupID: groupID,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Option")
	}
	return nil
}

// assertServiceOwned rejects cross-tenant access to a service. The org
// middleware proves membership of providerID; this proves the service is that
// provider's, so a valid member of org A cannot edit org B's catalog by id.
func (c *Catalog) assertServiceOwned(ctx context.Context, providerID, serviceID uuid.UUID) error {
	service, err := c.GetService(ctx, serviceID)
	if err != nil {
		return err
	}
	if service.ProviderID != providerID {
		return httpx.NotFound("Service")
	}
	return nil
}

func (c *Catalog) assertGroupOwned(ctx context.Context, providerID, serviceID, groupID uuid.UUID) error {
	if err := c.assertServiceOwned(ctx, providerID, serviceID); err != nil {
		return err
	}
	group, err := c.queries.GetOptionGroup(ctx, groupID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Option group")
		}
		return httpx.Internal(err)
	}
	if group.ServiceID != serviceID {
		return httpx.NotFound("Option group")
	}
	return nil
}
