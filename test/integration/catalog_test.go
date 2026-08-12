package integration

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Milestones 4 and 5: categories, attribute definitions, the validator,
// services, option groups and server-side price/duration computation.

// The point of the attribute model: a new vertical with its own fields is a
// data insert by a platform admin — no migration, no app release.
func TestIntegrationNewVerticalNeedsNoDeployment(t *testing.T) {
	s := newServer(t)
	admin := s.promoteToAdmin(t, s.signUp(t, "Admin", "admin@nearby.et"))
	adminAuth := withToken(admin.AccessToken)

	category := s.post(t, "/api/v1/admin/categories", map[string]any{
		"name": "Pet grooming",
	}, adminAuth)
	requireStatus(t, category, http.StatusCreated)
	categoryID := category.Body["id"].(string)
	assert.Equal(t, "pet-grooming", category.Body["slug"])

	for _, attribute := range []map[string]any{
		{
			"key": "pet_type", "label": "Pet", "dataType": "enum",
			"options": []string{"dog", "cat"}, "required": true,
			"appliesTo": "booking", "sortOrder": 1,
		},
		{
			"key": "coat_length", "label": "Coat length", "dataType": "enum",
			"options": []string{"short", "long"}, "appliesTo": "service",
			"filterable": true, "sortOrder": 2,
		},
	} {
		resp := s.post(t, "/api/v1/admin/categories/"+categoryID+"/attributes", attribute, adminAuth)
		requireStatus(t, resp, http.StatusCreated)
	}

	// Both clients read this to build their forms; it is available immediately
	// and without authentication.
	public := s.get(t, "/api/v1/public/categories/"+categoryID+"/attributes")
	requireStatus(t, public, http.StatusOK)
	attributes := public.Body["attributes"].([]any)
	require.Len(t, attributes, 2)

	first := attributes[0].(map[string]any)
	assert.Equal(t, "pet_type", first["key"])
	assert.Equal(t, []any{"dog", "cat"}, first["options"])
	assert.Equal(t, true, first["required"])

	// The scope filter is what lets a client ask only for the fields it needs.
	scoped := s.get(t, "/api/v1/public/categories/"+categoryID+"/attributes?appliesTo=service")
	requireStatus(t, scoped, http.StatusOK)
	require.Len(t, scoped.Body["attributes"].([]any), 1)
}

func TestIntegrationAttributeDefinitionsAreValidated(t *testing.T) {
	s := newServer(t)
	admin := s.promoteToAdmin(t, s.signUp(t, "Admin", "admin@nearby.et"))
	adminAuth := withToken(admin.AccessToken)

	category := s.post(t, "/api/v1/admin/categories", map[string]any{"name": "Laundry"}, adminAuth)
	requireStatus(t, category, http.StatusCreated)
	categoryID := category.Body["id"].(string)
	path := "/api/v1/admin/categories/" + categoryID + "/attributes"

	cases := []struct {
		name string
		body map[string]any
	}{
		{"enum with no options", map[string]any{
			"key": "tier", "label": "Tier", "dataType": "enum", "appliesTo": "service",
		}},
		{"unknown data type", map[string]any{
			"key": "x", "label": "X", "dataType": "colour", "appliesTo": "service",
		}},
		{"unknown scope", map[string]any{
			"key": "x", "label": "X", "dataType": "text", "appliesTo": "invoice",
		}},
		{"duplicate options", map[string]any{
			"key": "x", "label": "X", "dataType": "enum",
			"options": []string{"a", "a"}, "appliesTo": "service",
		}},
		{"missing key", map[string]any{
			"label": "X", "dataType": "text", "appliesTo": "service",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireError(t, s.post(t, path, tc.body, adminAuth),
				http.StatusUnprocessableEntity, "VALIDATION_ERROR")
		})
	}

	// The same (key, appliesTo) cannot be defined twice on one category.
	valid := map[string]any{
		"key": "tier", "label": "Tier", "dataType": "enum",
		"options": []string{"light", "heavy"}, "appliesTo": "service",
	}
	requireStatus(t, s.post(t, path, valid, adminAuth), http.StatusCreated)
	requireError(t, s.post(t, path, valid, adminAuth), http.StatusConflict, "CONFLICT")
}

