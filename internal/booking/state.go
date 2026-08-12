// Package booking owns the reservation lifecycle.
//
// Like scheduling/, it is category-blind: it receives a duration, a resource id
// and a time range, and never learns what kind of business it is booking. The
// snapshot columns are why — a booking records what was sold, not a pointer to
// a catalog row that may change underneath it.
package booking

import "github.com/nearby/booking-backend/internal/platform/httpx"

// Booking statuses.
const (
	StatusPending             = "pending"
	StatusConfirmed           = "confirmed"
	StatusInProgress          = "in_progress"
	StatusCompleted           = "completed"
	StatusCancelledByCustomer = "cancelled_by_customer"
	StatusCancelledByProvider = "cancelled_by_provider"
	StatusNoShow              = "no_show"
)

// Event is something that happens to a booking.
type Event string

// Lifecycle events. Cancellation is two events rather than one because who
// cancelled is recorded on the booking and shown to both sides.
const (
	EventConfirm          Event = "confirm"
	EventStart            Event = "start"
	EventComplete         Event = "complete"
	EventNoShow           Event = "no_show"
	EventCancelByCustomer Event = "cancel_by_customer"
	EventCancelByProvider Event = "cancel_by_provider"
)

// transitions is the whole state machine from spec §5.5.
//
//	pending ──confirm──> confirmed ──start──> in_progress ──complete──> completed
//	   │                     │                     │
//	   └──cancel──> cancelled_by_customer / cancelled_by_provider
//	                         └──no_show──> no_show
//
// Anything absent from this table is rejected with INVALID_TRANSITION. Keeping
// it as data rather than a chain of ifs is what makes that guarantee checkable.
var transitions = map[string]map[Event]string{
	StatusPending: {
		EventConfirm:          StatusConfirmed,
		EventCancelByCustomer: StatusCancelledByCustomer,
		EventCancelByProvider: StatusCancelledByProvider,
	},
	StatusConfirmed: {
		EventStart: StatusInProgress,
		// Only a confirmed booking can be a no-show: nobody failed to turn up
		// for an appointment that was never agreed to.
		EventNoShow:           StatusNoShow,
		EventCancelByCustomer: StatusCancelledByCustomer,
		EventCancelByProvider: StatusCancelledByProvider,
	},
	StatusInProgress: {
		EventComplete: StatusCompleted,
		// Cancelling mid-service is unusual but real: the car turns out not to
		// fit in the bay.
		EventCancelByProvider: StatusCancelledByProvider,
	},
	// completed, no_show and both cancellations are terminal.
}

// activeStatuses are the statuses that occupy a resource. They must match the
// WHERE clause of the bookings_no_overlap exclusion constraint exactly — if
// they ever drift, the database and the availability query disagree about what
// "busy" means.
var activeStatuses = map[string]bool{
	StatusPending:    true,
	StatusConfirmed:  true,
	StatusInProgress: true,
}

// Next returns the status that event moves from to, or INVALID_TRANSITION.
func Next(from string, event Event) (string, error) {
	allowed, known := transitions[from]
	if !known {
		return "", httpx.InvalidTransition(from, string(event))
	}
	to, ok := allowed[event]
	if !ok {
		return "", httpx.InvalidTransition(from, string(event))
	}
	return to, nil
}

// IsTerminal reports whether a booking can no longer change state.
func IsTerminal(status string) bool {
	_, hasTransitions := transitions[status]
	return !hasTransitions
}

// IsActive reports whether a booking in this status occupies its resource.
func IsActive(status string) bool { return activeStatuses[status] }

// CancelEventFor maps the canceller onto the right event.
func CancelEventFor(actor string) Event {
	if actor == ActorProvider {
		return EventCancelByProvider
	}
	return EventCancelByCustomer
}

// Who cancelled a booking.
const (
	ActorCustomer = "customer"
	ActorProvider = "provider"
)
