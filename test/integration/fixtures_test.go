package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixture is a fully set-up tenant: an approved car wash with one service, a
// required vehicle-size group, an optional extras group and two bays open
// every day.
//
// It is built through the API rather than by inserting rows, so every test
// that uses it also re-proves the org surface still works end to end.
type fixture struct {
	admin    testUser
	owner    testUser
	customer testUser

	providerID string
	categoryID string
	serviceID  string

	sedanID string
	suvID   string
	waxID   string

	bay1ID string
	bay2ID string
}

const (
	// The provider's zone. Addis is UTC+3 with no DST, so 08:00 local is
	// always 05:00Z — which keeps the expected values in these tests readable.
	fixtureTimezone = "Africa/Addis_Ababa"
	opensAt         = "08:00"
	closesAt        = "18:00"
)

// bookingDate returns a local date far enough ahead that the minimum lead time
// can never interfere.
func bookingDate() time.Time {
	return time.Now().UTC().AddDate(0, 0, 7)
}

// startAt builds an RFC3339 UTC instant for a local hour on the booking date.
// The provider is at UTC+3, so local 09:00 is 06:00Z.
func startAt(localHour, localMinute int) string {
	date := bookingDate()
	return time.Date(date.Year(), date.Month(), date.Day(),
		localHour-3, localMinute, 0, 0, time.UTC).Format(time.RFC3339)
}

func dateParam() string {
	return bookingDate().Format(time.DateOnly)
}

// newFixture builds the tenant. bookingMode is "instant" or "request".
func newFixture(t *testing.T, s *testServer, bookingMode string) fixture {
	t.Helper()

	f := fixture{}

	// --- platform admin defines the vertical -------------------------------
	f.admin = s.promoteToAdmin(t, s.signUp(t, "Platform Admin", "admin@nearby.et"))
	adminAuth := withToken(f.admin.AccessToken)

	resp := s.post(t, "/api/v1/admin/categories", map[string]any{
		"name": "Car wash",
		"slug": "car-wash",
	}, adminAuth)
	requireStatus(t, resp, http.StatusCreated)
	f.categoryID = resp.Body["id"].(string)

	// vehicle_size is asked at reservation time: it describes the customer's
	// car, not the service on offer.
	resp = s.post(t, "/api/v1/admin/categories/"+f.categoryID+"/attributes", map[string]any{
		"key":       "vehicle_size",
		"label":     "Vehicle size",
		"dataType":  "enum",
		"options":   []string{"sedan", "suv", "pickup"},
		"required":  true,
		"appliesTo": "booking",
	}, adminAuth)
	requireStatus(t, resp, http.StatusCreated)

	resp = s.post(t, "/api/v1/admin/categories/"+f.categoryID+"/attributes", map[string]any{
		"key":        "wash_type",
		"label":      "Wash type",
		"dataType":   "enum",
		"options":    []string{"exterior", "interior", "full"},
		"appliesTo":  "service",
		"filterable": true,
	}, adminAuth)
	requireStatus(t, resp, http.StatusCreated)

	// --- an owner registers a business, admin approves it ------------------
	f.owner = s.signUp(t, "Dawit Bekele", "owner@shinewash.et")

	resp = s.post(t, "/api/v1/me/providers", map[string]any{
		"name":        "Shine Car Wash",
		"slug":        "shine-wash",
		"city":        "Addis Ababa",
		"timezone":    fixtureTimezone,
		"bookingMode": bookingMode,
		"location":    map[string]any{"lat": 8.9950, "lng": 38.7869},
		"categoryIds": []string{f.categoryID},
	}, withToken(f.owner.AccessToken))
	requireStatus(t, resp, http.StatusCreated)
	f.providerID = resp.Body["id"].(string)
	require.Equal(t, "pending", resp.Body["status"], "a new provider must not be live until approved")

	resp = s.post(t, "/api/v1/admin/providers/"+f.providerID+"/approve", nil, adminAuth)
	requireStatus(t, resp, http.StatusOK)
	require.Equal(t, "active", resp.Body["status"])

	// The owner's token predates the membership, so re-authenticate to pick it
	// up in the claims. (The org middleware reads the database either way.)
	f.owner = s.signIn(t, f.owner.Email)
	orgAuth := []option{withToken(f.owner.AccessToken), withOrg(f.providerID)}

	// --- catalog -----------------------------------------------------------
	resp = s.post(t, "/api/v1/org/services", map[string]any{
		"categoryId":      f.categoryID,
		"name":            "Exterior wash",
		"priceCents":      24500,
		"durationMinutes": 45,
		"bufferMinutes":   10,
		"attributes":      map[string]any{"wash_type": "exterior"},
	}, orgAuth...)
	requireStatus(t, resp, http.StatusCreated)
	f.serviceID = resp.Body["id"].(string)

	// Required single-select: the price of a wash depends on the vehicle.
	resp = s.post(t, "/api/v1/org/services/"+f.serviceID+"/option-groups", map[string]any{
		"name":          "Vehicle size",
		"selectionType": "single",
		"isRequired":    true,
		"minSelect":     1,
		"maxSelect":     1,
		"sortOrder":     1,
	}, orgAuth...)
	requireStatus(t, resp, http.StatusCreated)
	sizeGroupID := resp.Body["id"].(string)

	f.sedanID = s.createOption(t, orgAuth, f.serviceID, sizeGroupID, "Sedan", 0, 0)
	f.suvID = s.createOption(t, orgAuth, f.serviceID, sizeGroupID, "SUV", 5000, 10)

	resp = s.post(t, "/api/v1/org/services/"+f.serviceID+"/option-groups", map[string]any{
		"name":          "Extras",
		"selectionType": "multi",
		"sortOrder":     2,
	}, orgAuth...)
	requireStatus(t, resp, http.StatusCreated)
	extrasGroupID := resp.Body["id"].(string)

	f.waxID = s.createOption(t, orgAuth, f.serviceID, extrasGroupID, "Wax", 3000, 20)

	// --- resources and hours ----------------------------------------------
	f.bay1ID = s.createResource(t, orgAuth, "Bay 1", f.serviceID)
	f.bay2ID = s.createResource(t, orgAuth, "Bay 2", f.serviceID)

	// Open every day, so a test never has to care which weekday it runs on.
	for weekday := 0; weekday <= 6; weekday++ {
		resp = s.post(t, "/api/v1/org/business-hours", map[string]any{
			"weekday":  weekday,
			"opensAt":  opensAt,
			"closesAt": closesAt,
		}, orgAuth...)
		requireStatus(t, resp, http.StatusCreated)
	}

	f.customer = s.signUp(t, "Abebe Kebede", "customer@example.et")

	return f
}

