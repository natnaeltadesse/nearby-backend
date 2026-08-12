package catalog

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/scheduling"
)

// ResolveScheduling implements scheduling.ServiceResolver.
//
// The direction of this dependency is deliberate: scheduling declares the
// narrow thing it needs (a duration, a provider, a timezone) and catalog —
// which owns durations, buffers and option deltas — supplies it. Reversing it
// would put a service lookup inside the slot generator and give it a reason to
// care what a category is.
func (c *Catalog) ResolveScheduling(ctx context.Context, serviceID uuid.UUID, optionIDs []uuid.UUID) (scheduling.ServiceScheduling, error) {
	service, err := c.GetServiceWithProvider(ctx, serviceID)
	if err != nil {
		return scheduling.ServiceScheduling{}, err
	}

	quote, err := c.QuoteFor(ctx, service.Service, optionIDs)
	if err != nil {
		return scheduling.ServiceScheduling{}, err
	}

	// Total duration is what the resource is actually occupied for: the
	// service, plus whatever the chosen options add, plus the buffer.
	total := time.Duration(quote.DurationMinutes+service.BufferMinutes) * time.Minute

	resolved := scheduling.ServiceScheduling{
		ServiceID:      service.ID,
		ProviderID:     service.ProviderID,
		TotalDuration:  total,
		Timezone:       service.Timezone,
		MinLeadMinutes: service.MinLeadMinutes,
		Bookable:       true,
	}

	switch {
	case !service.IsActive:
		resolved.Bookable = false
		resolved.UnbookableCode = httpx.CodeServiceInactive
	case service.ProviderStatus != "active":
		resolved.Bookable = false
		resolved.UnbookableCode = httpx.CodeProviderInactive
	}

	return resolved, nil
}
