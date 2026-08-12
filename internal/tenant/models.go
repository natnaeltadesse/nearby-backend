// Package tenant owns providers (the tenants), their members and invitations,
// plus the middleware that turns `x-organization-id` into a verified org scope.
package tenant

import (
	"time"

	"github.com/google/uuid"
)

// Provider statuses.
const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

// Booking modes. `instant` skips `pending` and confirms at creation.
const (
	ModeRequest = "request"
	ModeInstant = "instant"
)

// Member roles, most privileged first.
const (
	RoleOwner = "owner"
	RoleAdmin = "admin"
	RoleStaff = "staff"
)

// Location is a lng/lat pair. Stored as PostGIS geography(Point, 4326).
type Location struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Provider is a tenant: one service business.
type Provider struct {
	ID             uuid.UUID `json:"id"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Phone          *string   `json:"phone,omitempty"`
	Email          *string   `json:"email,omitempty"`
	Description    *string   `json:"description,omitempty"`
	City           *string   `json:"city,omitempty"`
	Address        *string   `json:"address,omitempty"`
	Location       *Location `json:"location,omitempty"`
	Timezone       string    `json:"timezone"`
	LogoURL        *string   `json:"logoUrl,omitempty"`
	LicenseNumber  *string   `json:"licenseNumber,omitempty"`
	Status         string    `json:"status"`
	RatingAvg      float64   `json:"ratingAvg"`
	RatingCount    int32     `json:"ratingCount"`
	BookingMode    string    `json:"bookingMode"`
	MinLeadMinutes int32     `json:"minLeadMinutes"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Member is a user's association with a provider.
type Member struct {
	ProviderID uuid.UUID `json:"providerId"`
	UserID     uuid.UUID `json:"userId"`
	Role       string    `json:"role"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Phone      *string   `json:"phone,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Invitation is a pending offer of membership.
type Invitation struct {
	ID         uuid.UUID  `json:"id"`
	ProviderID uuid.UUID  `json:"providerId"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	AcceptedAt *time.Time `json:"acceptedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	// Token is populated only in the response to creating the invitation —
	// it is never stored in plaintext and never returned again.
	Token string `json:"token,omitempty"`
}

// roleRank orders roles so privilege comparisons are a single integer test.
var roleRank = map[string]int{RoleOwner: 3, RoleAdmin: 2, RoleStaff: 1}

// RoleAtLeast reports whether have is at least as privileged as want.
func RoleAtLeast(have, want string) bool {
	return roleRank[have] >= roleRank[want] && roleRank[have] > 0
}

// ValidRole reports whether role is one of the three member roles.
func ValidRole(role string) bool {
	_, ok := roleRank[role]
	return ok
}
