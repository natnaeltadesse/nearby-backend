# Backend Architecture & Scaffolding (v1)

> Multi-category Service Reservation Platform — Go backend.
>
> This document is the build spec for a **new Go backend** serving three clients:
> a **customer mobile app** (Expo / React Native), a **provider web app**, and a
> **platform admin web app** (same web bundle, role-gated).
>
> It reuses the auth, tenancy, RBAC, media and payment patterns proven in the
> bottled-water backend, and adds the three things that platform does not have:
> a **category-driven catalog**, a **scheduling/availability engine**, and a
> **cross-tenant discovery surface**.
>
> **For the implementing agent:** section 11 is the build order. Do not skip
> section 4 — the surface split is the single decision everything else depends on.

---

## 1. Context

A marketplace where **service businesses register as tenants** and customers
**reserve time slots** with them. Categories are open-ended: car wash, men's
barber shop, women's salon, laundry, shoe cleaning, and whatever comes next.

Launch plan: one car wash (mandatory reservations, no walk-ins) plus a handful
of appointment-driven men's salons.

**Three actor types:**

| Actor | Client | Belongs to a tenant? |
|---|---|---|
| Customer | mobile app | **No** — reads across all tenants |
| Provider staff (owner / admin / staff) | web app | Yes — one or more |
| Platform admin | web app | No — acts on all tenants |

That first row is the structural difference from a normal B2B multi-tenant app,
and it drives section 4.

---

## 2. Stack

| Concern | Choice |
|---|---|
| Router | `chi` (`net/http`-native) |
| DB | PostgreSQL 16 + **PostGIS** + `btree_gist` |
| DB access | `pgx` + `sqlc` |
| Migrations | `goose` |
| Auth | `golang-jwt/jwt` (access + refresh) + `argon2id` |
| RBAC | hand-rolled role/permission middleware |
| Real-time | `coder/websocket` + FCM push |
| Background jobs | `river` (Postgres-backed) |
| Config | `envconfig` |
| Logging | `slog` |
| API contract | OpenAPI, `oapi-codegen` (spec-first) |
| Media | Cloudinary signed direct upload |
| Payments | Chapa / Telebirr (deposits + full prepay) |
| Testing | `testify` + `testcontainers-go` |
| Deploy | Docker + docker-compose; modular monolith |

Two extensions are non-negotiable from day one:

```sql
CREATE EXTENSION IF NOT EXISTS postgis;     -- nearby-provider search
CREATE EXTENSION IF NOT EXISTS btree_gist;  -- double-booking prevention
```

---

## 3. Project layout

```
booking-backend/
├── cmd/api/main.go
├── cmd/worker/main.go            # river worker (reminders, cleanup)
├── internal/
│   ├── auth/                     # users, sessions, jwt, password, middleware
│   ├── tenant/                   # providers (organizations), members, invitations
│   ├── catalog/                  # categories, attribute defs + validation,
│   │                             # services, option groups, options
│   ├── scheduling/               # resources, hours, exceptions, slot generation
│   ├── booking/                  # lifecycle state machine, conflict prevention
│   ├── discovery/                # public search: geo + category + attributes
│   ├── review/                   # ratings, aggregates
│   ├── media/                    # cloudinary signing, provider gallery
│   ├── payment/                  # chapa / telebirr
│   ├── notify/                   # fcm + websocket fan-out
│   ├── platform/                 # shared: db, config, http, errors, logging
│   └── http/
│       ├── router.go
│       ├── public/               # no auth required, no org scope
│       ├── customer/             # auth required, scoped by user id
│       ├── provider/             # auth + x-organization-id membership check
│       └── admin/                # auth + platform-admin check
├── migrations/
├── api/openapi.yaml
├── docker-compose.yml
└── go.mod
```

### Module boundary rules

These are the rules that keep it clean as categories multiply:

1. **`booking/` and `scheduling/` are category-blind.** They take a duration, a
   resource id and a time range. If a `switch category` ever appears in either,
   the catalog attribute model is missing something — fix it there, not here.
2. **Only `catalog/` knows what a category is.**
3. **`discovery/` reads attribute definitions generically** to build filters. It
   never hard-codes an attribute key.
4. No module imports `http/`. Handlers depend on services, never the reverse.

---

