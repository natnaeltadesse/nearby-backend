package scheduling

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Resource is a bay, a chair, a barber — anything a booking can occupy.
type Resource struct {
	ID         uuid.UUID   `json:"id"`
	ProviderID uuid.UUID   `json:"providerId"`
	Name       string      `json:"name"`
	UserID     *uuid.UUID  `json:"userId,omitempty"`
	IsActive   bool        `json:"isActive"`
	ServiceIDs []uuid.UUID `json:"serviceIds,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`
}

// HoursRule is one weekly opening span. A nil ResourceID makes it the
// provider-wide default that resources without their own hours inherit.
type HoursRule struct {
	ID         uuid.UUID  `json:"id"`
	ProviderID uuid.UUID  `json:"providerId"`
	ResourceID *uuid.UUID `json:"resourceId,omitempty"`
	Weekday    int        `json:"weekday"` // 0 = Sunday
	Opens      TimeOfDay  `json:"opensAt"`
	Closes     TimeOfDay  `json:"closesAt"`
}

// ExceptionRule overrides the weekly pattern on one date. A nil ResourceID
// applies to the whole provider.
type ExceptionRule struct {
	ID         uuid.UUID  `json:"id"`
	ProviderID uuid.UUID  `json:"providerId"`
	ResourceID *uuid.UUID `json:"resourceId,omitempty"`
	Date       string     `json:"date"` // YYYY-MM-DD in the provider's zone
	IsClosed   bool       `json:"isClosed"`
	Opens      *TimeOfDay `json:"opensAt,omitempty"`
	Closes     *TimeOfDay `json:"closesAt,omitempty"`
	Reason     *string    `json:"reason,omitempty"`
}

// ServiceScheduling is everything this package needs to know about a bookable
// thing: how long it takes, whose calendar it lands on, and whether it can be
// booked at all.
//
// Note what is missing: name, price, category, attributes. That absence is the
// module boundary — scheduling cannot branch on a category because it is never
// told one.
type ServiceScheduling struct {
	ServiceID  uuid.UUID
	ProviderID uuid.UUID
	// TotalDuration already includes the chosen options' deltas and the
	// service's buffer. Computing it is catalog's job, not this package's.
	TotalDuration  time.Duration
	Timezone       string
	MinLeadMinutes int32
	// Bookable is false when the service or its provider is inactive.
	Bookable       bool
	UnbookableCode string
}

// ServiceResolver is scheduling's outbound port to the catalog.
//
// Declared here, implemented there: this package states the little it needs
// and stays unaware of everything else a service is.
type ServiceResolver interface {
	ResolveScheduling(ctx context.Context, serviceID uuid.UUID, optionIDs []uuid.UUID) (ServiceScheduling, error)
}
