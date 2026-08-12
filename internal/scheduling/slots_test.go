package scheduling

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var addis = func() *time.Location {
	loc, err := time.LoadLocation("Africa/Addis_Ababa")
	if err != nil {
		panic(err)
	}
	return loc
}()

// localDate is the date every case generates for; a Wednesday.
var localDate = time.Date(2026, 8, 19, 0, 0, 0, 0, addis)

func at(hour, minute int) time.Time {
	return time.Date(2026, 8, 19, hour, minute, 0, 0, addis)
}

func startTimes(slots []Slot) []string {
	out := make([]string, 0, len(slots))
	for _, slot := range slots {
		out = append(out, slot.StartsAt.In(addis).Format("15:04"))
	}
	return out
}

func baseRequest(windows []DayWindow, busy []BusyInterval, duration time.Duration) SlotRequest {
	return SlotRequest{
		Date:          localDate,
		Location:      addis,
		TotalDuration: duration,
		Step:          15 * time.Minute,
		// Far in the past, so the lead-time filter is out of the way unless a
		// case sets it deliberately.
		EarliestStart: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		Windows:       windows,
		Busy:          busy,
	}
}

func TestGenerateSlotsStepsAcrossTheWindow(t *testing.T) {
	resource := uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 11 * 60}},
		nil,
		30*time.Minute,
	))

	// 09:00 through 10:30 — the 10:45 start is dropped because it would run
	// past the 11:00 close.
	assert.Equal(t, []string{"09:00", "09:15", "09:30", "09:45", "10:00", "10:15", "10:30"}, startTimes(slots))
	for _, slot := range slots {
		assert.Equal(t, []uuid.UUID{resource}, slot.ResourceIDs)
	}
}

func TestGenerateSlotsRequiresTheWholeDurationBeforeClosing(t *testing.T) {
	resource := uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 10 * 60}},
		nil,
		90*time.Minute, // longer than the window itself
	))

	assert.Empty(t, slots, "a service that cannot finish before closing has no slots")
}

func TestGenerateSlotsExcludesBusyRanges(t *testing.T) {
	resource := uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 12 * 60}},
		[]BusyInterval{{ResourceID: resource, Start: at(10, 0), End: at(11, 0)}},
		60*time.Minute,
	))

	// 09:15, 09:30 and 09:45 would all run into the 10:00 booking.
	assert.Equal(t, []string{"09:00", "11:00"}, startTimes(slots))
}

func TestGenerateSlotsTreatsTouchingBookingsAsFree(t *testing.T) {
	resource := uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 11 * 60}},
		[]BusyInterval{{ResourceID: resource, Start: at(9, 0), End: at(10, 0)}},
		60*time.Minute,
	))

	// A booking ending exactly at 10:00 does not block a 10:00 start — the
	// same half-open semantics the `&&` in the exclusion constraint uses.
	assert.Equal(t, []string{"10:00"}, startTimes(slots))
}

func TestGenerateSlotsMergesResourcesOfferingTheSameStart(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{
			{ResourceID: first, Opens: 9 * 60, Closes: 10 * 60},
			{ResourceID: second, Opens: 9 * 60, Closes: 10 * 60},
		},
		// Only the first bay is busy, so 09:00 survives on the second alone.
		[]BusyInterval{{ResourceID: first, Start: at(9, 0), End: at(9, 30)}},
		30*time.Minute,
	))

	require.Len(t, slots, 3)
	assert.Equal(t, []string{"09:00", "09:15", "09:30"}, startTimes(slots))
	assert.Equal(t, []uuid.UUID{second}, slots[0].ResourceIDs, "09:00 is only free on the second bay")
	assert.ElementsMatch(t, []uuid.UUID{first, second}, slots[2].ResourceIDs, "09:30 is free on both")
}

