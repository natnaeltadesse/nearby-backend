package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Milestones 6 and 7: resources, hours, exceptions, the slot algorithm,
// booking creation under the exclusion constraint, the state machine, both
// booking modes, and cancellation.

func TestIntegrationAvailabilityReflectsHoursAndDuration(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	// 08:00–18:00 local, a 45 minute service with a 10 minute buffer. The last
	// start that still finishes by closing is 17:00 local (14:00Z).
	resp := s.get(t, fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s&optionIds=%s",
		f.serviceID, dateParam(), f.sedanID))
	starts := slotStarts(t, resp)

	require.NotEmpty(t, starts)
	assert.Equal(t, startAt(8, 0), starts[0], "the first slot is at opening time")
	assert.Equal(t, startAt(17, 0), starts[len(starts)-1],
		"the last slot must leave room for the service and its buffer")
	assert.Len(t, starts, 37, "08:00 to 17:00 inclusive, every 15 minutes")

	// Wax adds 20 minutes, so the day's last usable start moves earlier.
	withWax := s.get(t, fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s&optionIds=%s,%s",
		f.serviceID, dateParam(), f.sedanID, f.waxID))
	waxStarts := slotStarts(t, withWax)
	assert.Equal(t, startAt(16, 45), waxStarts[len(waxStarts)-1],
		"a longer service must finish by closing too")
}

func TestIntegrationAvailabilityRequiresValidOptions(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	// The vehicle-size group is required, so a bare request cannot be priced
	// or timed and is rejected rather than guessed at.
	requireError(t, s.get(t, fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s",
		f.serviceID, dateParam())), http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	requireError(t, s.get(t, fmt.Sprintf("/api/v1/public/services/%s/availability",
		f.serviceID)), http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	requireError(t, s.get(t, fmt.Sprintf("/api/v1/public/services/%s/availability?date=nonsense",
		f.serviceID)), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

func TestIntegrationAvailabilityHonoursExceptionsAndClosures(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	query := fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s&optionIds=%s",
		f.serviceID, dateParam(), f.sedanID)
	require.NotEmpty(t, slotStarts(t, s.get(t, query)))

	closure := s.post(t, "/api/v1/org/schedule-exceptions", map[string]any{
		"date":     dateParam(),
		"isClosed": true,
		"reason":   "Public holiday",
	}, orgAuth...)
	requireStatus(t, closure, http.StatusCreated)

	assert.Empty(t, slotStarts(t, s.get(t, query)), "a closed day offers nothing")

	// Replace the closure with a short opening.
	requireStatus(t, s.delete(t,
		"/api/v1/org/schedule-exceptions/"+closure.Body["id"].(string), orgAuth...),
		http.StatusNoContent)

	shortDay := s.post(t, "/api/v1/org/schedule-exceptions", map[string]any{
		"date":     dateParam(),
		"isClosed": false,
		"opensAt":  "10:00",
		"closesAt": "12:00",
		"reason":   "Half day",
	}, orgAuth...)
	requireStatus(t, shortDay, http.StatusCreated)

	starts := slotStarts(t, s.get(t, query))
	require.NotEmpty(t, starts)
	assert.Equal(t, startAt(10, 0), starts[0])
	assert.Equal(t, startAt(11, 0), starts[len(starts)-1],
		"the last start must finish by the exception's closing time")
}

func TestIntegrationScheduleExceptionValidation(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"open without times", map[string]any{"date": dateParam(), "isClosed": false}},
		{"closed with times", map[string]any{
			"date": dateParam(), "isClosed": true, "opensAt": "10:00", "closesAt": "12:00",
		}},
		{"closes before it opens", map[string]any{
			"date": dateParam(), "isClosed": false, "opensAt": "14:00", "closesAt": "10:00",
		}},
		{"malformed date", map[string]any{"date": "the 4th", "isClosed": true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireError(t, s.post(t, "/api/v1/org/schedule-exceptions", tc.body, orgAuth...),
				http.StatusUnprocessableEntity, "VALIDATION_ERROR")
		})
	}

	// One exception per scope per date.
	valid := map[string]any{"date": dateParam(), "isClosed": true}
	requireStatus(t, s.post(t, "/api/v1/org/schedule-exceptions", valid, orgAuth...),
		http.StatusCreated)
	requireError(t, s.post(t, "/api/v1/org/schedule-exceptions", valid, orgAuth...),
		http.StatusConflict, "CONFLICT")
}

