package booking

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
)

// BookedOption is the snapshot of one add-on as it was when reserved.
type BookedOption struct {
	OptionID             *uuid.UUID `json:"optionId,omitempty"`
	Name                 string     `json:"name"`
	PriceDeltaCents      int32      `json:"priceDeltaCents"`
	DurationDeltaMinutes int32      `json:"durationDeltaMinutes"`
}

// ProviderSummary is the slice of provider a customer sees on their booking.
type ProviderSummary struct {
	ID       uuid.UUID `json:"id"`
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	LogoURL  *string   `json:"logoUrl,omitempty"`
	City     *string   `json:"city,omitempty"`
	Timezone string    `json:"timezone"`
}

// Booking is one reservation.
//
// Everything below `ServiceName` is a snapshot taken at reservation time. A
// price change or a renamed service must never rewrite what somebody already
// booked, so nothing here joins back to the live catalog.
type Booking struct {
	ID         uuid.UUID  `json:"id"`
	Code       string     `json:"code"`
	ProviderID uuid.UUID  `json:"providerId"`
	ServiceID  *uuid.UUID `json:"serviceId,omitempty"`
	ResourceID uuid.UUID  `json:"resourceId"`
	CustomerID *uuid.UUID `json:"customerId,omitempty"`

	StartsAt time.Time `json:"startsAt"`
	// EndsAt includes the service's buffer, which is why it is not simply
	// startsAt + durationMinutes.
	EndsAt time.Time `json:"endsAt"`
	Status string    `json:"status"`

	ServiceName     string         `json:"serviceName"`
	PriceCents      int32          `json:"priceCents"`
	Currency        string         `json:"currency"`
	DurationMinutes int32          `json:"durationMinutes"`
	Attributes      map[string]any `json:"attributes"`

	CustomerNote  *string `json:"customerNote,omitempty"`
	CustomerName  *string `json:"customerName,omitempty"`
	CustomerPhone *string `json:"customerPhone,omitempty"`
	CancelledBy   *string `json:"cancelledBy,omitempty"`
	CancelReason  *string `json:"cancelReason,omitempty"`

	Options      []BookedOption   `json:"options"`
	Provider     *ProviderSummary `json:"provider,omitempty"`
	ResourceName *string          `json:"resourceName,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Stats is the provider dashboard summary.
type Stats struct {
	Pending               int64 `json:"pending"`
	Confirmed             int64 `json:"confirmed"`
	InProgress            int64 `json:"inProgress"`
	Completed             int64 `json:"completed"`
	Cancelled             int64 `json:"cancelled"`
	NoShow                int64 `json:"noShow"`
	CompletedRevenueCents int64 `json:"completedRevenueCents"`
}

// codeAlphabet omits I, O, 0, 1 and U: the reference gets read down a phone
// line and written on a windscreen, so lookalike characters are a support cost.
const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTVWXYZ"

const codeLength = 4

// newBookingCode returns a short human reference such as "BK-7QK2".
//
// Random rather than sequential so a customer cannot infer how many bookings a
// provider takes. Collisions are handled by retrying against the UNIQUE index,
// not by hoping: 31^4 is only ~920k combinations.
func newBookingCode() (string, error) {
	buf := make([]byte, codeLength)
	limit := big.NewInt(int64(len(codeAlphabet)))

	for i := range buf {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("booking: generate code: %w", err)
		}
		buf[i] = codeAlphabet[n.Int64()]
	}
	return "BK-" + string(buf), nil
}