func TestGenerateSlotsDropsSlotsInsideTheLeadTime(t *testing.T) {
	resource := uuid.New()
	request := baseRequest(
		[]DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 11 * 60}},
		nil,
		60*time.Minute,
	)
	request.EarliestStart = at(9, 40)

	assert.Equal(t, []string{"09:45", "10:00"}, startTimes(GenerateSlots(request)))
}

// booking/ needs to tell "we are closed then" apart from "someone just took
// it", because those are different error codes to the client.
func TestGenerateSlotsCanIncludeFullyBookedPositions(t *testing.T) {
	resource := uuid.New()
	windows := []DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 11 * 60}}
	busy := []BusyInterval{{ResourceID: resource, Start: at(9, 0), End: at(10, 0)}}

	// Browsing hides the taken positions entirely.
	browsing := GenerateSlots(baseRequest(windows, busy, 60*time.Minute))
	assert.Equal(t, []string{"10:00"}, startTimes(browsing))

	booking := baseRequest(windows, busy, 60*time.Minute)
	booking.IncludeFullyBooked = true
	reserving := GenerateSlots(booking)

	// Every position that would finish before closing is a legitimate time to
	// ask for; the ones overlapping the booking simply have nothing free.
	assert.Equal(t, []string{"09:00", "09:15", "09:30", "09:45", "10:00"}, startTimes(reserving))
	for _, slot := range reserving[:4] {
		assert.Empty(t, slot.ResourceIDs, "a full position offers no resource")
	}
	assert.Equal(t, []uuid.UUID{resource}, reserving[4].ResourceIDs)

	// A position outside opening hours is still absent either way, which is
	// what makes the two cases distinguishable.
	for _, slot := range reserving {
		assert.NotEqual(t, "11:00", slot.StartsAt.In(addis).Format("15:04"))
	}
}

func TestGenerateSlotsHandlesSplitWindows(t *testing.T) {
	resource := uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{
			{ResourceID: resource, Opens: 9 * 60, Closes: 10 * 60},  // morning
			{ResourceID: resource, Opens: 14 * 60, Closes: 15 * 60}, // after lunch
		},
		nil,
		60*time.Minute,
	))

	assert.Equal(t, []string{"09:00", "14:00"}, startTimes(slots))
}

func TestGenerateSlotsReturnsInstantsNotWallClock(t *testing.T) {
	resource := uuid.New()
	slots := GenerateSlots(baseRequest(
		[]DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 10 * 60}},
		nil,
		60*time.Minute,
	))

	require.Len(t, slots, 1)
	// Addis is UTC+3 year-round, so 09:00 local is 06:00 UTC on the wire.
	assert.Equal(t, "2026-08-19T06:00:00Z", slots[0].StartsAt.UTC().Format(time.RFC3339))
}

func TestGenerateSlotsRejectsDegenerateInput(t *testing.T) {
	resource := uuid.New()
	windows := []DayWindow{{ResourceID: resource, Opens: 9 * 60, Closes: 10 * 60}}

	assert.Empty(t, GenerateSlots(baseRequest(nil, nil, time.Hour)), "no windows")
	assert.Empty(t, GenerateSlots(baseRequest(windows, nil, 0)), "zero duration")

	zeroStep := baseRequest(windows, nil, time.Hour)
	zeroStep.Step = 0
	assert.Empty(t, GenerateSlots(zeroStep), "zero step")

	inverted := baseRequest([]DayWindow{{ResourceID: resource, Opens: 17 * 60, Closes: 9 * 60}}, nil, time.Hour)
	assert.Empty(t, GenerateSlots(inverted), "window that closes before it opens")
}

// --- window resolution ----------------------------------------------------

func TestResolveWindowsFallsBackToProviderHours(t *testing.T) {
	resource := uuid.New()
	windows := ResolveWindows(
		[]uuid.UUID{resource},
		[]HoursRule{{ResourceID: nil, Weekday: 3, Opens: 9 * 60, Closes: 17 * 60}},
		nil,
		3,
	)

	require.Len(t, windows, 1)
	assert.Equal(t, DayWindow{ResourceID: resource, Opens: 9 * 60, Closes: 17 * 60}, windows[0])
}

