// Package scheduling owns resources, opening hours, exceptions and slot
// generation.
//
// It is category-blind by construction: everything it takes is a duration, a
// resource id and a time range. If a `switch category` ever appears in here,
// the catalog attribute model is missing something — fix it there, not here.
package scheduling

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// MinutesPerDay bounds a valid time of day.
const MinutesPerDay = 24 * 60

// TimeOfDay is a wall-clock time within a provider's local day, stored as
// minutes since midnight.
//
// Minutes rather than time.Time because opening hours are not instants: "we
// open at 09:00" is true on every date, in the provider's zone, and turning it
// into an instant is the slot generator's job. It crosses the database
// boundary as an integer and the wire as "HH:MM".
type TimeOfDay int

// ParseTimeOfDay reads "HH:MM" (or "HH:MM:SS", whose seconds are ignored).
func ParseTimeOfDay(raw string) (TimeOfDay, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("time %q must be formatted as HH:MM", raw)
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil || hours < 0 || hours > 24 {
		return 0, fmt.Errorf("time %q has an invalid hour", raw)
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, fmt.Errorf("time %q has an invalid minute", raw)
	}

	total := TimeOfDay(hours*60 + minutes)
	if total > MinutesPerDay {
		return 0, fmt.Errorf("time %q is past the end of the day", raw)
	}
	return total, nil
}

// String renders as "HH:MM".
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", int(t)/60, int(t)%60)
}

// MarshalJSON renders as a "HH:MM" string.
func (t TimeOfDay) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// UnmarshalJSON accepts a "HH:MM" string.
func (t *TimeOfDay) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("time of day must be a string like \"09:30\"")
	}
	parsed, err := ParseTimeOfDay(raw)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// Valid reports whether t is within a single day.
func (t TimeOfDay) Valid() bool { return t >= 0 && t <= MinutesPerDay }