// Service attributes are validated against the category's definitions on write.
func TestIntegrationServiceAttributesAreValidated(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	base := map[string]any{
		"categoryId": f.categoryID, "name": "Quick rinse",
		"priceCents": 12000, "durationMinutes": 20,
	}

	t.Run("value outside the enum", func(t *testing.T) {
		body := map[string]any{"attributes": map[string]any{"wash_type": "hovercraft"}}
		for k, v := range base {
			body[k] = v
		}
		resp := s.post(t, "/api/v1/org/services", body, orgAuth...)
		requireError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_ERROR")

		details := resp.Body["details"].(map[string]any)
		assert.Contains(t, details, "wash_type")
	})

	t.Run("unknown key", func(t *testing.T) {
		body := map[string]any{"attributes": map[string]any{"colour": "red"}}
		for k, v := range base {
			body[k] = v
		}
		requireError(t, s.post(t, "/api/v1/org/services", body, orgAuth...),
			http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	})

	t.Run("wrong scope is rejected as unknown", func(t *testing.T) {
		// vehicle_size is a booking attribute, so it is not a service field.
		body := map[string]any{"attributes": map[string]any{"vehicle_size": "sedan"}}
		for k, v := range base {
			body[k] = v
		}
		requireError(t, s.post(t, "/api/v1/org/services", body, orgAuth...),
			http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	})

	t.Run("valid attributes are stored", func(t *testing.T) {
		body := map[string]any{"attributes": map[string]any{"wash_type": "full"}}
		for k, v := range base {
			body[k] = v
		}
		resp := s.post(t, "/api/v1/org/services", body, orgAuth...)
		requireStatus(t, resp, http.StatusCreated)
		assert.Equal(t, map[string]any{"wash_type": "full"}, resp.Body["attributes"])
	})
}

func TestIntegrationServiceValidationRules(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"negative price", map[string]any{
			"categoryId": f.categoryID, "name": "X", "priceCents": -1, "durationMinutes": 30,
		}},
		{"zero duration", map[string]any{
			"categoryId": f.categoryID, "name": "X", "priceCents": 100, "durationMinutes": 0,
		}},
		{"blank name", map[string]any{
			"categoryId": f.categoryID, "name": "  ", "priceCents": 100, "durationMinutes": 30,
		}},
		{"unknown category", map[string]any{
			"categoryId": "00000000-0000-0000-0000-0000000000ff",
			"name":       "X", "priceCents": 100, "durationMinutes": 30,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireError(t, s.post(t, "/api/v1/org/services", tc.body, orgAuth...),
				http.StatusUnprocessableEntity, "VALIDATION_ERROR")
		})
	}
}

