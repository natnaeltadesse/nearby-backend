package catalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// carWashDefs mirrors what a platform admin would insert for a car wash.
func carWashDefs() []Attribute {
	return []Attribute{
		{
			Key: "vehicle_size", Label: "Vehicle size", DataType: TypeEnum,
			Options: []string{"sedan", "suv", "pickup"}, Required: true,
			AppliesTo: AppliesToService, Filterable: true,
		},
		{
			Key: "extras", Label: "Extras", DataType: TypeMultiEnum,
			Options:   []string{"wax", "interior", "engine"},
			AppliesTo: AppliesToService,
		},
		{Key: "notes", Label: "Notes", DataType: TypeText, AppliesTo: AppliesToService},
		{Key: "bay_height_cm", Label: "Bay height", DataType: TypeNumber, AppliesTo: AppliesToService},
		{Key: "indoor", Label: "Indoor", DataType: TypeBool, AppliesTo: AppliesToService},
	}
}

// asClientJSON round-trips through JSON so the test sees exactly the Go types
// a decoded request body produces (float64 for every number, []any for lists).
func asClientJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var values map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &values))
	return values
}

func assertValidationError(t *testing.T, err error, wantKeys ...string) *httpx.Error {
	t.Helper()
	require.Error(t, err)

	apiErr := httpx.AsError(err)
	assert.Equal(t, httpx.CodeValidationError, apiErr.Code)
	assert.Equal(t, 422, apiErr.Status)
	for _, key := range wantKeys {
		assert.Contains(t, apiErr.Details, key)
	}
	return apiErr
}

func TestValidateAttributesAcceptsAWellFormedPayload(t *testing.T) {
	values := asClientJSON(t, `{
		"vehicle_size": "suv",
		"extras": ["interior", "wax"],
		"notes": "  ring the bell  ",
		"bay_height_cm": 210,
		"indoor": true
	}`)

	normalized, err := ValidateAttributes(carWashDefs(), values)
	require.NoError(t, err)

	assert.Equal(t, "suv", normalized["vehicle_size"])
	assert.Equal(t, "ring the bell", normalized["notes"], "text is trimmed")
	assert.Equal(t, float64(210), normalized["bay_height_cm"])
	assert.Equal(t, true, normalized["indoor"])
	// Sorted so jsonb containment in discovery does not depend on the order the
	// client happened to send.
	assert.Equal(t, []string{"interior", "wax"}, normalized["extras"])
}

func TestValidateAttributesRejectsUnknownKeys(t *testing.T) {
	values := asClientJSON(t, `{"vehicle_size": "sedan", "colour": "red"}`)

	_, err := ValidateAttributes(carWashDefs(), values)
	assertValidationError(t, err, "colour")
}

func TestValidateAttributesRequiresRequiredKeys(t *testing.T) {
	_, err := ValidateAttributes(carWashDefs(), map[string]any{})
	apiErr := assertValidationError(t, err, "vehicle_size")
	assert.Contains(t, apiErr.Details["vehicle_size"], "required")

	// Explicit null counts as absent rather than as a value.
	_, err = ValidateAttributes(carWashDefs(), asClientJSON(t, `{"vehicle_size": null}`))
	assertValidationError(t, err, "vehicle_size")
}

func TestValidateAttributesEnforcesEnumMembership(t *testing.T) {
	_, err := ValidateAttributes(carWashDefs(), asClientJSON(t, `{"vehicle_size": "helicopter"}`))
	apiErr := assertValidationError(t, err, "vehicle_size")
	assert.Contains(t, apiErr.Details["vehicle_size"], "sedan")
}

func TestValidateAttributesEnforcesMultiEnumMembership(t *testing.T) {
	_, err := ValidateAttributes(carWashDefs(), asClientJSON(t,
		`{"vehicle_size": "suv", "extras": ["wax", "hovercraft"]}`))
	assertValidationError(t, err, "extras")

	_, err = ValidateAttributes(carWashDefs(), asClientJSON(t,
		`{"vehicle_size": "suv", "extras": ["wax", "wax"]}`))
	assertValidationError(t, err, "extras")

	_, err = ValidateAttributes(carWashDefs(), asClientJSON(t,
		`{"vehicle_size": "suv", "extras": "wax"}`))
	assertValidationError(t, err, "extras")
}

func TestValidateAttributesEnforcesScalarTypes(t *testing.T) {
	cases := map[string]string{
		"notes":         `{"vehicle_size": "suv", "notes": 42}`,
		"bay_height_cm": `{"vehicle_size": "suv", "bay_height_cm": "tall"}`,
		"indoor":        `{"vehicle_size": "suv", "indoor": "yes"}`,
	}

	for key, payload := range cases {
		t.Run(key, func(t *testing.T) {
			_, err := ValidateAttributes(carWashDefs(), asClientJSON(t, payload))
			assertValidationError(t, err, key)
		})
	}
}

func TestValidateAttributesReportsEveryProblemAtOnce(t *testing.T) {
	// A dynamic form should be able to mark all its bad fields in one pass
	// rather than making the user fix them one request at a time.
	values := asClientJSON(t, `{"vehicle_size": "helicopter", "indoor": "yes", "colour": "red"}`)

	_, err := ValidateAttributes(carWashDefs(), values)
	apiErr := assertValidationError(t, err, "vehicle_size", "indoor", "colour")
	assert.Len(t, apiErr.Details, 3)
	assert.Contains(t, apiErr.Message, "3 attributes are invalid")
}

func TestValidateAttributesAllowsOptionalAbsentFields(t *testing.T) {
	normalized, err := ValidateAttributes(carWashDefs(), asClientJSON(t, `{"vehicle_size": "sedan"}`))
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"vehicle_size": "sedan"}, normalized)
}

func TestValidateAttributesWithNoDefinitionsRejectsAnything(t *testing.T) {
	// A category with no attributes is a category where every key is unknown.
	_, err := ValidateAttributes(nil, asClientJSON(t, `{"anything": 1}`))
	assertValidationError(t, err, "anything")

	normalized, err := ValidateAttributes(nil, map[string]any{})
	require.NoError(t, err)
	assert.Empty(t, normalized)
}
