# Booking Platform — Go backend

Multi-category service reservation platform. Service businesses register as
tenants; customers reserve time slots with them. Categories are open-ended —
car wash, barber, laundry, and whatever comes next — because a new vertical is
a data insert, not a migration.

Built to [`backend.md`](backend.md), through **milestone 7** of its build order:
skeleton → auth → tenancy → catalog → options → scheduling → booking.

This repo is the API only. The customer app (Expo / React Native) and the
provider + platform admin web app are separate repos that consume
[`api/openapi.yaml`](api/openapi.yaml) as their contract.

---

## 60-second quickstart

You need Go 1.26+ and Docker. Nothing else — every tool is pinned and run
through `go run`.

```bash
make setup     # .env, deps, Postgres on :5433, migrations
make seed      # demo car wash with services, options, bays and hours
make dev       # hot-reloading API on :8081
```

Check it:

```bash
curl localhost:8081/healthz
curl localhost:8081/api/v1/public/categories
```

**Interactive docs: <http://localhost:8081/docs>** — Swagger UI over
`api/openapi.yaml`, with "Try it out" enabled. Paste an access token into
**Authorize** and it applies to every call. The raw contract is at
`/openapi.yaml`, served from the binary, so it works with no network; the UI
itself pulls Swagger from a CDN. Set `DOCS_ENABLED=false` to turn both off.

Sign in as the seeded provider owner (every seeded account uses `password123`):

```bash
curl -s -X POST localhost:8081/api/v1/auth/sign-in \
  -H 'content-type: application/json' \
  -d '{"email":"owner@shinewash.et","password":"password123"}'
```

| Account | Role |
|---|---|
| `admin@nearby.et` | platform admin |
| `owner@shinewash.et` | owner of Shine Car Wash |
| `staff@shinewash.et` | staff at Shine Car Wash |
| `customer@example.et` | a plain customer |

`make help` lists every target.

> Postgres binds host port **5433**, not 5432, so it coexists with another
> Postgres you may already be running.

---

## Try the whole flow

```bash
SVC=00000000-0000-0000-0000-0000000000e1   # Exterior wash, 45m + 10m buffer
SEDAN=00000000-0000-0000-0000-00000000010a
WAX=00000000-0000-0000-0000-00000000010d
DATE=$(date -u -d '+3 days' +%F)

TOKEN=$(curl -s -X POST localhost:8081/api/v1/auth/sign-in \
  -H 'content-type: application/json' \
  -d '{"email":"customer@example.et","password":"password123"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["tokens"]["accessToken"])')

# What's free? Options change the duration, so they change the answer.
curl -s "localhost:8081/api/v1/public/services/$SVC/availability?date=$DATE&optionIds=$SEDAN,$WAX"

# Book one. No price or duration in the request — the server derives both.
curl -s -X POST localhost:8081/api/v1/me/bookings \
  -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d "{\"serviceId\":\"$SVC\",\"startsAt\":\"${DATE}T06:00:00Z\",
       \"optionIds\":[\"$SEDAN\",\"$WAX\"],
       \"attributes\":{\"vehicle_size\":\"suv\",\"plate_number\":\"AA-12345\"}}"
```

---

## Layout

```
cmd/api            HTTP server
cmd/worker         background jobs (token cleanup today; river later)
internal/
  auth/            users, argon2id, JWT, refresh rotation, middleware
  tenant/          providers, members, invitations, the org middleware
  catalog/         categories, attribute defs + validator, services, options
  scheduling/      resources, hours, exceptions, slot generation
  booking/         lifecycle state machine, conflict prevention
  platform/        config, database, httpx (errors/JSON), logging
  db/              sqlc-generated queries (do not edit)
  apitypes/        oapi-codegen-generated models (do not edit)
  http/            router.go + one package per surface
migrations/        goose; also the sqlc schema
api/openapi.yaml   the contract
scripts/           init-db.sql, seed.sql
test/integration/  testcontainers-backed end-to-end tests
```

### Module boundary rules

1. **`booking/` and `scheduling/` are category-blind.** They take a duration, a
   resource id and a time range. There is no `switch category` in either, and
   there must never be — if one seems necessary, the catalog attribute model is
   missing something. `scheduling.ServiceResolver` is how this is enforced
   structurally: scheduling declares the narrow thing it needs (a duration, a
   provider, a timezone) and `catalog` implements it, so the slot generator is
   never handed a category at all.
2. **Only `catalog/` knows what a category is.**
3. No module imports `http/`. Handlers depend on services, never the reverse.

---

## The four surfaces

The split in `internal/http/router.go` is the decision everything else depends
on. Four groups, four different middleware chains:

| Prefix | Chain | For |
|---|---|---|
| `/api/v1/public` | optional bearer | browsing across every tenant |
| `/api/v1/me` | bearer required, scoped by `sub` | one customer's own data |
| `/api/v1/org` | bearer + `x-organization-id` + **DB membership check** | provider staff |
| `/api/v1/admin` | bearer + `platformRole == admin` | platform administrators |

A customer browses across every tenant and belongs to none, so the org-scoping
middleware that protects provider data would make discovery impossible. Keeping
the surfaces separate means `tenant.RequireOrg` mounts on exactly one subtree
and cannot be bypassed by a route added in the wrong place.