func TestResolveWindowsPrefersResourceHours(t *testing.T) {
	resource := uuid.New()
	windows := ResolveWindows(
		[]uuid.UUID{resource},
		[]HoursRule{
			{ResourceID: nil, Weekday: 3, Opens: 9 * 60, Closes: 17 * 60},
			{ResourceID: &resource, Weekday: 3, Opens: 12 * 60, Closes: 20 * 60},
		},
		nil,
		3,
	)

	require.Len(t, windows, 1)
	assert.Equal(t, TimeOfDay(12*60), windows[0].Opens, "the resource's own hours win outright")
	assert.Equal(t, TimeOfDay(20*60), windows[0].Closes)
}

func TestResolveWindowsIgnoresOtherWeekdays(t *testing.T) {
	resource := uuid.New()
	windows := ResolveWindows(
		[]uuid.UUID{resource},
		[]HoursRule{{ResourceID: nil, Weekday: 1, Opens: 9 * 60, Closes: 17 * 60}},
		nil,
		3,
	)

	assert.Empty(t, windows)
}

func TestResolveWindowsClosedExceptionRemovesTheDay(t *testing.T) {
	resource := uuid.New()
	windows := ResolveWindows(
		[]uuid.UUID{resource},
		[]HoursRule{{ResourceID: nil, Weekday: 3, Opens: 9 * 60, Closes: 17 * 60}},
		[]ExceptionRule{{ResourceID: nil, IsClosed: true}},
		3,
	)

	assert.Empty(t, windows, "a provider-wide closure beats the weekly pattern")
}

func TestResolveWindowsOpenExceptionReplacesTheWindow(t *testing.T) {
	resource := uuid.New()
	opens, closes := TimeOfDay(10*60), TimeOfDay(13*60)

	windows := ResolveWindows(
		[]uuid.UUID{resource},
		[]HoursRule{{ResourceID: nil, Weekday: 3, Opens: 9 * 60, Closes: 17 * 60}},
		[]ExceptionRule{{ResourceID: nil, IsClosed: false, Opens: &opens, Closes: &closes}},
		3,
	)

	require.Len(t, windows, 1)
	assert.Equal(t, opens, windows[0].Opens)
	assert.Equal(t, closes, windows[0].Closes)
}

func TestResolveWindowsResourceExceptionBeatsProviderException(t *testing.T) {
	open, closed := uuid.New(), uuid.New()
	opens, closes := TimeOfDay(10*60), TimeOfDay(12*60)

	windows := ResolveWindows(
		[]uuid.UUID{open, closed},
		[]HoursRule{{ResourceID: nil, Weekday: 3, Opens: 9 * 60, Closes: 17 * 60}},
		[]ExceptionRule{
			{ResourceID: nil, IsClosed: true}, // whole provider shut
			// ...except this one bay, which opens late.
			{ResourceID: &open, IsClosed: false, Opens: &opens, Closes: &closes},
		},
		3,
	)

	require.Len(t, windows, 1)
	assert.Equal(t, open, windows[0].ResourceID)
	assert.Equal(t, opens, windows[0].Opens)
}

// --- time of day ----------------------------------------------------------

func TestTimeOfDayRoundTrip(t *testing.T) {
	for _, raw := range []string{"00:00", "09:30", "23:59"} {
		parsed, err := ParseTimeOfDay(raw)
		require.NoError(t, err)
		assert.Equal(t, raw, parsed.String())
	}

	fromPostgres, err := ParseTimeOfDay("09:30:00")
	require.NoError(t, err)
	assert.Equal(t, TimeOfDay(570), fromPostgres)
}

func TestTimeOfDayRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "9", "25:00", "09:60", "nine", "09:30:00:00"} {
		_, err := ParseTimeOfDay(raw)
		assert.Error(t, err, "expected %q to be rejected", raw)
	}
}
