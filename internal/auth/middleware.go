package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

type contextKey struct{ name string }

var identityKey = contextKey{"auth.identity"}

// Identity is the authenticated caller, derived entirely from a verified
// access token — no database round trip on the hot path.
type Identity struct {
	UserID       uuid.UUID
	Email        string
	Name         string
	PlatformRole string
	Memberships  []Membership
}

// IsAdmin reports whether the caller is a platform administrator.
func (i Identity) IsAdmin() bool { return i.PlatformRole == RoleAdmin }

// MembershipRole returns the caller's role at providerID, as claimed by the
// token. It is a fast pre-check only: /org/* still verifies membership against
// the database, because a token outlives a revoked membership by up to its TTL.
func (i Identity) MembershipRole(providerID uuid.UUID) (string, bool) {
	for _, m := range i.Memberships {
		if m.ProviderID == providerID {
			return m.Role, true
		}
	}
	return "", false
}

// WithIdentity returns a context carrying the caller's identity.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

// IdentityFrom returns the caller's identity, if the request was authenticated.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey).(Identity)
	return identity, ok
}

// MustIdentity returns the caller's identity or an UNAUTHENTICATED error.
// Handlers mounted behind RequireAuth can treat the error branch as impossible,
// but returning it is cheaper than trusting the mount point.
func MustIdentity(ctx context.Context) (Identity, error) {
	identity, ok := IdentityFrom(ctx)
	if !ok {
		return Identity{}, httpx.Unauthenticated("Authentication is required")
	}
	return identity, nil
}

// RequireAuth rejects requests without a valid bearer token.
func RequireAuth(issuer *TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				httpx.WriteError(w, r, httpx.Unauthenticated("Authentication is required"))
				return
			}

			claims, err := issuer.ParseAccessToken(raw)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}

			identity, err := identityFromClaims(claims)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
		})
	}
}

// OptionalAuth attaches an identity when a valid token is present and
// otherwise does nothing. It is what lets /public/* personalize (favourites,
// "you have booked here before") without ever requiring a session.
//
// An invalid or expired token is ignored rather than rejected: a customer
// browsing with a stale token should still see the catalog.
func OptionalAuth(issuer *TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			claims, err := issuer.ParseAccessToken(raw)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			identity, err := identityFromClaims(claims)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
		})
	}
}

// RequirePlatformAdmin gates the /admin/* subtree. Mount it after RequireAuth.
func RequirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := MustIdentity(r.Context())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if !identity.IsAdmin() {
			httpx.WriteError(w, r, httpx.Forbidden("Platform administrator access is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func identityFromClaims(claims *Claims) (Identity, error) {
	userID, err := claims.UserID()
	if err != nil {
		return Identity{}, httpx.InvalidToken().WithCause(err)
	}
	return Identity{
		UserID:       userID,
		Email:        claims.Email,
		Name:         claims.Name,
		PlatformRole: claims.PlatformRole,
		Memberships:  claims.Memberships,
	}, nil
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}