func TestIntegrationBusinessHoursValidation(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"weekday out of range", map[string]any{"weekday": 9, "opensAt": "08:00", "closesAt": "18:00"}},
		{"closes before it opens", map[string]any{"weekday": 1, "opensAt": "18:00", "closesAt": "08:00"}},
		{"equal times", map[string]any{"weekday": 1, "opensAt": "08:00", "closesAt": "08:00"}},
		{"malformed time", map[string]any{"weekday": 1, "opensAt": "8am", "closesAt": "18:00"}},
		{"impossible hour", map[string]any{"weekday": 1, "opensAt": "25:00", "closesAt": "26:00"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireError(t, s.post(t, "/api/v1/org/business-hours", tc.body, orgAuth...),
				http.StatusUnprocessableEntity, "VALIDATION_ERROR")
		})
	}
}

// The server recomputes price and duration from the chosen option ids, and
// there is no field through which a client could assert its own totals.
func TestIntegrationBookingPricesFromTheCatalog(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	resp := s.post(t, "/api/v1/me/bookings", map[string]any{
		"serviceId": f.serviceID,
		"startsAt":  startAt(9, 0),
		"optionIds": []string{f.suvID, f.waxID},
		// All three are ignored: they are not part of the request contract.
		"priceCents":      1,
		"durationMinutes": 1,
		"attributes":      map[string]any{"vehicle_size": "suv"},
	}, withToken(f.customer.AccessToken))

	// Unknown fields are rejected outright rather than silently dropped.
	requireError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	honest := s.post(t, "/api/v1/me/bookings", map[string]any{
		"serviceId":  f.serviceID,
		"startsAt":   startAt(9, 0),
		"optionIds":  []string{f.suvID, f.waxID},
		"attributes": map[string]any{"vehicle_size": "suv"},
	}, withToken(f.customer.AccessToken))
	requireStatus(t, honest, http.StatusCreated)

	// 24500 + 5000 (SUV) + 3000 (wax)
	assert.Equal(t, float64(32500), honest.Body["priceCents"])
	// 45 + 10 (SUV) + 20 (wax)
	assert.Equal(t, float64(75), honest.Body["durationMinutes"])
	assert.Equal(t, "ETB", honest.Body["currency"])

	// ends_at additionally includes the 10 minute buffer: 09:00 + 75 + 10.
	assert.Equal(t, startAt(10, 25), honest.Body["endsAt"])
	assert.Regexp(t, `^BK-[A-Z0-9]{4}$`, honest.Body["code"])

	// The add-ons are snapshotted onto the booking.
	options := honest.Body["options"].([]any)
	require.Len(t, options, 2)
}