**`x-organization-id` is client-supplied and never trusted.** Every `/org/*`
request re-reads the caller's row in `members` before touching org-scoped data.
The JWT carries a `memberships` claim too, but it is only a hint — it can be up
to one access-token TTL stale, which is exactly long enough for a removed
employee to keep reading their old employer's bookings.

---

## Three things worth knowing

**Double-booking prevention lives in Postgres, not in Go.**

```sql
ALTER TABLE bookings ADD CONSTRAINT bookings_no_overlap
  EXCLUDE USING gist (resource_id WITH =, tstzrange(starts_at, ends_at) WITH &&)
  WHERE (status IN ('pending', 'confirmed', 'in_progress'));
```

Two customers tapping the same slot both reach the `INSERT`. One wins; the
other gets `409 SLOT_TAKEN`. No locking, no race. Where a provider has several
bays, the loser of the first is offered the second before the slot is declared
gone. Under heavy contention Postgres sometimes breaks the tie with a deadlock
rather than a clean constraint violation, which the booking store retries and
then reports as `SLOT_TAKEN` — contention must never surface as a 500.

**The server never trusts a client's totals.** `POST /bookings` has no price,
duration or end-time field at all. `catalog.BuildQuote` recomputes both from the
chosen option ids, and `bookings` snapshots the result, so a later price change
cannot rewrite what someone already paid.

**Adding a vertical is a data insert.** `category_attributes` drives the forms
in both clients via `GET /public/categories/:id/attributes`, and
`catalog/validator.go` enforces the same definitions on `services.attributes`
at write time and `bookings.attributes` at reservation time.

---

## Development

```bash
make test              # unit only, ~2s, no Docker
make test-integration  # testcontainers; spins real Postgres+PostGIS
make test-all          # both
make test-cover        # both, plus a coverage total
make lint fmt tidy
make generate          # sqlc + oapi-codegen
make migrate-create name=add_reviews
make db-reset          # nuke → up → migrate → seed
make psql
```

Every target reports one line per package and a count:

```
✓  internal/scheduling (1.014s)
✖  internal/booking (10ms)

=== Failed
=== FAIL: internal/booking TestActiveStatusesMatchTheExclusionConstraint (0.00s)
    state_test.go:106: Should be false

DONE 115 tests, 55 skipped, 1 failure in 1.934s
```

The skips in `make test` are the integration suite declining to start Docker.
They skip out loud on purpose — a suite that silently reports success for work
it never attempted is worse than one that fails.

Integration tests need Docker and take ~30s (~50s under `-race`); they run a
real Postgres+PostGIS container, migrate it with goose, and drive the API
through `httptest` with the same middleware chains production uses.

Migrations are goose SQL files in `migrations/`, and double as the schema sqlc
reads — one source of truth.

### Conventions

- Money is an integer in minor units (`priceCents`); 24500 = 245.00 ETB.
- Times are RFC3339 **UTC** on the wire. `cmd/api` pins `time.Local` to UTC so
  the serialization cannot drift with the host's zone; slot maths always uses
  the provider's own `timezone` column, loaded explicitly.
- Opening hours cross the wire as `"HH:MM"` in the provider's zone, and the
  database boundary as minutes since midnight — the slot algorithm is then pure
  integer arithmetic.
- Errors are always `{"message": string, "code": string}`, plus an optional
  `details` map on validation failures.

### Logs

Development gets one compact line per record, tinted by HTTP status — green
2xx, cyan 3xx, yellow 4xx, red 5xx — so a failing request is visible without
reading it. Everything else gets JSON, where colour would corrupt the fields.

`LOG_COLOR` is `auto` (colour only when stdout is a terminal), `always` or
`never`; `NO_COLOR` is honoured. Redirecting to a file or a log shipper stays
plain automatically.

### Notes on the toolchain

`sqlc` has no mapping for PostGIS `geography`, so no query ever selects that
column directly: `providers` projects an explicit `hasLocation`/`lng`/`lat`
triple instead, and writes go through `ST_SetSRID(ST_MakePoint(...))`.

`oapi-codegen` generates **models only**. The generated chi server binds one
router per spec, which would either flatten the four-surface split or force four
spec files that drift apart, so handlers are hand-written and the spec remains
the contract for the wire format.

---

## Not built yet

Milestones 8–11, deliberately absent rather than stubbed:

- **8 — Discovery.** `GET /public/providers` (PostGIS radius + attribute
  filters). The schema is ready: `providers.location` is indexed with GiST,
  `services.attributes` with GIN, and `category_attributes.filterable` marks
  what may be filtered on.
- **9 — Media and reviews.** Cloudinary signing, galleries, one review per
  completed booking, rating aggregates.
- **10 — Notifications.** FCM on booking events, `river` reminders at T-2h,
  WebSocket for the provider inbox.
- **11 — Payments.** Chapa / Telebirr deposits and webhook reconciliation.

Config placeholders for Cloudinary, Chapa and Telebirr are already in
`.env.example`.

### API versioning

Once the mobile app ships, an old version lives in users' hands for months.
`/api/v1` is **additive-only**: new optional fields are fine; removing,
renaming or retyping a field, or tightening validation, is not. Worth adding a
CI step that diffs `api/openapi.yaml` against the last released tag.
