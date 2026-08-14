package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Milestone 2: users, argon2id, JWT, refresh rotation, client_type, middleware.

func TestIntegrationSignUpAndSignIn(t *testing.T) {
	s := newServer(t)

	resp := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name":     "Abebe Kebede",
		"email":    "  Abebe@Example.ET ",
		"password": "password123",
	})
	requireStatus(t, resp, http.StatusCreated)

	user := resp.Body["user"].(map[string]any)
	assert.Equal(t, "abebe@example.et", user["email"], "email is normalized on the way in")
	assert.Equal(t, "user", user["platformRole"])
	assert.NotContains(t, resp.Raw, "password", "no password material may appear in a response")

	// A customer's memberships list is empty — that is the whole difference
	// between a customer account and a provider account.
	assert.Empty(t, resp.Body["memberships"])

	// The normalized address is what signs in.
	signIn := s.post(t, "/api/v1/auth/sign-in", map[string]any{
		"email":    "ABEBE@EXAMPLE.ET",
		"password": "password123",
	})
	requireStatus(t, signIn, http.StatusOK)
}

func TestIntegrationSignUpRejectsBadInput(t *testing.T) {
	s := newServer(t)
	s.signUp(t, "Taken", "taken@example.et")

	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"duplicate email", map[string]any{
			"name": "Other", "email": "taken@example.et", "password": "password123",
		}, "EMAIL_TAKEN"},
		{"short password", map[string]any{
			"name": "Short", "email": "short@example.et", "password": "abc",
		}, "VALIDATION_ERROR"},
		{"malformed email", map[string]any{
			"name": "Bad", "email": "not-an-email", "password": "password123",
		}, "VALIDATION_ERROR"},
		{"missing name", map[string]any{
			"email": "noname@example.et", "password": "password123",
		}, "VALIDATION_ERROR"},
		{"unknown field", map[string]any{
			"name": "X", "email": "x@example.et", "password": "password123", "isAdmin": true,
		}, "VALIDATION_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, "/api/v1/auth/sign-up", tc.body)
			assert.Equal(t, tc.code, resp.Code(), "unexpected: %s", resp)
		})
	}
}

// A client must never be able to promote itself by sending platformRole.
func TestIntegrationSignUpCannotSelfPromote(t *testing.T) {
	s := newServer(t)

	resp := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name": "Sneaky", "email": "sneaky@example.et",
		"password": "password123", "platformRole": "admin",
	})
	requireError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

func TestIntegrationSignInRejectsWrongCredentials(t *testing.T) {
	s := newServer(t)
	s.signUp(t, "Abebe", "abebe@example.et")

	wrongPassword := s.post(t, "/api/v1/auth/sign-in", map[string]any{
		"email": "abebe@example.et", "password": "wrong-password",
	})
	requireError(t, wrongPassword, http.StatusUnauthorized, "INVALID_CREDENTIALS")

	noSuchUser := s.post(t, "/api/v1/auth/sign-in", map[string]any{
		"email": "nobody@example.et", "password": "password123",
	})
	requireError(t, noSuchUser, http.StatusUnauthorized, "INVALID_CREDENTIALS")

	// Both branches must say exactly the same thing, or the endpoint becomes
	// an account-existence oracle.
	assert.Equal(t, wrongPassword.Message(), noSuchUser.Message())
}

func TestIntegrationRefreshRotatesAndDetectsReuse(t *testing.T) {
	s := newServer(t)
	user := s.signUp(t, "Abebe", "abebe@example.et")

	first := s.post(t, "/api/v1/auth/refresh", map[string]any{
		"refreshToken": user.RefreshToken,
	})
	requireStatus(t, first, http.StatusOK)

	rotated := first.Body["tokens"].(map[string]any)["refreshToken"].(string)
	require.NotEqual(t, user.RefreshToken, rotated, "the refresh token must rotate on every use")

	// Replaying the consumed token is treated as theft: it fails, and it also
	// burns the whole token family because we cannot tell victim from thief.
	replay := s.post(t, "/api/v1/auth/refresh", map[string]any{
		"refreshToken": user.RefreshToken,
	})
	requireError(t, replay, http.StatusUnauthorized, "INVALID_TOKEN")

	afterReuse := s.post(t, "/api/v1/auth/refresh", map[string]any{
		"refreshToken": rotated,
	})
	requireError(t, afterReuse, http.StatusUnauthorized, "INVALID_TOKEN")
}

func TestIntegrationRefreshRejectsGarbage(t *testing.T) {
	s := newServer(t)

	resp := s.post(t, "/api/v1/auth/refresh", map[string]any{"refreshToken": "not-a-token"})
	requireError(t, resp, http.StatusUnauthorized, "INVALID_TOKEN")
}

func TestIntegrationSignOutRevokesTheToken(t *testing.T) {
	s := newServer(t)
	user := s.signUp(t, "Abebe", "abebe@example.et")

	resp := s.post(t, "/api/v1/auth/sign-out", map[string]any{"refreshToken": user.RefreshToken})
	requireStatus(t, resp, http.StatusNoContent)

	reuse := s.post(t, "/api/v1/auth/refresh", map[string]any{"refreshToken": user.RefreshToken})
	requireError(t, reuse, http.StatusUnauthorized, "INVALID_TOKEN")

	// Signing out twice is not an error: a client retrying must not see a failure.
	again := s.post(t, "/api/v1/auth/sign-out", map[string]any{"refreshToken": user.RefreshToken})
	requireStatus(t, again, http.StatusNoContent)
}