## 4. API surfaces (the core decision)

Four router groups, four different middleware chains. Mount them separately in
`internal/http/router.go`.

```
/api/v1/public/*     no auth (optional bearer for personalization)
                     → categories, provider search, provider detail,
                       services, availability

/api/v1/me/*         auth required, scoped by JWT `sub`
                     → my bookings, my profile, my favorites, my reviews

/api/v1/org/*        auth + `x-organization-id` + membership check
                     → services, options, resources, hours, bookings inbox

/api/v1/admin/*      auth + platform role == admin
                     → categories, attribute definitions, all providers,
                       verification, moderation
```

**Why:** the customer app browses across every tenant and belongs to none. The
org-scoping middleware that protects provider data would make discovery
impossible. Keeping them as separate groups means the `x-organization-id`
middleware mounts on exactly one subtree and can never be accidentally bypassed.

> **Security rule (unchanged from the water backend):** `x-organization-id` is
> client-supplied. On every `/org/*` request, verify the caller is a member of
> that org in `members` before scoping any data. Never trust the header.

---

## 5. Data model

### 5.1 Identity & tenancy

```sql
users (
  id, name, email UNIQUE, phone, password_hash,
  platform_role      text NOT NULL DEFAULT 'user',   -- 'admin' | 'user'
  email_verified     bool NOT NULL DEFAULT false,
  fcm_token          text,
  created_at, updated_at
);

refresh_tokens (
  id, user_id, token_hash, client_type text,          -- 'mobile' | 'web'
  expires_at, revoked_at, created_at
);

providers (                                            -- the tenant
  id, slug UNIQUE, name,
  phone, email, description,
  city, address,
  location           geography(Point, 4326),          -- lng/lat
  logo_url, logo_public_id,
  license_number,
  status             text NOT NULL DEFAULT 'pending', -- pending|active|suspended
  rating_avg         numeric(2,1) NOT NULL DEFAULT 0, -- denormalized cache
  rating_count       int NOT NULL DEFAULT 0,
  booking_mode       text NOT NULL DEFAULT 'request', -- 'request' | 'instant'
  created_at, updated_at
);
CREATE INDEX ON providers USING gist (location);
CREATE INDEX ON providers (status) WHERE status = 'active';

members (
  provider_id, user_id,
  role text NOT NULL,                                  -- owner|admin|staff
  PRIMARY KEY (provider_id, user_id)
);

invitations (id, provider_id, email, role, token_hash, expires_at, accepted_at);
```

`booking_mode` matters early: the car wash may auto-confirm (`instant`), while a
salon may want to approve each request (`request`). Same state machine, one flag.

### 5.2 Category-driven catalog

```sql
categories (
  id, slug UNIQUE, name, icon, parent_id, sort_order, is_active
);

provider_categories (provider_id, category_id, PRIMARY KEY (provider_id, category_id));

category_attributes (
  id, category_id,
  key          text NOT NULL,        -- 'vehicle_size'
  label        text NOT NULL,
  data_type    text NOT NULL,        -- enum|multi_enum|text|number|bool
  options      jsonb,                -- ['sedan','suv','pickup']
  required     bool NOT NULL DEFAULT false,
  applies_to   text NOT NULL,        -- 'service' | 'booking' | 'provider'
  filterable   bool NOT NULL DEFAULT false,
  sort_order   int,
  UNIQUE (category_id, key, applies_to)
);

services (
  id, provider_id, category_id,
  name, description,
  price_cents      int NOT NULL,     -- minor units (santim); 24500 = 245.00 ETB
  currency         text NOT NULL DEFAULT 'ETB',
  duration_minutes int NOT NULL,     -- the scheduling engine's only real input
  buffer_minutes   int NOT NULL DEFAULT 0,
  attributes       jsonb NOT NULL DEFAULT '{}',
  image_url, image_public_id,
  is_active        bool NOT NULL DEFAULT true,
  created_at, updated_at
);
CREATE INDEX ON services USING gin (attributes jsonb_path_ops);
CREATE INDEX ON services (provider_id) WHERE is_active;
```

**Attribute validation** lives in `catalog/validator.go`: load the category's
definitions, check required / data_type / enum membership, reject unknown keys
with `422 VALIDATION_ERROR`. Applies to `services.attributes` on write and
`bookings.attributes` at reservation time.

