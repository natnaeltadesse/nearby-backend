// Package catalog is the only module that knows what a category is.
//
// Everything category-specific lives behind the attribute model here: adding a
// vertical is a data insert by a platform admin, not a migration and not an app
// release. scheduling/ and booking/ consume durations and prices from this
// package and never learn where they came from.
package catalog

import (
	"time"

	"github.com/google/uuid"
)

// Attribute data types.
const (
	TypeEnum      = "enum"
	TypeMultiEnum = "multi_enum"
	TypeText      = "text"
	TypeNumber    = "number"
	TypeBool      = "bool"
)

// What an attribute definition describes.
const (
	AppliesToService  = "service"
	AppliesToBooking  = "booking"
	AppliesToProvider = "provider"
)

// Option group selection types.
const (
	SelectionSingle = "single"
	SelectionMulti  = "multi"
)

// Category is a vertical: car wash, men's barber shop, laundry.
type Category struct {
	ID        uuid.UUID  `json:"id"`
	Slug      string     `json:"slug"`
	Name      string     `json:"name"`
	Icon      *string    `json:"icon,omitempty"`
	ParentID  *uuid.UUID `json:"parentId,omitempty"`
	SortOrder int32      `json:"sortOrder"`
	IsActive  bool       `json:"isActive"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// Attribute is one field a category adds to services, bookings or providers.
// Both clients build their forms from these, so the JSON shape is a contract.
type Attribute struct {
	ID         uuid.UUID `json:"id"`
	CategoryID uuid.UUID `json:"categoryId"`
	Key        string    `json:"key"`
	Label      string    `json:"label"`
	DataType   string    `json:"dataType"`
	Options    []string  `json:"options,omitempty"`
	Required   bool      `json:"required"`
	AppliesTo  string    `json:"appliesTo"`
	Filterable bool      `json:"filterable"`
	SortOrder  int32     `json:"sortOrder"`
}

// Service is one bookable thing a provider sells.
type Service struct {
	ID         uuid.UUID `json:"id"`
	ProviderID uuid.UUID `json:"providerId"`
	CategoryID uuid.UUID `json:"categoryId"`
	// CategoryName is populated by the list endpoints, which join it anyway to
	// sort by it. Saves every client re-deriving the label from a uuid.
	CategoryName *string `json:"categoryName,omitempty"`
	Name         string  `json:"name"`
	Description  *string `json:"description,omitempty"`
	// Minor units (santim): 24500 = 245.00 ETB. Never a float.
	PriceCents int32  `json:"priceCents"`
	Currency   string `json:"currency"`
	// The scheduling engine's only real input.
	DurationMinutes int32          `json:"durationMinutes"`
	BufferMinutes   int32          `json:"bufferMinutes"`
	Attributes      map[string]any `json:"attributes"`
	ImageURL        *string        `json:"imageUrl,omitempty"`
	IsActive        bool           `json:"isActive"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`

	// Populated only by the detail endpoint.
	OptionGroups []OptionGroup `json:"optionGroups,omitempty"`
}

// OptionGroup gives radio-vs-checkbox semantics with one mechanism.
type OptionGroup struct {
	ID            uuid.UUID `json:"id"`
	ServiceID     uuid.UUID `json:"serviceId"`
	Name          string    `json:"name"`
	SelectionType string    `json:"selectionType"`
	IsRequired    bool      `json:"isRequired"`
	MinSelect     int32     `json:"minSelect"`
	MaxSelect     *int32    `json:"maxSelect,omitempty"`
	SortOrder     int32     `json:"sortOrder"`
	Options       []Option  `json:"options"`
}

// Option is one choice within a group, carrying its price and duration effect.
type Option struct {
	ID                   uuid.UUID `json:"id"`
	GroupID              uuid.UUID `json:"groupId"`
	Name                 string    `json:"name"`
	PriceDeltaCents      int32     `json:"priceDeltaCents"`
	DurationDeltaMinutes int32     `json:"durationDeltaMinutes"`
	IsActive             bool      `json:"isActive"`
	SortOrder            int32     `json:"sortOrder"`
}

// ServiceWithProvider is a service plus the provider facts booking needs:
// booking_mode, timezone and status decide the rest of the reservation flow.
type ServiceWithProvider struct {
	Service
	ProviderSlug   string `json:"providerSlug"`
	ProviderName   string `json:"providerName"`
	ProviderStatus string `json:"providerStatus"`
	BookingMode    string `json:"bookingMode"`
	Timezone       string `json:"timezone"`
	MinLeadMinutes int32  `json:"minLeadMinutes"`
}