func TestIntegrationMobileAndWebGetDifferentRefreshLifetimes(t *testing.T) {
	s := newServer(t)

	web := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name": "Web", "email": "web@example.et", "password": "password123", "clientType": "web",
	})
	requireStatus(t, web, http.StatusCreated)

	mobile := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name": "Mobile", "email": "mobile@example.et", "password": "password123", "clientType": "mobile",
	})
	requireStatus(t, mobile, http.StatusCreated)

	var webType, mobileType string
	require.NoError(t, testPool.QueryRow(t.Context(),
		`SELECT client_type FROM refresh_tokens r
		 JOIN users u ON u.id = r.user_id WHERE u.email = 'web@example.et'`).Scan(&webType))
	require.NoError(t, testPool.QueryRow(t.Context(),
		`SELECT client_type FROM refresh_tokens r
		 JOIN users u ON u.id = r.user_id WHERE u.email = 'mobile@example.et'`).Scan(&mobileType))

	assert.Equal(t, "web", webType)
	assert.Equal(t, "mobile", mobileType)

	// 30 days for web, 90 for mobile: the mobile token must outlive the web one.
	var mobileLongerLived bool
	require.NoError(t, testPool.QueryRow(t.Context(), `
		SELECT (SELECT expires_at FROM refresh_tokens WHERE client_type = 'mobile')
		     > (SELECT expires_at FROM refresh_tokens WHERE client_type = 'web')`).
		Scan(&mobileLongerLived))
	assert.True(t, mobileLongerLived, "mobile refresh tokens must last longer than web ones")
}

// Rotation must preserve the client type, or a mobile session would silently
// shorten itself to the web TTL on its first refresh.
func TestIntegrationRefreshKeepsClientType(t *testing.T) {
	s := newServer(t)

	resp := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name": "Mobile", "email": "mobile@example.et",
		"password": "password123", "clientType": "mobile",
	})
	requireStatus(t, resp, http.StatusCreated)
	refreshToken := resp.Body["tokens"].(map[string]any)["refreshToken"].(string)

	refreshed := s.post(t, "/api/v1/auth/refresh", map[string]any{"refreshToken": refreshToken})
	requireStatus(t, refreshed, http.StatusOK)

	var clientType string
	require.NoError(t, testPool.QueryRow(t.Context(),
		`SELECT client_type FROM refresh_tokens WHERE revoked_at IS NULL`).Scan(&clientType))
	assert.Equal(t, "mobile", clientType)
}

func TestIntegrationSessionRequiresAValidToken(t *testing.T) {
	s := newServer(t)
	user := s.signUp(t, "Abebe", "abebe@example.et")

	ok := s.get(t, "/api/v1/auth/session", withToken(user.AccessToken))
	requireStatus(t, ok, http.StatusOK)
	assert.Equal(t, "abebe@example.et", ok.Body["user"].(map[string]any)["email"])

	missing := s.get(t, "/api/v1/auth/session")
	requireError(t, missing, http.StatusUnauthorized, "UNAUTHENTICATED")

	garbage := s.get(t, "/api/v1/auth/session", withToken("not.a.jwt"))
	requireError(t, garbage, http.StatusUnauthorized, "INVALID_TOKEN")
}

// A token signed with the wrong key must never be accepted, however
// well-formed it looks.
func TestIntegrationRejectsForeignlySignedToken(t *testing.T) {
	s := newServer(t)

	// HS256 over {"sub": <uuid>, "platformRole": "admin"} signed with "evil".
	forged := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiIwMDAwMDAwMC0wMDAwLTAwMDAtMDAwMC0wMDAwMDAwMDAwMDEiLCJwbGF0Zm9ybVJvbGUiOiJhZG1pbiJ9." +
		"3Hs8Zx9r0mJq0dQ1p8VYh7hRk7wS0nZ0jH0lQ7bJ8xY"

	resp := s.get(t, "/api/v1/auth/session", withToken(forged))
	requireError(t, resp, http.StatusUnauthorized, "INVALID_TOKEN")
}