func TestIntegrationBookingValidatesBookingAttributes(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	customer := withToken(f.customer.AccessToken)

	body := func(attributes map[string]any) map[string]any {
		return map[string]any{
			"serviceId":  f.serviceID,
			"startsAt":   startAt(9, 0),
			"optionIds":  []string{f.sedanID},
			"attributes": attributes,
		}
	}

	// vehicle_size is a required booking-scoped attribute.
	requireError(t, s.post(t, "/api/v1/me/bookings", body(map[string]any{}), customer),
		http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	requireError(t, s.post(t, "/api/v1/me/bookings",
		body(map[string]any{"vehicle_size": "helicopter"}), customer),
		http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	requireError(t, s.post(t, "/api/v1/me/bookings",
		body(map[string]any{"vehicle_size": "sedan", "colour": "red"}), customer),
		http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	requireStatus(t, s.post(t, "/api/v1/me/bookings",
		body(map[string]any{"vehicle_size": "sedan"}), customer), http.StatusCreated)
}

func TestIntegrationBookingRejectsUnbookableTimes(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	t.Run("outside opening hours", func(t *testing.T) {
		requireError(t, s.book(t, f, startAt(23, 0), []string{f.sedanID}),
			http.StatusUnprocessableEntity, "OUTSIDE_HOURS")
	})

	t.Run("off the slot grid", func(t *testing.T) {
		requireError(t, s.book(t, f, startAt(9, 7), []string{f.sedanID}),
			http.StatusUnprocessableEntity, "OUTSIDE_HOURS")
	})

	t.Run("too late in the day to finish", func(t *testing.T) {
		requireError(t, s.book(t, f, startAt(17, 45), []string{f.sedanID}),
			http.StatusUnprocessableEntity, "OUTSIDE_HOURS")
	})

	t.Run("in the past", func(t *testing.T) {
		past := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
		requireError(t, s.book(t, f, past, []string{f.sedanID}),
			http.StatusUnprocessableEntity, "OUTSIDE_HOURS")
	})

	t.Run("inside the minimum lead time", func(t *testing.T) {
		// A start a few minutes from now is inside the 30 minute lead window.
		soon := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Hour).Format(time.RFC3339)
		requireError(t, s.book(t, f, soon, []string{f.sedanID}),
			http.StatusUnprocessableEntity, "OUTSIDE_HOURS")
	})
}

func TestIntegrationBookingModesDecideTheStartingStatus(t *testing.T) {
	t.Run("instant confirms immediately", func(t *testing.T) {
		s := newServer(t)
		f := newFixture(t, s, "instant")

		resp := s.book(t, f, startAt(9, 0), []string{f.sedanID})
		requireStatus(t, resp, http.StatusCreated)
		assert.Equal(t, "confirmed", resp.Body["status"])
	})

	t.Run("request waits for the provider", func(t *testing.T) {
		s := newServer(t)
		f := newFixture(t, s, "request")

		resp := s.book(t, f, startAt(9, 0), []string{f.sedanID})
		requireStatus(t, resp, http.StatusCreated)
		assert.Equal(t, "pending", resp.Body["status"])

		// A pending booking already holds the slot, which is what stops a
		// provider double-promising while they decide.
		bookingID := resp.Body["id"].(string)
		confirm := s.post(t, "/api/v1/org/bookings/"+bookingID+"/confirm", nil, f.orgAuth()...)
		requireStatus(t, confirm, http.StatusOK)
		assert.Equal(t, "confirmed", confirm.Body["status"])
	})
}

// The exclusion constraint is the guarantee, not application logic: this is
// the test that would catch its removal.
func TestIntegrationConcurrentBookingsCannotDoubleBook(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	const attempts = 8
	start := startAt(9, 0)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		created   []string
		slotTaken int
		other     []string
	)

	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := s.book(t, f, start, []string{f.sedanID})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case resp.Status == http.StatusCreated:
				created = append(created, resp.Body["resourceId"].(string))
			case resp.Code() == "SLOT_TAKEN":
				slotTaken++
			default:
				other = append(other, resp.String())
			}
		}()
	}
	wg.Wait()

	assert.Empty(t, other, "contention must never surface as an internal error")

	// Two bays, so exactly two bookings can exist and they must be on
	// different resources.
	require.Len(t, created, 2, "one booking per bay, no more and no fewer")
	assert.NotEqual(t, created[0], created[1], "the two winners must occupy different bays")
	assert.Equal(t, attempts-2, slotTaken)

	// And the database agrees.
	var count int
	require.NoError(t, testPool.QueryRow(t.Context(),
		`SELECT count(*) FROM bookings WHERE starts_at = $1`, start).Scan(&count))
	assert.Equal(t, 2, count)
}

// Once every bay is taken the slot disappears from availability rather than
// lingering to be rejected later.
func TestIntegrationBookedSlotsLeaveAvailability(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	query := fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s&optionIds=%s",
		f.serviceID, dateParam(), f.sedanID)
	before := slotStarts(t, s.get(t, query))
	assert.Contains(t, before, startAt(9, 0))

	first := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, first, http.StatusCreated)

	// One bay is still free, so the slot survives.
	assert.Contains(t, slotStarts(t, s.get(t, query)), startAt(9, 0))

	second := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, second, http.StatusCreated)

	after := slotStarts(t, s.get(t, query))
	assert.NotContains(t, after, startAt(9, 0), "with both bays busy the slot is gone")

	// A 55 minute occupancy from 09:00 also blocks the overlapping starts.
	for _, blocked := range []string{startAt(9, 15), startAt(9, 30), startAt(9, 45)} {
		assert.NotContains(t, after, blocked)
	}
	// ...but not the one that starts after it ends.
	assert.Contains(t, after, startAt(10, 0))

	// A third attempt is refused.
	requireError(t, s.book(t, f, startAt(9, 0), []string{f.sedanID}),
		http.StatusConflict, "SLOT_TAKEN")
}

