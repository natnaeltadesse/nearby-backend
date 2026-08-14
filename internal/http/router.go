// Package httpapi mounts the four API surfaces.
//
// The split in router.go is the single decision everything else depends on
// (spec §4). A customer browses across every tenant and belongs to none, so the
// org-scoping middleware that protects provider data would make discovery
// impossible. Keeping the surfaces as separate groups means `x-organization-id`
// enforcement mounts on exactly one subtree and cannot be accidentally bypassed
// by a route added in the wrong place.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/booking"
	"github.com/nearby/booking-backend/internal/catalog"
	"github.com/nearby/booking-backend/internal/http/admin"
	"github.com/nearby/booking-backend/internal/http/customer"
	"github.com/nearby/booking-backend/internal/http/provider"
	"github.com/nearby/booking-backend/internal/http/public"
	"github.com/nearby/booking-backend/internal/http/shared"
	"github.com/nearby/booking-backend/internal/media"
	"github.com/nearby/booking-backend/internal/platform/config"
	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/scheduling"
	"github.com/nearby/booking-backend/internal/tenant"
)

// Services is everything the router wires together. Each group receives only
// the services it needs — public/ never sees the booking service, and nothing
// but the org subtree sees the membership check.
type Services struct {
	Auth      *auth.Service
	Tenant    *tenant.Service
	Catalog   *catalog.Catalog
	Scheduler *scheduling.Scheduler
	Booking   *booking.Service
	Media     *media.Service
	Issuer    *auth.TokenIssuer
	// LocalMedia is set only when uploads are kept on this machine. When it is
	// nil the bytes live with a hosted provider and this API serves none.
	LocalMedia *media.LocalStorage
}

// NewRouter builds the complete HTTP handler.
func NewRouter(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, svc Services) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(httpx.RequestLogger(logger))
	r.Use(httpx.Recoverer)
	r.Use(httpx.CORS(cfg.CORSAllowedOrigins))
	r.Use(middleware.Timeout(30 * time.Second))

	r.NotFound(httpx.NotFoundHandler())
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler())

	// Uploaded images, when they are kept locally. Unauthenticated on purpose:
	// these are the same pictures the public app shows, and the keys are
	// unguessable, so a token would buy nothing.
	if svc.LocalMedia != nil {
		r.Get("/media/{key}", localMediaHandler(svc.LocalMedia))
	}

	r.Get("/healthz", healthHandler(pool))
	r.Get("/readyz", readyHandler(pool))

	// GET /docs (Swagger UI) and GET /openapi.yaml (the contract itself).
	if cfg.DocsEnabled {
		mountDocs(r)
	}

	r.Route("/api/v1", func(v1 chi.Router) {
		// Shared: no surface of its own. Sign-in cannot require a token, and
		// GET /session applies its own.
		v1.Mount("/auth", shared.New(svc.Auth, svc.Issuer).Routes())

		// ---------------------------------------------------------------
		// /public — no auth required.
		//
		// OptionalAuth attaches an identity when a valid bearer token happens
		// to be present, and ignores a bad one rather than rejecting it: a
		// customer browsing with an expired token should still see the catalog.
		// ---------------------------------------------------------------
		v1.Group(func(pub chi.Router) {
			pub.Use(auth.OptionalAuth(svc.Issuer))
			pub.Mount("/public", public.New(svc.Catalog, svc.Tenant, svc.Scheduler).Routes())
		})

		// ---------------------------------------------------------------
		// /me — auth required, scoped by the JWT subject.
		//
		// No org scope at all: these routes read across every tenant on behalf
		// of one user.
		// ---------------------------------------------------------------
		v1.Group(func(me chi.Router) {
			me.Use(auth.RequireAuth(svc.Issuer))
			me.Mount("/me", customer.New(svc.Auth, svc.Tenant, svc.Booking).Routes())
		})

		// ---------------------------------------------------------------
		// /org — auth + x-organization-id + a database membership check.
		//
		// RequireOrg is mounted here and nowhere else. The header is
		// client-supplied and is never trusted: every request re-reads the
		// caller's row in `members` before any org-scoped data is touched.
		// ---------------------------------------------------------------
		v1.Group(func(org chi.Router) {
			org.Use(auth.RequireAuth(svc.Issuer))
			org.Use(tenant.RequireOrg(svc.Tenant))
			org.Mount("/org", provider.New(
				svc.Tenant, svc.Catalog, svc.Scheduler, svc.Booking, svc.Media).Routes())
		})

		// ---------------------------------------------------------------
		// /admin — auth + platform role == admin.
		//
		// Platform admins act on all tenants and belong to none, so this
		// subtree deliberately does not go through the org middleware.
		// ---------------------------------------------------------------
		v1.Group(func(adm chi.Router) {
			adm.Use(auth.RequireAuth(svc.Issuer))
			adm.Use(auth.RequirePlatformAdmin)
			adm.Mount("/admin", admin.New(svc.Tenant, svc.Catalog, svc.Booking, pool).Routes())
		})
	})

	return r
}

// localMediaHandler serves an uploaded image off local disk.
//
// nosniff is not decoration here: this is user-supplied content served from
// the API's own origin, and the whole safety argument rests on the browser
// treating it as the type we say it is rather than guessing.
func localMediaHandler(storage *media.LocalStorage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file, contentType, err := storage.Open(chi.URLParam(r, "key"))
		if err != nil {
			httpx.WriteError(w, r, httpx.NotFound("Image"))
			return
		}
		defer func() { _ = file.Close() }()

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, r, "", time.Time{}, file)
	}
}

// healthHandler reports that the process is up. Deliberately does not touch
// the database: an orchestrator restarting the API because Postgres blinked is
// rarely what anyone wants.
func healthHandler(_ *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// readyHandler reports whether the API can actually serve traffic, which does
// mean reaching the database.
func readyHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return httpx.H(func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := contextWithTimeout(r, 3*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			return &httpx.Error{
				Status:  http.StatusServiceUnavailable,
				Code:    httpx.CodeInternal,
				Message: "The database is not reachable",
			}
		}
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
		return nil
	})
}