func TestIntegrationProfileUpdate(t *testing.T) {
	s := newServer(t)
	user := s.signUp(t, "Abebe", "abebe@example.et")
	authed := withToken(user.AccessToken)

	resp := s.patch(t, "/api/v1/me/profile", map[string]any{
		"name":  "Abebe Kebede",
		"phone": "+251911223344",
	}, authed)
	requireStatus(t, resp, http.StatusOK)
	assert.Equal(t, "Abebe Kebede", resp.Body["name"])
	assert.Equal(t, "+251911223344", resp.Body["phone"])

	// A partial update leaves untouched fields alone.
	partial := s.patch(t, "/api/v1/me/profile", map[string]any{"name": "Abebe K."}, authed)
	requireStatus(t, partial, http.StatusOK)
	assert.Equal(t, "+251911223344", partial.Body["phone"])

	blank := s.patch(t, "/api/v1/me/profile", map[string]any{"name": "   "}, authed)
	requireError(t, blank, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

func TestIntegrationHealthAndReadiness(t *testing.T) {
	s := newServer(t)

	health := s.get(t, "/healthz")
	requireStatus(t, health, http.StatusOK)
	assert.Equal(t, "ok", health.Body["status"])

	ready := s.get(t, "/readyz")
	requireStatus(t, ready, http.StatusOK)
	assert.Equal(t, "ready", ready.Body["status"])
}

// Unmatched routes must still speak the standard envelope, so a client's
// interceptor never has to special-case a 404 page.
func TestIntegrationUnknownRouteUsesTheErrorEnvelope(t *testing.T) {
	s := newServer(t)

	resp := s.get(t, "/api/v1/nope")
	requireError(t, resp, http.StatusNotFound, "NOT_FOUND")
}

// Sign-up issues a one-time code for the address it was given. Until an SMS or
// email provider is configured the code goes to the log; the test harness
// captures it the same way a recipient would read it.
func TestIntegrationEmailVerification(t *testing.T) {
	s := newServer(t)

	resp := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name":     "Abebe Kebede",
		"email":    "abebe@example.et",
		"password": "password123",
	})
	requireStatus(t, resp, http.StatusCreated)

	user := resp.Body["user"].(map[string]any)
	assert.Equal(t, false, user["emailVerified"], "an address starts unproven")

	code := s.codes.latestFor(t, "abebe@example.et")
	require.Len(t, code, 6, "a six-digit code is what the client prompts for")
	assert.NotContains(t, resp.Raw, code, "the code must never ride back in the response")

	t.Run("a wrong code is refused", func(t *testing.T) {
		wrong := "000000"
		if wrong == code {
			wrong = "111111"
		}
		requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
			"email": "abebe@example.et", "code": wrong,
		}), http.StatusBadRequest, "INVALID_CODE")
	})

	t.Run("an unknown address is refused the same way", func(t *testing.T) {
		requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
			"email": "nobody@example.et", "code": code,
		}), http.StatusBadRequest, "INVALID_CODE")
	})

	t.Run("the right code verifies the address", func(t *testing.T) {
		verify := s.post(t, "/api/v1/auth/verify-email", map[string]any{
			"email": "ABEBE@EXAMPLE.ET", // normalized like everywhere else
			"code":  code,
		})
		requireStatus(t, verify, http.StatusOK)

		session := s.get(t, "/api/v1/auth/session", withToken(s.signIn(t, "abebe@example.et").AccessToken))
		requireStatus(t, session, http.StatusOK)
		assert.Equal(t, true, session.Body["user"].(map[string]any)["emailVerified"])
	})

	t.Run("a code cannot be replayed", func(t *testing.T) {
		requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
			"email": "abebe@example.et", "code": code,
		}), http.StatusBadRequest, "INVALID_CODE")
	})
}

// Resending retires the previous code, so exactly one is ever live.
func TestIntegrationResendVerificationRetiresTheOldCode(t *testing.T) {
	s := newServer(t)

	s.signUp(t, "Selam", "selam@example.et")
	first := s.codes.latestFor(t, "selam@example.et")

	resend := s.post(t, "/api/v1/auth/resend-verification",
		map[string]any{"email": "selam@example.et"})
	requireStatus(t, resend, http.StatusNoContent)

	second := s.codes.latestFor(t, "selam@example.et")
	require.NotEqual(t, first, second, "a resend issues a new code")

	requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
		"email": "selam@example.et", "code": first,
	}), http.StatusBadRequest, "INVALID_CODE")

	requireStatus(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
		"email": "selam@example.et", "code": second,
	}), http.StatusOK)
}

// An address with no account must not be distinguishable from one that has.
func TestIntegrationResendDoesNotRevealRegistrations(t *testing.T) {
	s := newServer(t)

	requireStatus(t, s.post(t, "/api/v1/auth/resend-verification",
		map[string]any{"email": "nobody@example.et"}), http.StatusNoContent)

	requireError(t, s.post(t, "/api/v1/auth/resend-verification",
		map[string]any{"email": "not-an-email"}), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

// Guessing is capped, and hitting the cap burns the code rather than leaving
// it to be ground down.
func TestIntegrationVerificationAttemptsAreCapped(t *testing.T) {
	s := newServer(t)

	s.signUp(t, "Yonas", "yonas@example.et")
	code := s.codes.latestFor(t, "yonas@example.et")

	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}

	for range 5 {
		requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
			"email": "yonas@example.et", "code": wrong,
		}), http.StatusBadRequest, "INVALID_CODE")
	}

	requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
		"email": "yonas@example.et", "code": wrong,
	}), http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")

	// Even the correct code is dead now; a resend is the only way forward.
	requireError(t, s.post(t, "/api/v1/auth/verify-email", map[string]any{
		"email": "yonas@example.et", "code": code,
	}), http.StatusBadRequest, "INVALID_CODE")
}