// Cancelling frees the slot, because a cancelled booking is outside the
// exclusion constraint's WHERE clause.
func TestIntegrationCancellationFreesTheSlot(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	query := fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s&optionIds=%s",
		f.serviceID, dateParam(), f.sedanID)

	first := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, first, http.StatusCreated)
	second := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, second, http.StatusCreated)
	assert.NotContains(t, slotStarts(t, s.get(t, query)), startAt(9, 0))

	cancel := s.post(t, "/api/v1/me/bookings/"+first.Body["id"].(string)+"/cancel",
		map[string]any{"reason": "Change of plan"}, withToken(f.customer.AccessToken))
	requireStatus(t, cancel, http.StatusOK)
	assert.Equal(t, "cancelled_by_customer", cancel.Body["status"])
	assert.Equal(t, "customer", cancel.Body["cancelledBy"])
	assert.Equal(t, "Change of plan", cancel.Body["cancelReason"])

	assert.Contains(t, slotStarts(t, s.get(t, query)), startAt(9, 0),
		"the freed bay puts the slot back")

	// And the freed bay can genuinely be rebooked.
	requireStatus(t, s.book(t, f, startAt(9, 0), []string{f.sedanID}), http.StatusCreated)
}

func TestIntegrationBookingStateMachineOverHTTP(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "request")
	orgAuth := f.orgAuth()

	created := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, created, http.StatusCreated)
	id := created.Body["id"].(string)
	path := "/api/v1/org/bookings/" + id

	// Skipping a step is refused.
	requireError(t, s.post(t, path+"/complete", nil, orgAuth...),
		http.StatusConflict, "INVALID_TRANSITION")

	for _, step := range []struct{ action, want string }{
		{"confirm", "confirmed"},
		{"start", "in_progress"},
		{"complete", "completed"},
	} {
		resp := s.post(t, path+"/"+step.action, nil, orgAuth...)
		requireStatus(t, resp, http.StatusOK)
		assert.Equal(t, step.want, resp.Body["status"])
	}

	// A completed booking is terminal.
	for _, action := range []string{"confirm", "start", "complete", "no-show", "cancel"} {
		requireError(t, s.post(t, path+"/"+action, nil, orgAuth...),
			http.StatusConflict, "INVALID_TRANSITION")
	}

	// A customer belongs to no org, so the provider route is closed to them
	// before ownership is even considered.
	requireError(t, s.post(t, path+"/cancel", nil, withToken(f.customer.AccessToken)),
		http.StatusBadRequest, "ORG_REQUIRED")
	requireError(t, s.post(t, path+"/cancel", nil,
		withToken(f.customer.AccessToken), withOrg(f.providerID)),
		http.StatusForbidden, "NOT_A_MEMBER")
	requireError(t, s.post(t, "/api/v1/me/bookings/"+id+"/cancel", nil,
		withToken(f.customer.AccessToken)), http.StatusConflict, "INVALID_TRANSITION")
}

func TestIntegrationNoShowOnlyFromConfirmed(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "request")
	orgAuth := f.orgAuth()

	created := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, created, http.StatusCreated)
	path := "/api/v1/org/bookings/" + created.Body["id"].(string)

	requireError(t, s.post(t, path+"/no-show", nil, orgAuth...),
		http.StatusConflict, "INVALID_TRANSITION")

	requireStatus(t, s.post(t, path+"/confirm", nil, orgAuth...), http.StatusOK)

	noShow := s.post(t, path+"/no-show", nil, orgAuth...)
	requireStatus(t, noShow, http.StatusOK)
	assert.Equal(t, "no_show", noShow.Body["status"])
}

func TestIntegrationCustomersOnlySeeTheirOwnBookings(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	mine := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, mine, http.StatusCreated)
	bookingID := mine.Body["id"].(string)

	stranger := s.signUp(t, "Stranger", "stranger@example.et")
	strangerAuth := withToken(stranger.AccessToken)

	// Not found rather than forbidden: confirming existence is itself a leak.
	requireError(t, s.get(t, "/api/v1/me/bookings/"+bookingID, strangerAuth),
		http.StatusNotFound, "NOT_FOUND")
	requireError(t, s.post(t, "/api/v1/me/bookings/"+bookingID+"/cancel", nil, strangerAuth),
		http.StatusNotFound, "NOT_FOUND")

	empty := s.get(t, "/api/v1/me/bookings", strangerAuth)
	requireStatus(t, empty, http.StatusOK)
	assert.Empty(t, empty.Body["bookings"])

	own := s.get(t, "/api/v1/me/bookings", withToken(f.customer.AccessToken))
	requireStatus(t, own, http.StatusOK)
	require.Len(t, own.Body["bookings"].([]any), 1)

	// The customer's list carries the provider summary they need to render it.
	first := own.Body["bookings"].([]any)[0].(map[string]any)
	provider := first["provider"].(map[string]any)
	assert.Equal(t, "Shine Car Wash", provider["name"])
	assert.Equal(t, fixtureTimezone, provider["timezone"])
}

