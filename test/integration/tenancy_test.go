package integration

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Milestone 3: providers, members, invitations, the x-organization-id
// middleware, and the four router groups mounted with their own chains.

// The surface split is the decision everything else depends on, so this test
// walks all four groups with the same token and checks each one enforces the
// chain it is supposed to.
func TestIntegrationFourSurfacesEnforceTheirOwnChains(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	customer := withToken(f.customer.AccessToken)

	t.Run("public needs nothing", func(t *testing.T) {
		anonymous := s.get(t, "/api/v1/public/categories")
		requireStatus(t, anonymous, http.StatusOK)

		// An optional bearer token is accepted, not required.
		authenticated := s.get(t, "/api/v1/public/categories", customer)
		requireStatus(t, authenticated, http.StatusOK)

		// And a bad one is ignored rather than rejected: someone browsing with
		// an expired token should still see the catalog.
		stale := s.get(t, "/api/v1/public/categories", withToken("garbage"))
		requireStatus(t, stale, http.StatusOK)
	})

	t.Run("me needs auth and nothing else", func(t *testing.T) {
		requireError(t, s.get(t, "/api/v1/me/profile"), http.StatusUnauthorized, "UNAUTHENTICATED")
		requireStatus(t, s.get(t, "/api/v1/me/profile", customer), http.StatusOK)

		// No org header is involved even when one is supplied.
		withStrayHeader := s.get(t, "/api/v1/me/profile", customer, withOrg(f.providerID))
		requireStatus(t, withStrayHeader, http.StatusOK)
	})

	t.Run("org needs auth plus a verified membership", func(t *testing.T) {
		requireError(t, s.get(t, "/api/v1/org/profile"), http.StatusUnauthorized, "UNAUTHENTICATED")

		// Authenticated but no header.
		requireError(t, s.get(t, "/api/v1/org/profile", customer),
			http.StatusBadRequest, "ORG_REQUIRED")

		// Header present, but the caller is not a member of that org.
		requireError(t, s.get(t, "/api/v1/org/profile", customer, withOrg(f.providerID)),
			http.StatusForbidden, "NOT_A_MEMBER")

		// The owner is.
		requireStatus(t, s.get(t, "/api/v1/org/profile", f.orgAuth()...), http.StatusOK)
	})

	t.Run("admin needs the platform role", func(t *testing.T) {
		requireError(t, s.get(t, "/api/v1/admin/providers"), http.StatusUnauthorized, "UNAUTHENTICATED")
		requireError(t, s.get(t, "/api/v1/admin/providers", customer), http.StatusForbidden, "FORBIDDEN")

		// A provider owner is not a platform admin.
		requireError(t, s.get(t, "/api/v1/admin/providers", withToken(f.owner.AccessToken)),
			http.StatusForbidden, "FORBIDDEN")

		requireStatus(t, s.get(t, "/api/v1/admin/providers", withToken(f.admin.AccessToken)),
			http.StatusOK)
	})
}

// The header is client-supplied, so it must be checked against the database on
// every request rather than trusted or read from the token.
func TestIntegrationOrgHeaderIsNeverTrusted(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	outsider := s.signUp(t, "Outsider", "outsider@example.et")

	t.Run("random org id", func(t *testing.T) {
		resp := s.get(t, "/api/v1/org/profile",
			withToken(outsider.AccessToken), withOrg(uuid.NewString()))
		requireError(t, resp, http.StatusForbidden, "NOT_A_MEMBER")
	})

	t.Run("a real org the caller does not belong to", func(t *testing.T) {
		resp := s.get(t, "/api/v1/org/profile",
			withToken(outsider.AccessToken), withOrg(f.providerID))
		requireError(t, resp, http.StatusForbidden, "NOT_A_MEMBER")
	})

	t.Run("malformed header", func(t *testing.T) {
		resp := s.get(t, "/api/v1/org/profile",
			withToken(outsider.AccessToken), withOrg("not-a-uuid"))
		requireError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	})

	// The decisive case: a membership revoked after the token was minted. The
	// JWT still claims it, so only a database check can catch this.
	t.Run("revoked membership with a still-valid token", func(t *testing.T) {
		staff := s.signUp(t, "Staff", "staff@shinewash.et")
		_, err := testPool.Exec(t.Context(),
			`INSERT INTO members (provider_id, user_id, role) VALUES ($1, $2, 'staff')`,
			f.providerID, staff.ID)
		require.NoError(t, err)

		staff = s.signIn(t, staff.Email)
		staffAuth := []option{withToken(staff.AccessToken), withOrg(f.providerID)}
		requireStatus(t, s.get(t, "/api/v1/org/profile", staffAuth...), http.StatusOK)

		_, err = testPool.Exec(t.Context(),
			`DELETE FROM members WHERE provider_id = $1 AND user_id = $2`, f.providerID, staff.ID)
		require.NoError(t, err)

		// Same token, which still carries the membership claim.
		resp := s.get(t, "/api/v1/org/profile", staffAuth...)
		requireError(t, resp, http.StatusForbidden, "NOT_A_MEMBER")
	})
}

