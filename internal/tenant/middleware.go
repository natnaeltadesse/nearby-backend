package tenant

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// OrgHeader is the client-supplied tenant selector.
const OrgHeader = "x-organization-id"

type contextKey struct{ name string }

var orgKey = contextKey{"tenant.org"}

// Org is a verified org scope: the provider the request acts on, and the
// caller's role there as read from the database.
type Org struct {
	ProviderID uuid.UUID
	Role       string
}

// CanManage reports whether the caller may change settings, staff and catalog
// (as opposed to just working the bookings inbox).
func (o Org) CanManage() bool { return RoleAtLeast(o.Role, RoleAdmin) }

// IsOwner reports whether the caller owns the provider.
func (o Org) IsOwner() bool { return o.Role == RoleOwner }

// WithOrg returns a context carrying a verified org scope.
func WithOrg(ctx context.Context, org Org) context.Context {
	return context.WithValue(ctx, orgKey, org)
}

// OrgFrom returns the verified org scope, if the request has one.
func OrgFrom(ctx context.Context) (Org, bool) {
	org, ok := ctx.Value(orgKey).(Org)
	return org, ok
}

// MustOrg returns the verified org scope or an ORG_REQUIRED error.
func MustOrg(ctx context.Context) (Org, error) {
	org, ok := OrgFrom(ctx)
	if !ok {
		return Org{}, httpx.OrgRequired()
	}
	return org, nil
}

// RequireOrg resolves `x-organization-id` into a verified scope.
//
// The header is client-supplied, so it is never trusted on its own: this
// middleware reads the caller's membership row out of the database on every
// request and rejects with NOT_A_MEMBER when there isn't one. The JWT's
// memberships claim is not consulted — it can be up to one access-token TTL
// out of date, which is exactly long enough for a removed employee to keep
// reading their old employer's bookings.
//
// Mount it on the /org subtree only. Because it is the sole mount point, there
// is no route that can accidentally serve org-scoped data without it.
func RequireOrg(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, err := auth.MustIdentity(r.Context())
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}

			raw := strings.TrimSpace(r.Header.Get(OrgHeader))
			if raw == "" {
				httpx.WriteError(w, r, httpx.OrgRequired())
				return
			}

			providerID, err := uuid.Parse(raw)
			if err != nil {
				httpx.WriteError(w, r, httpx.Validation(OrgHeader+" must be a UUID"))
				return
			}

			role, err := svc.Membership(r.Context(), providerID, identity.UserID)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}

			ctx := WithOrg(r.Context(), Org{ProviderID: providerID, Role: role})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireOrgRole gates a route on a minimum role within the already-verified
// org scope. Mount it inside RequireOrg.
func RequireOrgRole(minimum string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			org, err := MustOrg(r.Context())
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if !RoleAtLeast(org.Role, minimum) {
				httpx.WriteError(w, r, httpx.Forbidden("Your role does not allow that action"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