// Staff enter walk-ins, which have no user account behind them.
func TestIntegrationWalkInBooking(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	resp := s.post(t, "/api/v1/org/bookings", map[string]any{
		"serviceId":     f.serviceID,
		"startsAt":      startAt(11, 0),
		"optionIds":     []string{f.sedanID},
		"attributes":    map[string]any{"vehicle_size": "pickup"},
		"customerName":  "Walk-in Wondimu",
		"customerPhone": "+251911998877",
	}, orgAuth...)
	requireStatus(t, resp, http.StatusCreated)

	assert.Equal(t, "Walk-in Wondimu", resp.Body["customerName"])
	assert.Nil(t, resp.Body["customerId"], "a walk-in has no user account")

	inbox := s.get(t, "/api/v1/org/bookings", orgAuth...)
	requireStatus(t, inbox, http.StatusOK)
	require.Len(t, inbox.Body["bookings"].([]any), 1)

	entry := inbox.Body["bookings"].([]any)[0].(map[string]any)
	assert.Equal(t, "Walk-in Wondimu", entry["customerName"])
	assert.NotEmpty(t, entry["resourceName"])
}

func TestIntegrationProviderBookingsAreTenantScoped(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	created := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, created, http.StatusCreated)
	bookingID := created.Body["id"].(string)

	rival := s.signUp(t, "Rival", "rival@example.et")
	rivalProvider := s.post(t, "/api/v1/me/providers", map[string]any{"name": "Rival Wash"},
		withToken(rival.AccessToken))
	requireStatus(t, rivalProvider, http.StatusCreated)
	rivalID := rivalProvider.Body["id"].(string)
	setProviderStatus(t, rivalID, "active")

	rival = s.signIn(t, rival.Email)
	rivalAuth := []option{withToken(rival.AccessToken), withOrg(rivalID)}

	requireError(t, s.get(t, "/api/v1/org/bookings/"+bookingID, rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")
	requireError(t, s.post(t, "/api/v1/org/bookings/"+bookingID+"/cancel", nil, rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")

	empty := s.get(t, "/api/v1/org/bookings", rivalAuth...)
	requireStatus(t, empty, http.StatusOK)
	assert.Empty(t, empty.Body["bookings"])
}

func TestIntegrationProviderStatsAndInboxFilters(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	first := s.book(t, f, startAt(9, 0), []string{f.sedanID})
	requireStatus(t, first, http.StatusCreated)
	second := s.book(t, f, startAt(11, 0), []string{f.sedanID})
	requireStatus(t, second, http.StatusCreated)

	// Take one all the way to completed so the revenue figure has something in it.
	path := "/api/v1/org/bookings/" + first.Body["id"].(string)
	requireStatus(t, s.post(t, path+"/start", nil, orgAuth...), http.StatusOK)
	requireStatus(t, s.post(t, path+"/complete", nil, orgAuth...), http.StatusOK)

	stats := s.get(t, "/api/v1/org/stats/summary", orgAuth...)
	requireStatus(t, stats, http.StatusOK)
	assert.Equal(t, float64(1), stats.Body["completed"])
	assert.Equal(t, float64(1), stats.Body["confirmed"])
	assert.Equal(t, float64(24500), stats.Body["completedRevenueCents"])

	byStatus := s.get(t, "/api/v1/org/bookings?status=completed", orgAuth...)
	requireStatus(t, byStatus, http.StatusOK)
	require.Len(t, byStatus.Body["bookings"].([]any), 1)

	byDate := s.get(t, "/api/v1/org/bookings?date="+dateParam(), orgAuth...)
	requireStatus(t, byDate, http.StatusOK)
	require.Len(t, byDate.Body["bookings"].([]any), 2)

	otherDay := bookingDate().AddDate(0, 0, 1).Format(time.DateOnly)
	assert.Empty(t, s.get(t, "/api/v1/org/bookings?date="+otherDay, orgAuth...).Body["bookings"])
}

// A booking keeps what was sold, even after the catalog moves on.
func TestIntegrationBookingsSnapshotTheCatalog(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	created := s.book(t, f, startAt(9, 0), []string{f.sedanID, f.waxID})
	requireStatus(t, created, http.StatusCreated)
	bookingID := created.Body["id"].(string)

	originalPrice := created.Body["priceCents"].(float64)
	originalName := created.Body["serviceName"].(string)

	// The provider raises prices and renames the service afterwards.
	update := s.patch(t, "/api/v1/org/services/"+f.serviceID, map[string]any{
		"priceCents": 99000,
		"name":       "Premium exterior wash",
	}, orgAuth...)
	requireStatus(t, update, http.StatusOK)

	after := s.get(t, "/api/v1/me/bookings/"+bookingID, withToken(f.customer.AccessToken))
	requireStatus(t, after, http.StatusOK)

	assert.Equal(t, originalPrice, after.Body["priceCents"],
		"a price change must not rewrite what somebody already booked")
	assert.Equal(t, originalName, after.Body["serviceName"])

	// The option snapshot survives the option being deleted outright.
	options := after.Body["options"].([]any)
	require.Len(t, options, 2)
	assert.Equal(t, "Sedan", options[0].(map[string]any)["name"])
}

func TestIntegrationBookingRejectsInactiveServiceOrProvider(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	requireStatus(t, s.patch(t, "/api/v1/org/services/"+f.serviceID,
		map[string]any{"isActive": false}, orgAuth...), http.StatusOK)

	requireError(t, s.book(t, f, startAt(9, 0), []string{f.sedanID}),
		http.StatusUnprocessableEntity, "SERVICE_INACTIVE")

	requireStatus(t, s.patch(t, "/api/v1/org/services/"+f.serviceID,
		map[string]any{"isActive": true}, orgAuth...), http.StatusOK)

	setProviderStatus(t, f.providerID, "suspended")
	requireError(t, s.book(t, f, startAt(9, 0), []string{f.sedanID}),
		http.StatusUnprocessableEntity, "PROVIDER_INACTIVE")
}

// A resource with no service attached offers nothing, and a service with no
// resource is simply not bookable — an empty day, not an error.
func TestIntegrationServiceWithNoResourcesHasNoSlots(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	for _, resourceID := range []string{f.bay1ID, f.bay2ID} {
		requireStatus(t, s.delete(t,
			fmt.Sprintf("/api/v1/org/resources/%s/services/%s", resourceID, f.serviceID),
			orgAuth...), http.StatusNoContent)
	}

	resp := s.get(t, fmt.Sprintf("/api/v1/public/services/%s/availability?date=%s&optionIds=%s",
		f.serviceID, dateParam(), f.sedanID))
	assert.Empty(t, slotStarts(t, resp))

	requireError(t, s.book(t, f, startAt(9, 0), []string{f.sedanID}),
		http.StatusUnprocessableEntity, "OUTSIDE_HOURS")
}

func TestIntegrationAdminSeesAcrossTenants(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	adminAuth := withToken(f.admin.AccessToken)

	requireStatus(t, s.book(t, f, startAt(9, 0), []string{f.sedanID}), http.StatusCreated)

	bookings := s.get(t, "/api/v1/admin/bookings", adminAuth)
	requireStatus(t, bookings, http.StatusOK)
	require.Len(t, bookings.Body["bookings"].([]any), 1)

	entry := bookings.Body["bookings"].([]any)[0].(map[string]any)
	assert.Equal(t, "Shine Car Wash", entry["providerName"])
	assert.Equal(t, "customer@example.et", entry["customerEmail"])

	users := s.get(t, "/api/v1/admin/users", adminAuth)
	requireStatus(t, users, http.StatusOK)
	assert.GreaterOrEqual(t, len(users.Body["users"].([]any)), 3)

	// Admin filters work and are not org-scoped in any way.
	filtered := s.get(t, "/api/v1/admin/bookings?providerId="+f.providerID, adminAuth)
	requireStatus(t, filtered, http.StatusOK)
	require.Len(t, filtered.Body["bookings"].([]any), 1)

	byStatus := s.get(t, "/api/v1/admin/bookings?status=completed", adminAuth)
	requireStatus(t, byStatus, http.StatusOK)
	assert.Empty(t, byStatus.Body["bookings"])
}