// One org must not be able to read or write another's catalog, even holding a
// valid token and a valid org header of its own.
func TestIntegrationCatalogIsTenantIsolated(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	rival := s.signUp(t, "Rival", "rival@example.et")
	created := s.post(t, "/api/v1/me/providers", map[string]any{"name": "Rival Wash"},
		withToken(rival.AccessToken))
	requireStatus(t, created, http.StatusCreated)
	rivalProviderID := created.Body["id"].(string)
	setProviderStatus(t, rivalProviderID, "active")

	rival = s.signIn(t, rival.Email)
	rivalAuth := []option{withToken(rival.AccessToken), withOrg(rivalProviderID)}

	// The rival's own org surface works.
	requireStatus(t, s.get(t, "/api/v1/org/services", rivalAuth...), http.StatusOK)

	// But the fixture's service is invisible and unwritable to them.
	requireError(t, s.get(t, "/api/v1/org/services/"+f.serviceID, rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")

	requireError(t, s.patch(t, "/api/v1/org/services/"+f.serviceID,
		map[string]any{"priceCents": 1}, rivalAuth...), http.StatusNotFound, "NOT_FOUND")

	requireError(t, s.delete(t, "/api/v1/org/services/"+f.serviceID, rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")

	requireError(t, s.post(t, "/api/v1/org/services/"+f.serviceID+"/option-groups",
		map[string]any{"name": "Sneaky", "selectionType": "single"}, rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")

	// The fixture's service is untouched.
	original := s.get(t, "/api/v1/org/services/"+f.serviceID, f.orgAuth()...)
	requireStatus(t, original, http.StatusOK)
	assert.Equal(t, float64(24500), original.Body["priceCents"])
}

// Only active services of active providers appear on the public surface.
func TestIntegrationPublicSurfaceHidesInactiveCatalog(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	requireStatus(t, s.get(t, "/api/v1/public/services/"+f.serviceID), http.StatusOK)

	listed := s.get(t, "/api/v1/public/providers/"+f.providerID+"/services")
	requireStatus(t, listed, http.StatusOK)
	require.Len(t, listed.Body["services"].([]any), 1)

	deactivate := s.patch(t, "/api/v1/org/services/"+f.serviceID,
		map[string]any{"isActive": false}, orgAuth...)
	requireStatus(t, deactivate, http.StatusOK)

	requireError(t, s.get(t, "/api/v1/public/services/"+f.serviceID),
		http.StatusNotFound, "NOT_FOUND")

	hidden := s.get(t, "/api/v1/public/providers/"+f.providerID+"/services")
	requireStatus(t, hidden, http.StatusOK)
	assert.Empty(t, hidden.Body["services"])

	// Staff still see it, which is how they turn it back on.
	staffView := s.get(t, "/api/v1/org/services", orgAuth...)
	requireStatus(t, staffView, http.StatusOK)
	require.Len(t, staffView.Body["services"].([]any), 1)
}

// The service detail endpoint carries the option tree, which is what the
// client renders the add-on form from.
func TestIntegrationServiceDetailCarriesItsOptionTree(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	resp := s.get(t, "/api/v1/public/services/"+f.serviceID)
	requireStatus(t, resp, http.StatusOK)

	groups := resp.Body["optionGroups"].([]any)
	require.Len(t, groups, 2)

	size := groups[0].(map[string]any)
	assert.Equal(t, "Vehicle size", size["name"])
	assert.Equal(t, "single", size["selectionType"])
	assert.Equal(t, true, size["isRequired"])
	require.Len(t, size["options"].([]any), 2)

	extras := groups[1].(map[string]any)
	assert.Equal(t, "multi", extras["selectionType"])

	wax := extras["options"].([]any)[0].(map[string]any)
	assert.Equal(t, "Wax", wax["name"])
	assert.Equal(t, float64(3000), wax["priceDeltaCents"])
	assert.Equal(t, float64(20), wax["durationDeltaMinutes"])
}

func TestIntegrationOptionGroupValidation(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()
	path := "/api/v1/org/services/" + f.serviceID + "/option-groups"

	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown selection type", map[string]any{"name": "X", "selectionType": "maybe"}},
		{"blank name", map[string]any{"name": "", "selectionType": "single"}},
		{"min above max", map[string]any{
			"name": "X", "selectionType": "multi", "minSelect": 3, "maxSelect": 2,
		}},
		{"single-select with max above one", map[string]any{
			"name": "X", "selectionType": "single", "maxSelect": 2,
		}},
		{"zero max", map[string]any{
			"name": "X", "selectionType": "multi", "maxSelect": 0,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireError(t, s.post(t, path, tc.body, orgAuth...),
				http.StatusUnprocessableEntity, "VALIDATION_ERROR")
		})
	}
}

// Deleting a category that services still reference must fail rather than
// cascade, so a vertical cannot be pulled out from under a live tenant.
func TestIntegrationCategoryInUseCannotBeDeleted(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	adminAuth := withToken(f.admin.AccessToken)

	requireError(t, s.delete(t, "/api/v1/admin/categories/"+f.categoryID, adminAuth),
		http.StatusConflict, "CONFLICT")

	// Deactivating is the supported route, and it hides the category from the
	// public list without disturbing existing services.
	deactivate := s.patch(t, "/api/v1/admin/categories/"+f.categoryID,
		map[string]any{"isActive": false}, adminAuth)
	requireStatus(t, deactivate, http.StatusOK)

	public := s.get(t, "/api/v1/public/categories")
	requireStatus(t, public, http.StatusOK)
	assert.Empty(t, public.Body["categories"])

	// Admins still see it.
	all := s.get(t, "/api/v1/admin/categories", adminAuth)
	requireStatus(t, all, http.StatusOK)
	require.Len(t, all.Body["categories"].([]any), 1)
}

func TestIntegrationServiceUpdateRevalidatesAttributes(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()
	path := fmt.Sprintf("/api/v1/org/services/%s", f.serviceID)

	// A partial update that does not mention attributes leaves them alone.
	priceOnly := s.patch(t, path, map[string]any{"priceCents": 30000}, orgAuth...)
	requireStatus(t, priceOnly, http.StatusOK)
	assert.Equal(t, float64(30000), priceOnly.Body["priceCents"])
	assert.Equal(t, map[string]any{"wash_type": "exterior"}, priceOnly.Body["attributes"])

	// One that does is validated again.
	requireError(t, s.patch(t, path,
		map[string]any{"attributes": map[string]any{"wash_type": "spaceship"}}, orgAuth...),
		http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	good := s.patch(t, path,
		map[string]any{"attributes": map[string]any{"wash_type": "interior"}}, orgAuth...)
	requireStatus(t, good, http.StatusOK)
	assert.Equal(t, map[string]any{"wash_type": "interior"}, good.Body["attributes"])
}
