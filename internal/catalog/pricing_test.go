package catalog

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// carWash builds the launch tenant's exterior wash: a required vehicle-size
// group and an optional extras group, exactly as spec §5.3 tabulates it.
type carWash struct {
	service  Service
	groups   []OptionGroup
	sedan    uuid.UUID
	suv      uuid.UUID
	wax      uuid.UUID
	interior uuid.UUID
}

func newCarWash() carWash {
	serviceID := uuid.New()
	sizeGroup, extrasGroup := uuid.New(), uuid.New()
	sedan, suv := uuid.New(), uuid.New()
	wax, interior := uuid.New(), uuid.New()

	return carWash{
		service: Service{
			ID: serviceID, Name: "Exterior wash",
			PriceCents: 24500, Currency: "ETB", DurationMinutes: 45, BufferMinutes: 10,
		},
		sedan: sedan, suv: suv, wax: wax, interior: interior,
		groups: []OptionGroup{
			{
				ID: sizeGroup, ServiceID: serviceID, Name: "Vehicle size",
				SelectionType: SelectionSingle, IsRequired: true, MinSelect: 1,
				Options: []Option{
					{ID: sedan, GroupID: sizeGroup, Name: "Sedan", IsActive: true},
					{ID: suv, GroupID: sizeGroup, Name: "SUV", PriceDeltaCents: 5000, IsActive: true},
				},
			},
			{
				ID: extrasGroup, ServiceID: serviceID, Name: "Extras",
				SelectionType: SelectionMulti,
				Options: []Option{
					{ID: wax, GroupID: extrasGroup, Name: "Wax",
						PriceDeltaCents: 3000, DurationDeltaMinutes: 20, IsActive: true},
					{ID: interior, GroupID: extrasGroup, Name: "Interior",
						PriceDeltaCents: 2500, DurationDeltaMinutes: 15, IsActive: true},
				},
			},
		},
	}
}

func assertRejected(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)

	apiErr := httpx.AsError(err)
	assert.Equal(t, httpx.CodeValidationError, apiErr.Code)
	assert.Contains(t, apiErr.Message, contains)
}

func TestBuildQuoteSumsDeltasFromTheCatalog(t *testing.T) {
	wash := newCarWash()

	quote, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.suv, wash.wax, wash.interior})
	require.NoError(t, err)

	// 24500 + 5000 + 3000 + 2500
	assert.Equal(t, int32(35000), quote.PriceCents)
	// 45 + 20 + 15 — the 10 minute buffer is a scheduling concern, not a price one.
	assert.Equal(t, int32(80), quote.DurationMinutes)
	assert.Equal(t, "ETB", quote.Currency)
	assert.Len(t, quote.Options, 3)
}

func TestBuildQuoteIgnoresWhateverTheClientClaimsATotalIs(t *testing.T) {
	wash := newCarWash()

	// BuildQuote's signature is the guarantee: ids in, money out. There is no
	// parameter through which a client could assert a price at all.
	quote, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan})
	require.NoError(t, err)

	assert.Equal(t, wash.service.PriceCents, quote.PriceCents)
	assert.Equal(t, wash.service.DurationMinutes, quote.DurationMinutes)
}

func TestBuildQuoteRequiresRequiredGroups(t *testing.T) {
	wash := newCarWash()

	_, err := BuildQuote(wash.service, wash.groups, nil)
	assertRejected(t, err, "Vehicle size")

	_, err = BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.wax})
	assertRejected(t, err, "Vehicle size")
}

func TestBuildQuoteRejectsTwoChoicesInASingleSelectGroup(t *testing.T) {
	wash := newCarWash()

	_, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, wash.suv})
	assertRejected(t, err, "choose only one")
}

func TestBuildQuoteEnforcesMaxSelect(t *testing.T) {
	wash := newCarWash()
	limit := int32(1)
	wash.groups[1].MaxSelect = &limit

	_, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, wash.wax, wash.interior})
	assertRejected(t, err, "at most")
}

func TestBuildQuoteRejectsForeignOptions(t *testing.T) {
	wash := newCarWash()

	// An id from some other provider's service must not price into this one.
	_, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, uuid.New()})
	assertRejected(t, err, "does not belong to this service")
}

func TestBuildQuoteRejectsDuplicatesAndInactiveOptions(t *testing.T) {
	wash := newCarWash()

	_, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, wash.wax, wash.wax})
	assertRejected(t, err, "more than once")

	wash.groups[1].Options[0].IsActive = false
	_, err = BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, wash.wax})
	assertRejected(t, err, "no longer available")
}

func TestBuildQuoteRejectsMisconfiguredDeltas(t *testing.T) {
	wash := newCarWash()
	wash.groups[1].Options[0].PriceDeltaCents = -1_000_000

	_, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, wash.wax})
	assertRejected(t, err, "not purchasable")

	wash = newCarWash()
	wash.groups[1].Options[0].DurationDeltaMinutes = -1000
	_, err = BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.sedan, wash.wax})
	assertRejected(t, err, "no time to perform")
}

func TestBuildQuoteWithNoGroupsPricesTheBareService(t *testing.T) {
	wash := newCarWash()

	quote, err := BuildQuote(wash.service, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, int32(24500), quote.PriceCents)
	assert.Empty(t, quote.Options)
}

func TestBuildQuoteIsOrderIndependent(t *testing.T) {
	wash := newCarWash()

	first, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.suv, wash.wax})
	require.NoError(t, err)
	second, err := BuildQuote(wash.service, wash.groups, []uuid.UUID{wash.wax, wash.suv})
	require.NoError(t, err)

	assert.Equal(t, first, second)
}