func (s *testServer) createOption(
	t *testing.T,
	orgAuth []option,
	serviceID, groupID, name string,
	priceDelta, durationDelta int,
) string {
	t.Helper()

	resp := s.post(t,
		fmt.Sprintf("/api/v1/org/services/%s/option-groups/%s/options", serviceID, groupID),
		map[string]any{
			"name":                 name,
			"priceDeltaCents":      priceDelta,
			"durationDeltaMinutes": durationDelta,
		}, orgAuth...)
	requireStatus(t, resp, http.StatusCreated)

	return resp.Body["id"].(string)
}

func (s *testServer) createResource(t *testing.T, orgAuth []option, name, serviceID string) string {
	t.Helper()

	resp := s.post(t, "/api/v1/org/resources", map[string]any{
		"name":       name,
		"serviceIds": []string{serviceID},
	}, orgAuth...)
	requireStatus(t, resp, http.StatusCreated)

	return resp.Body["id"].(string)
}

// orgAuth returns the header pair for acting as the fixture's owner.
func (f fixture) orgAuth() []option {
	return []option{withToken(f.owner.AccessToken), withOrg(f.providerID)}
}

// book is the happy-path reservation used by several tests.
func (s *testServer) book(t *testing.T, f fixture, start string, optionIDs []string) response {
	t.Helper()

	return s.post(t, "/api/v1/me/bookings", map[string]any{
		"serviceId":  f.serviceID,
		"startsAt":   start,
		"optionIds":  optionIDs,
		"attributes": map[string]any{"vehicle_size": "sedan"},
	}, withToken(f.customer.AccessToken))
}

// slotStarts extracts the start times from an availability response.
func slotStarts(t *testing.T, resp response) []string {
	t.Helper()
	requireStatus(t, resp, http.StatusOK)

	rawSlots, ok := resp.Body["slots"].([]any)
	require.True(t, ok, "availability response has no slots: %s", resp)

	starts := make([]string, 0, len(rawSlots))
	for _, raw := range rawSlots {
		slot := raw.(map[string]any)
		starts = append(starts, slot["startsAt"].(string))
	}
	return starts
}