`GET /public/categories/:id/attributes` returns the definitions so **both
clients render their forms dynamically**. Adding "pet grooming" with its own
fields is then a data insert by a platform admin — no migration, no app release.

### 5.3 Add-ons and variants

Groups give you radio-vs-checkbox semantics with one mechanism.

```sql
option_groups (
  id, service_id,
  name           text NOT NULL,     -- 'Vehicle size' | 'Extras'
  selection_type text NOT NULL,     -- 'single' | 'multi'
  is_required    bool NOT NULL DEFAULT false,
  min_select     int NOT NULL DEFAULT 0,
  max_select     int,
  sort_order     int
);

service_options (
  id, group_id,
  name                   text NOT NULL,   -- 'SUV' | 'Massage' | 'Beard trim'
  price_delta_cents      int NOT NULL DEFAULT 0,
  duration_delta_minutes int NOT NULL DEFAULT 0,
  is_active              bool NOT NULL DEFAULT true,
  sort_order             int
);
```

Covers every case you'll hit:

| Vertical | Group | Type | Options |
|---|---|---|---|
| Car wash | Vehicle size | single, required | Sedan +0, SUV +5000, Pickup +8000 |
| Car wash | Extras | multi | Wax +3000/+20min, Interior +2500/+15min |
| Men's salon | Add-ons | multi | Massage +10000/+20min, Beard trim +5000/+15min |
| Laundry | Weight tier | single, required | ≤5kg +0, ≤10kg +4000 |

Server recomputes total price and duration from the chosen option ids on every
booking. **Never trust client-sent totals.**

### 5.4 Scheduling

```sql
resources (                                    -- a bay, a chair, a barber
  id, provider_id, name,
  user_id       uuid,                          -- nullable: staff who can log in
  is_active     bool NOT NULL DEFAULT true
);

resource_services (resource_id, service_id, PRIMARY KEY (resource_id, service_id));

business_hours (
  id, provider_id,
  resource_id  uuid,                           -- NULL = provider-wide default
  weekday      int NOT NULL,                   -- 0=Sunday
  opens_at     time NOT NULL,
  closes_at    time NOT NULL
);

schedule_exceptions (
  id, provider_id,
  resource_id  uuid,                           -- NULL = whole provider
  date         date NOT NULL,
  is_closed    bool NOT NULL DEFAULT true,
  opens_at     time,                           -- set when is_closed = false
  closes_at    time,
  reason       text
);
```

Store a `timezone` on the provider (`Africa/Addis_Ababa`) and do all slot math in
that zone. Persist every instant as `timestamptz`.

### 5.5 Bookings

```sql
bookings (
  id, code text UNIQUE,                        -- short human ref, e.g. 'BK-7QK2'
  provider_id, service_id, resource_id, customer_id,
  starts_at        timestamptz NOT NULL,
  ends_at          timestamptz NOT NULL,       -- includes buffer
  status           text NOT NULL,
  -- snapshots: never join to live catalog rows for historical bookings
  service_name     text NOT NULL,
  price_cents      int NOT NULL,
  currency         text NOT NULL,
  duration_minutes int NOT NULL,
  attributes       jsonb NOT NULL DEFAULT '{}',
  customer_note    text,
  cancelled_by     text,                       -- 'customer' | 'provider'
  cancel_reason    text,
  created_at, updated_at
);

booking_options (                              -- snapshot of chosen add-ons
  booking_id, option_id,
  name text NOT NULL, price_delta_cents int NOT NULL, duration_delta_minutes int NOT NULL
);
```

**Double-booking prevention belongs in Postgres, not application code:**

```sql
ALTER TABLE bookings ADD CONSTRAINT bookings_no_overlap
  EXCLUDE USING gist (
    resource_id WITH =,
    tstzrange(starts_at, ends_at) WITH &&
  ) WHERE (status IN ('pending', 'confirmed', 'in_progress'));
```

Two customers tapping the same slot: one insert wins, the other gets a
constraint violation → map to `409 SLOT_TAKEN`. No locking, no race.

**Status machine** (enforce transitions in `booking/state.go`):

