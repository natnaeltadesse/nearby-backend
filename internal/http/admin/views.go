package admin

import (
	"time"

	"github.com/google/uuid"
)

// adminBooking is the cross-tenant booking row. It is a flatter, thinner shape
// than booking.Booking on purpose: the admin list is for triage, and pulling
// option snapshots and attributes for every tenant's bookings would cost far
// more than the view is worth.
type adminBooking struct {
	ID     uuid.UUID `json:"id"`
	Code   string    `json:"code"`
	Status string    `json:"status"`

	ProviderID   uuid.UUID `json:"providerId"`
	ProviderSlug string    `json:"providerSlug"`
	ProviderName string    `json:"providerName"`

	ServiceName     string `json:"serviceName"`
	PriceCents      int32  `json:"priceCents"`
	Currency        string `json:"currency"`
	DurationMinutes int32  `json:"durationMinutes"`

	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`

	CustomerName  *string `json:"customerName,omitempty"`
	CustomerEmail *string `json:"customerEmail,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

// adminUser is the platform user list row.
type adminUser struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Phone         *string   `json:"phone,omitempty"`
	PlatformRole  string    `json:"platformRole"`
	EmailVerified bool      `json:"emailVerified"`
	CreatedAt     time.Time `json:"createdAt"`
}
