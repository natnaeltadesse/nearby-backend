package booking

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

func TestHappyPathWalksTheWholeMachine(t *testing.T) {
	status := StatusPending

	for _, step := range []struct {
		event Event
		want  string
	}{
		{EventConfirm, StatusConfirmed},
		{EventStart, StatusInProgress},
		{EventComplete, StatusCompleted},
	} {
		next, err := Next(status, step.event)
		require.NoError(t, err, "%s should accept %s", status, step.event)
		assert.Equal(t, step.want, next)
		status = next
	}

	assert.True(t, IsTerminal(status))
}

func TestCancellationRecordsWhoCancelled(t *testing.T) {
	for _, from := range []string{StatusPending, StatusConfirmed} {
		next, err := Next(from, EventCancelByCustomer)
		require.NoError(t, err)
		assert.Equal(t, StatusCancelledByCustomer, next)

		next, err = Next(from, EventCancelByProvider)
		require.NoError(t, err)
		assert.Equal(t, StatusCancelledByProvider, next)
	}
}

func TestCustomerCannotCancelAServiceAlreadyUnderway(t *testing.T) {
	// The car is on the ramp; only the provider can call it off.
	_, err := Next(StatusInProgress, EventCancelByCustomer)
	assertInvalidTransition(t, err)

	next, err := Next(StatusInProgress, EventCancelByProvider)
	require.NoError(t, err)
	assert.Equal(t, StatusCancelledByProvider, next)
}

func TestNoShowOnlyFromConfirmed(t *testing.T) {
	next, err := Next(StatusConfirmed, EventNoShow)
	require.NoError(t, err)
	assert.Equal(t, StatusNoShow, next)

	// Nobody failed to turn up for an appointment that was never agreed to,
	// and a service in progress plainly had someone there.
	for _, from := range []string{StatusPending, StatusInProgress} {
		_, err := Next(from, EventNoShow)
		assertInvalidTransition(t, err)
	}
}

func TestSkippingAheadIsRejected(t *testing.T) {
	for _, step := range []struct {
		from  string
		event Event
	}{
		{StatusPending, EventStart},
		{StatusPending, EventComplete},
		{StatusConfirmed, EventComplete},
		{StatusConfirmed, EventConfirm},
		{StatusInProgress, EventStart},
	} {
		_, err := Next(step.from, step.event)
		assertInvalidTransition(t, err)
	}
}

func TestTerminalStatusesAcceptNothing(t *testing.T) {
	terminal := []string{
		StatusCompleted, StatusNoShow,
		StatusCancelledByCustomer, StatusCancelledByProvider,
	}
	events := []Event{
		EventConfirm, EventStart, EventComplete, EventNoShow,
		EventCancelByCustomer, EventCancelByProvider,
	}

	for _, status := range terminal {
		assert.True(t, IsTerminal(status), "%s should be terminal", status)
		for _, event := range events {
			_, err := Next(status, event)
			assertInvalidTransition(t, err)
		}
	}
}

func TestActiveStatusesMatchTheExclusionConstraint(t *testing.T) {
	// These three are the WHERE clause of bookings_no_overlap. If this ever
	// drifts, the database and the availability query disagree about "busy"
	// and the constraint silently stops preventing double bookings.
	assert.True(t, IsActive(StatusPending))
	assert.True(t, IsActive(StatusConfirmed))
	assert.True(t, IsActive(StatusInProgress))

	for _, status := range []string{
		StatusCompleted, StatusNoShow,
		StatusCancelledByCustomer, StatusCancelledByProvider,
	} {
		assert.False(t, IsActive(status), "%s must free its slot", status)
	}
}

func TestUnknownStatusIsRejectedRatherThanAssumed(t *testing.T) {
	_, err := Next("wat", EventConfirm)
	assertInvalidTransition(t, err)
}

func TestCancelEventForMapsTheActor(t *testing.T) {
	assert.Equal(t, EventCancelByProvider, CancelEventFor(ActorProvider))
	assert.Equal(t, EventCancelByCustomer, CancelEventFor(ActorCustomer))
	assert.Equal(t, EventCancelByCustomer, CancelEventFor("anything else"))
}

func assertInvalidTransition(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)

	apiErr := httpx.AsError(err)
	assert.Equal(t, httpx.CodeInvalidTransition, apiErr.Code)
	assert.Equal(t, 409, apiErr.Status)
}