```
pending ──confirm──> confirmed ──start──> in_progress ──complete──> completed
   │                     │                     │
   └──cancel──> cancelled_by_customer / cancelled_by_provider
                         └──no_show──> no_show
```

`instant` providers skip `pending` and land on `confirmed` at creation.

### 5.6 Reviews & media

```sql
reviews (
  id, booking_id UNIQUE, provider_id, customer_id,
  rating int NOT NULL CHECK (rating BETWEEN 1 AND 5),
  comment text, created_at
);

provider_media (id, provider_id, image_url, image_public_id, caption, sort_order);
favorites (customer_id, provider_id, PRIMARY KEY (customer_id, provider_id));
```

One review per completed booking — that `UNIQUE (booking_id)` plus a check that
the booking is `completed` and owned by the reviewer is your entire anti-spam
story for v1. Update `providers.rating_avg` / `rating_count` in the same
transaction so list queries never fan out.

---

## 6. Auth model

Unchanged from the water backend, with two mobile-driven adjustments.

- **Access token**: JWT, 15 min, `Authorization: Bearer <token>`, verified
  locally — no DB hit on the hot path.
- **Refresh token**: opaque, rotated on every use, stored hashed, revocable.
  **30 days for `web`, 90 days for `mobile`** — record `client_type` on issue.
  Mobile clients store it in `expo-secure-store`, never `AsyncStorage`.

```json
{
  "sub": "user_abc123",
  "email": "abebe@example.et",
  "name": "Abebe Kebede",
  "platformRole": "user",
  "memberships": [{ "providerId": "prov_shine", "role": "owner" }],
  "iat": 1750000000,
  "exp": 1750000900
}
```

A customer simply has `memberships: []`. That is the whole difference between a
customer account and a provider account — no separate user table.

---

## 7. Error envelope & conventions

Flat shape, matching the existing frontend interceptor:

```json
{ "message": "That slot was just taken", "code": "SLOT_TAKEN" }
```

Codes: `INVALID_CREDENTIALS`, `EMAIL_TAKEN`, `UNAUTHENTICATED`, `FORBIDDEN`,
`NOT_A_MEMBER`, `TOKEN_EXPIRED`, `INVALID_TOKEN`, `VALIDATION_ERROR`,
`ORG_REQUIRED`, `NOT_FOUND`, `SLOT_TAKEN`, `OUTSIDE_HOURS`,
`INVALID_TRANSITION`, `SERVICE_INACTIVE`, `PROVIDER_INACTIVE`.

Every authenticated request:

```
Authorization: Bearer <accessToken>
x-organization-id: <providerId>     // /org/* only
Content-Type: application/json
```

Money is always `priceCents` (integer minor units). Times are always RFC3339
UTC on the wire.

---

## 8. Endpoint map

### `/api/v1/public`
```
GET  /categories
GET  /categories/:id/attributes           # drives dynamic forms on both clients
GET  /providers?lat=&lng=&radius=&categoryId=&search=&attr.<key>=&sort=&limit=&offset=
GET  /providers/:slug
GET  /providers/:id/services
GET  /providers/:id/media
GET  /providers/:id/reviews
GET  /services/:id                        # incl. option groups + options
GET  /services/:id/availability?date=&optionIds=
```

### `/api/v1/me`
```
GET    /profile              PATCH /profile
GET    /bookings?status=     POST  /bookings          # 409 SLOT_TAKEN
GET    /bookings/:id         POST  /bookings/:id/cancel
POST   /bookings/:id/review
GET    /favorites            POST/DELETE /favorites/:providerId
POST   /devices              # register FCM token
```

### `/api/v1/org` (all require `x-organization-id`)
```
GET/PATCH  /profile                     # provider profile, hours, location
CRUD       /services                    # + attribute validation
CRUD       /services/:id/option-groups  # + /options
CRUD       /resources
CRUD       /business-hours
CRUD       /schedule-exceptions
GET        /bookings?date=&status=&resourceId=
POST       /bookings/:id/confirm | /start | /complete | /no-show | /cancel
POST       /bookings                    # walk-in entered by staff
GET        /members    POST /invitations
CRUD       /media
GET        /stats/summary
```

### `/api/v1/admin`
```
CRUD /categories        CRUD /categories/:id/attributes
GET  /providers?status= POST /providers/:id/approve | /suspend
GET  /bookings          GET  /users
```

