package scheduling

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// DayWindow is one span a single resource is open for on a given date, already
// resolved from business hours and exceptions.
type DayWindow struct {
	ResourceID uuid.UUID
	Opens      TimeOfDay
	Closes     TimeOfDay
}

// BusyInterval is time a resource is already committed to. Instants, not wall
// clock: a booking is an absolute range regardless of what the clock says.
type BusyInterval struct {
	ResourceID uuid.UUID
	Start      time.Time
	End        time.Time
}

// Slot is one bookable start time and the resources that could take it.
//
// Only the start is returned. The client sends a start back, the server
// re-derives the duration and the resource, and the exclusion constraint
// arbitrates — so a stale slot list can never become a double booking.
type Slot struct {
	StartsAt    time.Time   `json:"startsAt"`
	ResourceIDs []uuid.UUID `json:"resourceIds"`
}

// SlotRequest is everything GenerateSlots needs. Note what is absent: no
// service, no category, no price. Just a duration and some ranges.
type SlotRequest struct {
	// Date is the local calendar day to generate for; only its Y/M/D are read.
	Date time.Time
	// Location is the provider's zone. All wall-clock arithmetic happens here,
	// and every instant returned is still absolute.
	Location *time.Location
	// TotalDuration already includes option deltas and the service buffer.
	TotalDuration time.Duration
	// Step is how finely starts are offered, e.g. 15 minutes.
	Step time.Duration
	// EarliestStart drops slots too soon to be honoured (now + min lead).
	EarliestStart time.Time

	Windows []DayWindow
	Busy    []BusyInterval

	// IncludeFullyBooked emits grid positions that are inside opening hours
	// but have no free resource, as slots with an empty ResourceIDs list.
	//
	// The public availability endpoint leaves this false — a customer only
	// wants what they can actually take. booking/ sets it, because it needs to
	// tell "that time is outside our hours" from "someone just took it", and
	// those are different codes to the client.
	IncludeFullyBooked bool
}

// GenerateSlots implements the availability algorithm from spec §9.
//
// Pure: same inputs, same outputs, no clock and no database. That is what
// makes the awkward cases — a booking straddling a window edge, two resources
// offering the same start, a slot that starts inside opening hours but would
// finish after closing — testable without a container.
func GenerateSlots(req SlotRequest) []Slot {
	if req.TotalDuration <= 0 || req.Step <= 0 || len(req.Windows) == 0 {
		return []Slot{}
	}

	location := req.Location
	if location == nil {
		location = time.UTC
	}

	busyByResource := make(map[uuid.UUID][]BusyInterval, len(req.Windows))
	for _, interval := range req.Busy {
		busyByResource[interval.ResourceID] = append(busyByResource[interval.ResourceID], interval)
	}

	year, month, day := req.Date.In(location).Date()

	// Candidate starts are keyed by instant so two resources free at the same
	// moment collapse into one slot offering both.
	candidates := make(map[time.Time][]uuid.UUID)
	// Every grid position inside opening hours, free or not.
	bookable := make(map[time.Time]struct{})

	for _, window := range req.Windows {
		if window.Closes <= window.Opens {
			continue
		}

		// time.Date normalizes minute overflow and resolves the zone offset
		// for that specific instant, so a DST transition inside the day is
		// handled without any special casing.
		windowStart := time.Date(year, month, day, 0, int(window.Opens), 0, 0, location)
		windowEnd := time.Date(year, month, day, 0, int(window.Closes), 0, 0, location)

		busy := busyByResource[window.ResourceID]

		for offset := 0; ; offset += int(req.Step / time.Minute) {
			start := time.Date(year, month, day, 0, int(window.Opens)+offset, 0, 0, location)
			end := start.Add(req.TotalDuration)

			// The service must finish before closing, not merely start before it.
			if end.After(windowEnd) {
				break
			}
			if start.Before(windowStart) {
				continue
			}
			if start.Before(req.EarliestStart) {
				continue
			}

			key := start.UTC()
			// Recorded before the busy check: this position is a legitimate
			// time to ask for, whether or not anything is free to take it.
			bookable[key] = struct{}{}

			if overlapsAny(start, end, busy) {
				continue
			}
			if !containsID(candidates[key], window.ResourceID) {
				candidates[key] = append(candidates[key], window.ResourceID)
			}
		}
	}

	if req.IncludeFullyBooked {
		for start := range bookable {
			if _, ok := candidates[start]; !ok {
				candidates[start] = nil
			}
		}
	}

	slots := make([]Slot, 0, len(candidates))
	for start, resourceIDs := range candidates {
		if resourceIDs == nil {
			resourceIDs = []uuid.UUID{}
		}
		sort.Slice(resourceIDs, func(i, j int) bool {
			return resourceIDs[i].String() < resourceIDs[j].String()
		})
		slots = append(slots, Slot{StartsAt: start, ResourceIDs: resourceIDs})
	}
	sort.Slice(slots, func(i, j int) bool {
		return slots[i].StartsAt.Before(slots[j].StartsAt)
	})
	return slots
}

// overlapsAny reports whether [start, end) intersects any busy interval.
// Half-open on both sides, so a booking ending exactly when the next starts
// does not count as a conflict — matching the `&&` semantics of tstzrange in
// the exclusion constraint.
func overlapsAny(start, end time.Time, busy []BusyInterval) bool {
	for _, interval := range busy {
		if start.Before(interval.End) && interval.Start.Before(end) {
			return true
		}
	}
	return false
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

// ResolveWindows works out each resource's open spans for one date.
//
// Precedence, narrowest first:
//  1. an exception for this resource on this date
//  2. an exception for the whole provider on this date
//  3. business hours for this resource on this weekday
//  4. business hours for the whole provider on this weekday
//
// A closed exception at either scope removes the resource from the day.
func ResolveWindows(
	resourceIDs []uuid.UUID,
	hours []HoursRule,
	exceptions []ExceptionRule,
	weekday int,
) []DayWindow {
	resourceExceptions := make(map[uuid.UUID]ExceptionRule, len(exceptions))
	var providerException *ExceptionRule

	for i, exception := range exceptions {
		if exception.ResourceID != nil {
			resourceExceptions[*exception.ResourceID] = exception
		} else {
			providerException = &exceptions[i]
		}
	}

	resourceHours := make(map[uuid.UUID][]HoursRule, len(resourceIDs))
	var providerHours []HoursRule

	for _, rule := range hours {
		if rule.Weekday != weekday {
			continue
		}
		if rule.ResourceID != nil {
			resourceHours[*rule.ResourceID] = append(resourceHours[*rule.ResourceID], rule)
		} else {
			providerHours = append(providerHours, rule)
		}
	}

	windows := make([]DayWindow, 0, len(resourceIDs))

	for _, resourceID := range resourceIDs {
		exception, hasException := resourceExceptions[resourceID]
		if !hasException && providerException != nil {
			exception, hasException = *providerException, true
		}

		if hasException {
			if exception.IsClosed || exception.Opens == nil || exception.Closes == nil {
				continue
			}
			windows = append(windows, DayWindow{
				ResourceID: resourceID,
				Opens:      *exception.Opens,
				Closes:     *exception.Closes,
			})
			continue
		}

		rules := resourceHours[resourceID]
		if len(rules) == 0 {
			rules = providerHours
		}
		for _, rule := range rules {
			windows = append(windows, DayWindow{
				ResourceID: resourceID,
				Opens:      rule.Opens,
				Closes:     rule.Closes,
			})
		}
	}

	return windows
}