func TestIntegrationProviderRegistrationNeedsApproval(t *testing.T) {
	s := newServer(t)
	admin := s.promoteToAdmin(t, s.signUp(t, "Admin", "admin@nearby.et"))
	owner := s.signUp(t, "Owner", "owner@example.et")

	resp := s.post(t, "/api/v1/me/providers", map[string]any{
		"name": "Corner Barber",
	}, withToken(owner.AccessToken))
	requireStatus(t, resp, http.StatusCreated)

	providerID := resp.Body["id"].(string)
	assert.Equal(t, "pending", resp.Body["status"])
	assert.Equal(t, "corner-barber", resp.Body["slug"], "a slug is derived from the name")
	assert.Equal(t, "Africa/Addis_Ababa", resp.Body["timezone"], "the configured default applies")

	// A pending provider is invisible to the public surface.
	requireError(t, s.get(t, "/api/v1/public/providers/corner-barber"),
		http.StatusNotFound, "NOT_FOUND")

	// The registrant is its owner and can already administer it.
	ownerAuth := []option{withToken(s.signIn(t, owner.Email).AccessToken), withOrg(providerID)}
	profile := s.get(t, "/api/v1/org/profile", ownerAuth...)
	requireStatus(t, profile, http.StatusOK)
	assert.Equal(t, "owner", profile.Body["role"])

	approve := s.post(t, "/api/v1/admin/providers/"+providerID+"/approve", nil,
		withToken(admin.AccessToken))
	requireStatus(t, approve, http.StatusOK)
	assert.Equal(t, "active", approve.Body["status"])

	requireStatus(t, s.get(t, "/api/v1/public/providers/corner-barber"), http.StatusOK)

	// Suspending hides it again.
	suspend := s.post(t, "/api/v1/admin/providers/"+providerID+"/suspend", nil,
		withToken(admin.AccessToken))
	requireStatus(t, suspend, http.StatusOK)
	requireError(t, s.get(t, "/api/v1/public/providers/corner-barber"),
		http.StatusNotFound, "NOT_FOUND")
}

func TestIntegrationSlugCollisionsResolve(t *testing.T) {
	s := newServer(t)
	first := s.signUp(t, "First", "first@example.et")
	second := s.signUp(t, "Second", "second@example.et")

	one := s.post(t, "/api/v1/me/providers", map[string]any{"name": "Shine Wash"},
		withToken(first.AccessToken))
	requireStatus(t, one, http.StatusCreated)
	assert.Equal(t, "shine-wash", one.Body["slug"])

	two := s.post(t, "/api/v1/me/providers", map[string]any{"name": "Shine Wash"},
		withToken(second.AccessToken))
	requireStatus(t, two, http.StatusCreated)
	assert.Equal(t, "shine-wash-2", two.Body["slug"], "a derived slug steps around a collision")

	// An explicitly requested duplicate is a conflict rather than a surprise.
	three := s.post(t, "/api/v1/me/providers",
		map[string]any{"name": "Another", "slug": "shine-wash"},
		withToken(second.AccessToken))
	requireError(t, three, http.StatusConflict, "CONFLICT")
}