### Shared
```
POST /api/v1/auth/sign-up | sign-in | refresh | sign-out
GET  /api/v1/auth/session
POST /api/v1/uploads/signature            # Cloudinary signed direct upload
```

---

## 9. Availability algorithm

`GET /public/services/:id/availability?date=2026-08-20&optionIds=opt_1,opt_9`

```
1. total_duration = service.duration_minutes
                  + Σ chosen options' duration_delta
                  + service.buffer_minutes
2. resources      = resource_services WHERE service_id AND is_active
3. for each resource:
     window = schedule_exceptions(date) ?? business_hours(resource ?? provider, weekday)
     if closed → skip
     busy   = bookings WHERE resource_id AND date AND status IN (pending, confirmed, in_progress)
     free   = window minus busy
     slots += every start at `slot_step` (15 min) where [start, start+total_duration] ⊆ free
4. drop slots earlier than now + min_lead_minutes
5. return DISTINCT starts, sorted, each with its candidate resource ids
```

Response returns only start times; the client sends back a chosen start and the
server re-derives everything and lets the exclusion constraint arbitrate. Cache
per `(service, date, optionSet)` for ~60s and invalidate on any booking write
for that provider.

---

## 10. Discovery query

The one place geo, category and attributes must cooperate:

```sql
SELECT p.id, p.slug, p.name, p.city, p.logo_url,
       p.rating_avg, p.rating_count,
       ST_Distance(p.location, $1::geography) AS distance_m,
       MIN(s.price_cents) AS from_price_cents
FROM providers p
JOIN provider_categories pc ON pc.provider_id = p.id
JOIN services s ON s.provider_id = p.id AND s.is_active
WHERE p.status = 'active'
  AND ST_DWithin(p.location, $1::geography, $2)      -- radius metres
  AND pc.category_id = $3
  AND s.attributes @> $4::jsonb                       -- built from attr.* params
GROUP BY p.id
ORDER BY distance_m
LIMIT $5 OFFSET $6;
```

`ST_DWithin` uses the GiST index; `@>` uses the GIN index. Build `$4` in
`discovery/filter.go` by looking up `filterable` attribute definitions for the
category — never from raw query params, or you've built an injection surface.

---

## 11. Build order

Each milestone should end green with `testcontainers` integration tests.

1. **Skeleton** — `cmd/api`, chi router, config, slog, error envelope, docker-compose
   with Postgres+PostGIS, goose, sqlc, health check.
2. **Auth** — users, argon2id, JWT, refresh rotation, `client_type`, middleware.
3. **Tenancy** — providers, members, invitations, `x-organization-id` middleware,
   the four router groups mounted with their chains.
4. **Catalog** — categories, attribute definitions, the validator, services.
5. **Options** — option groups, options, server-side price/duration computation.
6. **Scheduling** — resources, hours, exceptions, the slot algorithm.
7. **Booking** — creation with the exclusion constraint, state machine, both
   `instant` and `request` modes, cancellation.
8. **Discovery** — PostGIS search, attribute filters, provider detail.
9. **Media + reviews** — Cloudinary signing, gallery, one-review-per-booking,
   rating aggregates.
10. **Notify** — FCM on booking created/confirmed/cancelled, `river` job for
    reminders (T-2h), WebSocket for the provider bookings inbox.
11. **Payments** — Chapa/Telebirr deposit or prepay, webhook reconciliation.

Milestones 1–7 are a usable product for the launch car wash.

---

## 12. Non-goals for v1

Recurring bookings, waitlists, multi-service baskets in one reservation,
provider payouts and commission ledger, promo codes, chat between customer and
provider, mobile-provider services that travel to the customer, cross-provider
staff. Design nothing around these now — but note that a multi-service basket is
the most likely v2 ask, so keep `bookings` ↔ `booking_options` clean enough that
a `booking_items` table could slot in without reshaping the state machine.

---

## 13. API versioning discipline

Once the mobile app ships to stores, an old version lives in users' hands for
months. `/api/v1` becomes **additive-only**: new optional fields are fine;
removing a field, renaming it, retyping it, or tightening validation is not.
Since the contract is spec-first, add a CI step that diffs `api/openapi.yaml`
against the last released tag and fails the build on a breaking change.