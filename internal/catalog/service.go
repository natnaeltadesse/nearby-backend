package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// Catalog implements the catalog use cases.
//
// It is named for the module rather than the usual `Service`, because in this
// package `Service` is already taken by the domain noun — a bookable service.
type Catalog struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// New wires the catalog.
func New(pool *pgxpool.Pool) *Catalog {
	return &Catalog{pool: pool, queries: db.New(pool)}
}

// --- categories -----------------------------------------------------------

// ListCategories returns categories, optionally only the active ones.
func (c *Catalog) ListCategories(ctx context.Context, activeOnly bool) ([]Category, error) {
	rows, err := c.queries.ListCategories(ctx, activeOnly)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	categories := make([]Category, 0, len(rows))
	for _, row := range rows {
		categories = append(categories, Category{
			ID:        row.ID,
			Slug:      row.Slug,
			Name:      row.Name,
			Icon:      row.Icon,
			ParentID:  row.ParentID,
			SortOrder: row.SortOrder,
			IsActive:  row.IsActive,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return categories, nil
}

// GetCategory reads one category.
func (c *Catalog) GetCategory(ctx context.Context, id uuid.UUID) (*Category, error) {
	row, err := c.queries.GetCategoryByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Category")
		}
		return nil, httpx.Internal(err)
	}
	return &Category{
		ID:        row.ID,
		Slug:      row.Slug,
		Name:      row.Name,
		Icon:      row.Icon,
		ParentID:  row.ParentID,
		SortOrder: row.SortOrder,
		IsActive:  row.IsActive,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// CreateCategoryInput describes a new vertical.
type CreateCategoryInput struct {
	Slug      *string    `json:"slug"`
	Name      string     `json:"name"`
	Icon      *string    `json:"icon"`
	ParentID  *uuid.UUID `json:"parentId"`
	SortOrder *int32     `json:"sortOrder"`
	IsActive  *bool      `json:"isActive"`
}

// CreateCategory adds a vertical. Platform-admin only.
func (c *Catalog) CreateCategory(ctx context.Context, in CreateCategoryInput) (*Category, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Validation("Name is required")
	}

	slug := slugify(name)
	if in.Slug != nil {
		slug = slugify(*in.Slug)
	}
	if slug == "" {
		return nil, httpx.Validation("slug must contain at least one letter or digit")
	}

	row, err := c.queries.CreateCategory(ctx, db.CreateCategoryParams{
		Slug:      slug,
		Name:      name,
		Icon:      trimPtr(in.Icon),
		ParentID:  in.ParentID,
		SortOrder: valueOr(in.SortOrder, 0),
		IsActive:  valueOr(in.IsActive, true),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, httpx.Conflict(fmt.Sprintf("The category slug %q is already taken", slug))
		}
		if database.IsForeignKeyViolation(err) {
			return nil, httpx.Validation("parentId does not refer to an existing category")
		}
		return nil, httpx.Internal(err)
	}

	return &Category{
		ID: row.ID, Slug: row.Slug, Name: row.Name, Icon: row.Icon,
		ParentID: row.ParentID, SortOrder: row.SortOrder, IsActive: row.IsActive,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// UpdateCategoryInput patches a category.
type UpdateCategoryInput struct {
	Slug      *string    `json:"slug"`
	Name      *string    `json:"name"`
	Icon      *string    `json:"icon"`
	ParentID  *uuid.UUID `json:"parentId"`
	SortOrder *int32     `json:"sortOrder"`
	IsActive  *bool      `json:"isActive"`
}

// UpdateCategory applies a partial update. Platform-admin only.
func (c *Catalog) UpdateCategory(ctx context.Context, id uuid.UUID, in UpdateCategoryInput) (*Category, error) {
	var slug *string
	if in.Slug != nil {
		normalized := slugify(*in.Slug)
		if normalized == "" {
			return nil, httpx.Validation("slug must contain at least one letter or digit")
		}
		slug = &normalized
	}

	row, err := c.queries.UpdateCategory(ctx, db.UpdateCategoryParams{
		ID:        id,
		Slug:      slug,
		Name:      trimPtr(in.Name),
		Icon:      trimPtr(in.Icon),
		ParentID:  in.ParentID,
		SortOrder: in.SortOrder,
		IsActive:  in.IsActive,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Category")
		}
		if database.IsUniqueViolation(err) {
			return nil, httpx.Conflict("That category slug is already taken")
		}
		return nil, httpx.Internal(err)
	}

	return &Category{
		ID: row.ID, Slug: row.Slug, Name: row.Name, Icon: row.Icon,
		ParentID: row.ParentID, SortOrder: row.SortOrder, IsActive: row.IsActive,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// DeleteCategory removes a category. Services reference categories with
// ON DELETE RESTRICT, so a category in use cannot be deleted — deactivate it
// instead.
func (c *Catalog) DeleteCategory(ctx context.Context, id uuid.UUID) error {
	affected, err := c.queries.DeleteCategory(ctx, id)
	if err != nil {
		if database.ConstraintName(err) != "" {
			return httpx.Conflict("That category still has services; deactivate it instead")
		}
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Category")
	}
	return nil
}

// --- attributes -----------------------------------------------------------

// ListAttributes returns a category's attribute definitions, optionally
// narrowed to one `appliesTo` scope. This is what both clients read to render
// their forms dynamically.
func (c *Catalog) ListAttributes(ctx context.Context, categoryID uuid.UUID, appliesTo string) ([]Attribute, error) {
	rows, err := c.queries.ListCategoryAttributes(ctx, db.ListCategoryAttributesParams{
		CategoryID: categoryID,
		AppliesTo:  appliesTo,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	attributes := make([]Attribute, 0, len(rows))
	for _, row := range rows {
		options, err := decodeOptions(row.Options)
		if err != nil {
			return nil, httpx.Internal(err)
		}
		attributes = append(attributes, Attribute{
			ID:         row.ID,
			CategoryID: row.CategoryID,
			Key:        row.Key,
			Label:      row.Label,
			DataType:   row.DataType,
			Options:    options,
			Required:   row.Required,
			AppliesTo:  row.AppliesTo,
			Filterable: row.Filterable,
			SortOrder:  row.SortOrder,
		})
	}
	return attributes, nil
}

// CreateAttributeInput defines a new field on a category.
type CreateAttributeInput struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	DataType   string   `json:"dataType"`
	Options    []string `json:"options"`
	Required   *bool    `json:"required"`
	AppliesTo  string   `json:"appliesTo"`
	Filterable *bool    `json:"filterable"`
	SortOrder  *int32   `json:"sortOrder"`
}

// CreateAttribute adds a field to a category. Platform-admin only.
func (c *Catalog) CreateAttribute(ctx context.Context, categoryID uuid.UUID, in CreateAttributeInput) (*Attribute, error) {
	key := strings.TrimSpace(in.Key)
	label := strings.TrimSpace(in.Label)

	if key == "" {
		return nil, httpx.Validation("key is required")
	}
	if label == "" {
		return nil, httpx.Validation("label is required")
	}
	if err := validateDataType(in.DataType, in.Options); err != nil {
		return nil, err
	}
	if err := validateAppliesTo(in.AppliesTo); err != nil {
		return nil, err
	}

	encoded, err := encodeOptions(in.DataType, in.Options)
	if err != nil {
		return nil, err
	}

	row, err := c.queries.CreateCategoryAttribute(ctx, db.CreateCategoryAttributeParams{
		CategoryID: categoryID,
		Key:        key,
		Label:      label,
		DataType:   in.DataType,
		Options:    encoded,
		Required:   valueOr(in.Required, false),
		AppliesTo:  in.AppliesTo,
		Filterable: valueOr(in.Filterable, false),
		SortOrder:  valueOr(in.SortOrder, 0),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, httpx.Conflict(fmt.Sprintf(
				"This category already defines %q for %s", key, in.AppliesTo))
		}
		if database.IsForeignKeyViolation(err) {
			return nil, httpx.NotFound("Category")
		}
		return nil, httpx.Internal(err)
	}

	options, err := decodeOptions(row.Options)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	return &Attribute{
		ID: row.ID, CategoryID: row.CategoryID, Key: row.Key, Label: row.Label,
		DataType: row.DataType, Options: options, Required: row.Required,
		AppliesTo: row.AppliesTo, Filterable: row.Filterable, SortOrder: row.SortOrder,
	}, nil
}

// UpdateAttributeInput patches an attribute definition.
//
// `key` and `appliesTo` are deliberately absent: changing either would orphan
// every value already stored under the old identity.
type UpdateAttributeInput struct {
	Label      *string  `json:"label"`
	DataType   *string  `json:"dataType"`
	Options    []string `json:"options"`
	Required   *bool    `json:"required"`
	Filterable *bool    `json:"filterable"`
	SortOrder  *int32   `json:"sortOrder"`
}

// UpdateAttribute applies a partial update. Platform-admin only.
func (c *Catalog) UpdateAttribute(ctx context.Context, id uuid.UUID, in UpdateAttributeInput) (*Attribute, error) {
	existing, err := c.queries.GetCategoryAttribute(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Attribute")
		}
		return nil, httpx.Internal(err)
	}

	dataType := existing.DataType
	if in.DataType != nil {
		dataType = *in.DataType
	}

	options := in.Options
	if options == nil {
		decoded, err := decodeOptions(existing.Options)
		if err != nil {
			return nil, httpx.Internal(err)
		}
		options = decoded
	}
	if err := validateDataType(dataType, options); err != nil {
		return nil, err
	}

	var encoded []byte
	if in.Options != nil || in.DataType != nil {
		encoded, err = encodeOptions(dataType, options)
		if err != nil {
			return nil, err
		}
	}

	row, err := c.queries.UpdateCategoryAttribute(ctx, db.UpdateCategoryAttributeParams{
		ID:         id,
		Label:      trimPtr(in.Label),
		DataType:   in.DataType,
		Options:    encoded,
		Required:   in.Required,
		Filterable: in.Filterable,
		SortOrder:  in.SortOrder,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Attribute")
		}
		return nil, httpx.Internal(err)
	}

	decodedOptions, err := decodeOptions(row.Options)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	return &Attribute{
		ID: row.ID, CategoryID: row.CategoryID, Key: row.Key, Label: row.Label,
		DataType: row.DataType, Options: decodedOptions, Required: row.Required,
		AppliesTo: row.AppliesTo, Filterable: row.Filterable, SortOrder: row.SortOrder,
	}, nil
}

// DeleteAttribute removes an attribute definition. Platform-admin only.
func (c *Catalog) DeleteAttribute(ctx context.Context, id uuid.UUID) error {
	affected, err := c.queries.DeleteCategoryAttribute(ctx, id)
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Attribute")
	}
	return nil
}

// --- helpers --------------------------------------------------------------

func validateDataType(dataType string, options []string) error {
	switch dataType {
	case TypeText, TypeNumber, TypeBool:
		return nil
	case TypeEnum, TypeMultiEnum:
		if len(options) == 0 {
			return httpx.Validation(fmt.Sprintf("%s attributes need a non-empty options list", dataType))
		}
		seen := make(map[string]struct{}, len(options))
		for _, option := range options {
			if strings.TrimSpace(option) == "" {
				return httpx.Validation("options cannot contain blank values")
			}
			if _, duplicate := seen[option]; duplicate {
				return httpx.Validation(fmt.Sprintf("options contains %q twice", option))
			}
			seen[option] = struct{}{}
		}
		return nil
	default:
		return httpx.Validation("dataType must be one of: enum, multi_enum, text, number, bool")
	}
}

func validateAppliesTo(appliesTo string) error {
	switch appliesTo {
	case AppliesToService, AppliesToBooking, AppliesToProvider:
		return nil
	default:
		return httpx.Validation("appliesTo must be one of: service, booking, provider")
	}
}

func encodeOptions(dataType string, options []string) ([]byte, error) {
	if dataType != TypeEnum && dataType != TypeMultiEnum {
		return nil, nil
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return encoded, nil
}

func decodeOptions(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var options []string
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, fmt.Errorf("decode attribute options: %w", err)
	}
	return options, nil
}

func decodeAttributes(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode attributes: %w", err)
	}
	return values, nil
}

func encodeAttributes(values map[string]any) ([]byte, error) {
	if values == nil {
		values = map[string]any{}
	}
	return json.Marshal(values)
}

func slugify(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	var b strings.Builder
	lastHyphen := true
	for _, r := range lowered {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteRune('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func valueOr[T any](ptr *T, fallback T) T {
	if ptr == nil {
		return fallback
	}
	return *ptr
}