func TestIntegrationInvitationFlow(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	invite := s.post(t, "/api/v1/org/invitations", map[string]any{
		"email": "newstaff@example.et",
		"role":  "staff",
	}, orgAuth...)
	requireStatus(t, invite, http.StatusCreated)

	token := invite.Body["token"].(string)
	require.NotEmpty(t, token, "the plaintext token is returned exactly once")

	// Only the hash is stored, so the token cannot be recovered afterwards.
	list := s.get(t, "/api/v1/org/invitations", orgAuth...)
	requireStatus(t, list, http.StatusOK)
	assert.NotContains(t, list.Raw, token, "the token must never be readable again")

	t.Run("wrong recipient cannot redeem it", func(t *testing.T) {
		wrongPerson := s.signUp(t, "Wrong", "wrong@example.et")
		resp := s.post(t, "/api/v1/me/invitations/accept", map[string]any{"token": token},
			withToken(wrongPerson.AccessToken))
		requireError(t, resp, http.StatusForbidden, "FORBIDDEN")
	})

	t.Run("a forged token is rejected", func(t *testing.T) {
		person := s.signUp(t, "Forger", "newstaff@example.et")
		resp := s.post(t, "/api/v1/me/invitations/accept",
			map[string]any{"token": "made-up-token"}, withToken(person.AccessToken))
		requireError(t, resp, http.StatusUnauthorized, "INVALID_TOKEN")
	})

	t.Run("the invited address redeems it once", func(t *testing.T) {
		invited := s.signIn(t, "newstaff@example.et")

		accept := s.post(t, "/api/v1/me/invitations/accept", map[string]any{"token": token},
			withToken(invited.AccessToken))
		requireStatus(t, accept, http.StatusOK)
		assert.Equal(t, "staff", accept.Body["role"])

		// The membership is real: the org surface now opens for them.
		invited = s.signIn(t, invited.Email)
		requireStatus(t, s.get(t, "/api/v1/org/profile",
			withToken(invited.AccessToken), withOrg(f.providerID)), http.StatusOK)

		// ...but a second redemption does not.
		replay := s.post(t, "/api/v1/me/invitations/accept", map[string]any{"token": token},
			withToken(invited.AccessToken))
		requireError(t, replay, http.StatusConflict, "CONFLICT")
	})
}

// The other way onto a team: the owner creates the account outright and hands
// the credentials over, instead of mailing an invitation.
func TestIntegrationOwnerCreatesStaffAccounts(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	t.Run("with a password the owner chose", func(t *testing.T) {
		resp := s.post(t, "/api/v1/org/members", map[string]any{
			"name":     "Selam Tesfaye",
			"email":    "selam@shinewash.et",
			"password": "chosen-by-owner",
			"role":     "admin",
		}, orgAuth...)
		requireStatus(t, resp, http.StatusCreated)

		member := resp.Body["member"].(map[string]any)
		assert.Equal(t, "admin", member["role"])
		assert.Equal(t, "selam@shinewash.et", member["email"])
		assert.NotContains(t, resp.Body, "generatedPassword",
			"a password the caller supplied is not echoed back")

		// The account is real: those credentials open the org surface.
		created := s.signInWith(t, "selam@shinewash.et", "chosen-by-owner")
		requireStatus(t, s.get(t, "/api/v1/org/profile",
			withToken(created.AccessToken), withOrg(f.providerID)), http.StatusOK)
	})

	t.Run("with a password the server generated", func(t *testing.T) {
		resp := s.post(t, "/api/v1/org/members", map[string]any{
			"name":  "Yonas Alemu",
			"email": "yonas@shinewash.et",
			"role":  "staff",
		}, orgAuth...)
		requireStatus(t, resp, http.StatusCreated)

		password, ok := resp.Body["generatedPassword"].(string)
		require.True(t, ok, "the generated password is returned so it can be handed over")
		require.NotEmpty(t, password)

		created := s.signInWith(t, "yonas@shinewash.et", password)
		requireStatus(t, s.get(t, "/api/v1/org/profile",
			withToken(created.AccessToken), withOrg(f.providerID)), http.StatusOK)

		// It was hashed on the way in, so it is not readable a second time.
		list := s.get(t, "/api/v1/org/members", orgAuth...)
		requireStatus(t, list, http.StatusOK)
		assert.NotContains(t, list.Raw, password)
	})

	t.Run("an address that already has an account is refused", func(t *testing.T) {
		resp := s.post(t, "/api/v1/org/members", map[string]any{
			"name":  "Someone Else",
			"email": f.owner.Email,
			"role":  "staff",
		}, orgAuth...)
		requireError(t, resp, http.StatusConflict, "CONFLICT")
	})

	t.Run("a new account cannot start as an owner", func(t *testing.T) {
		resp := s.post(t, "/api/v1/org/members", map[string]any{
			"name":  "Would-be Owner",
			"email": "wouldbe@shinewash.et",
			"role":  "owner",
		}, orgAuth...)
		requireError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	})

	// A failed creation must not leave a usable login behind: the user row and
	// the membership go in together or not at all.
	t.Run("a rejected role leaves no orphaned account", func(t *testing.T) {
		var exists bool
		require.NoError(t, testPool.QueryRow(t.Context(),
			`SELECT exists(SELECT 1 FROM users WHERE email = 'wouldbe@shinewash.et')`).Scan(&exists))
		assert.False(t, exists)
	})
}

func TestIntegrationRoleGatingWithinAnOrg(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	staff := s.signUp(t, "Staff", "staff@shinewash.et")
	_, err := testPool.Exec(t.Context(),
		`INSERT INTO members (provider_id, user_id, role) VALUES ($1, $2, 'staff')`,
		f.providerID, staff.ID)
	require.NoError(t, err)
	staff = s.signIn(t, staff.Email)
	staffAuth := []option{withToken(staff.AccessToken), withOrg(f.providerID)}

	// Staff work the inbox...
	requireStatus(t, s.get(t, "/api/v1/org/bookings", staffAuth...), http.StatusOK)
	requireStatus(t, s.get(t, "/api/v1/org/services", staffAuth...), http.StatusOK)

	// ...but do not change the catalog, the schedule or the team.
	requireError(t, s.post(t, "/api/v1/org/services", map[string]any{
		"categoryId": f.categoryID, "name": "Nope", "priceCents": 1, "durationMinutes": 10,
	}, staffAuth...), http.StatusForbidden, "FORBIDDEN")

	requireError(t, s.patch(t, "/api/v1/org/profile",
		map[string]any{"name": "Renamed"}, staffAuth...), http.StatusForbidden, "FORBIDDEN")

	requireError(t, s.post(t, "/api/v1/org/invitations",
		map[string]any{"email": "x@example.et", "role": "staff"}, staffAuth...),
		http.StatusForbidden, "FORBIDDEN")

	// Removing a member, or creating one outright, is owner-only.
	requireError(t, s.delete(t, "/api/v1/org/members/"+f.owner.ID, staffAuth...),
		http.StatusForbidden, "FORBIDDEN")

	requireError(t, s.post(t, "/api/v1/org/members", map[string]any{
		"name": "Hire", "email": "hire@example.et", "role": "staff",
	}, staffAuth...), http.StatusForbidden, "FORBIDDEN")
}

// A provider must never be left with nobody who can administer it.
func TestIntegrationLastOwnerIsProtected(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	demote := s.patch(t, "/api/v1/org/members/"+f.owner.ID,
		map[string]any{"role": "staff"}, orgAuth...)
	requireError(t, demote, http.StatusConflict, "CONFLICT")

	remove := s.delete(t, "/api/v1/org/members/"+f.owner.ID, orgAuth...)
	requireError(t, remove, http.StatusConflict, "CONFLICT")
}

func TestIntegrationProviderProfileUpdate(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	resp := s.patch(t, "/api/v1/org/profile", map[string]any{
		"description": "Two-bay hand wash on Bole Road.",
		"location":    map[string]any{"lat": 9.0192, "lng": 38.7578},
		"bookingMode": "request",
	}, orgAuth...)
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, "request", resp.Body["bookingMode"])
	location := resp.Body["location"].(map[string]any)
	assert.InDelta(t, 9.0192, location["lat"], 0.0001)
	assert.InDelta(t, 38.7578, location["lng"], 0.0001)

	t.Run("rejects nonsense", func(t *testing.T) {
		requireError(t, s.patch(t, "/api/v1/org/profile",
			map[string]any{"timezone": "Mars/Olympus_Mons"}, orgAuth...),
			http.StatusUnprocessableEntity, "VALIDATION_ERROR")

		requireError(t, s.patch(t, "/api/v1/org/profile",
			map[string]any{"location": map[string]any{"lat": 991, "lng": 0}}, orgAuth...),
			http.StatusUnprocessableEntity, "VALIDATION_ERROR")

		requireError(t, s.patch(t, "/api/v1/org/profile",
			map[string]any{"bookingMode": "whenever"}, orgAuth...),
			http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	})
}
