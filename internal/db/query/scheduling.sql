-- Times of day cross the sqlc boundary as minutes since midnight rather than
-- pgtype.Time: the slot algorithm is pure integer arithmetic that way, and
-- scheduling.TimeOfDay renders them back as "HH:MM" at the HTTP edge.

-- name: CreateResource :one
INSERT INTO resources (provider_id, name, user_id, is_active)
VALUES (@provider_id, @name, sqlc.narg(user_id), @is_active)
RETURNING id, provider_id, name, user_id, is_active, created_at, updated_at;

-- name: GetResource :one
SELECT id, provider_id, name, user_id, is_active, created_at, updated_at
FROM resources
WHERE id = @id;

-- name: ListResourcesByProvider :many
SELECT id, provider_id, name, user_id, is_active, created_at, updated_at
FROM resources
WHERE provider_id = @provider_id
  AND (@active_only::bool = false OR is_active)
ORDER BY name;

-- Availability step 2: which resources can actually perform this service.
-- name: ListResourcesForService :many
SELECT r.id, r.provider_id, r.name, r.user_id, r.is_active
FROM resource_services rs
JOIN resources r ON r.id = rs.resource_id
WHERE rs.service_id = @service_id AND r.is_active
ORDER BY r.name;

-- name: UpdateResource :one
UPDATE resources
SET name       = COALESCE(sqlc.narg(name), name),
    user_id    = COALESCE(sqlc.narg(user_id), user_id),
    is_active  = COALESCE(sqlc.narg(is_active), is_active),
    updated_at = now()
WHERE id = @id AND provider_id = @provider_id
RETURNING id, provider_id, name, user_id, is_active, created_at, updated_at;

-- name: DeleteResource :execrows
DELETE FROM resources WHERE id = @id AND provider_id = @provider_id;

-- name: AddResourceService :exec
INSERT INTO resource_services (resource_id, service_id)
VALUES (@resource_id, @service_id)
ON CONFLICT DO NOTHING;

-- name: RemoveResourceService :execrows
DELETE FROM resource_services
WHERE resource_id = @resource_id AND service_id = @service_id;

-- name: ListServiceIDsForResource :many
SELECT service_id FROM resource_services WHERE resource_id = @resource_id;

-- ---------------------------------------------------------------- business hours

-- name: CreateBusinessHours :one
INSERT INTO business_hours (provider_id, resource_id, weekday, opens_at, closes_at)
VALUES (
    @provider_id, sqlc.narg(resource_id), @weekday,
    make_interval(mins => @opens_minute::int)::time,
    make_interval(mins => @closes_minute::int)::time
)
RETURNING id, provider_id, resource_id, weekday,
          (EXTRACT(EPOCH FROM opens_at) / 60)::int AS opens_minute,
          (EXTRACT(EPOCH FROM closes_at) / 60)::int AS closes_minute,
          created_at, updated_at;

-- name: ListBusinessHours :many
SELECT id, provider_id, resource_id, weekday,
       (EXTRACT(EPOCH FROM opens_at) / 60)::int AS opens_minute,
       (EXTRACT(EPOCH FROM closes_at) / 60)::int AS closes_minute,
       created_at, updated_at
FROM business_hours
WHERE provider_id = @provider_id
ORDER BY weekday, opens_at;

-- Availability step 3: the day's window for one resource. Resource-specific
-- rows win; the provider-wide default (resource_id IS NULL) is the fallback.
-- name: ListBusinessHoursForDay :many
SELECT id, provider_id, resource_id, weekday,
       (EXTRACT(EPOCH FROM opens_at) / 60)::int AS opens_minute,
       (EXTRACT(EPOCH FROM closes_at) / 60)::int AS closes_minute
FROM business_hours
WHERE provider_id = @provider_id
  AND weekday = @weekday
  AND (resource_id IS NULL OR resource_id = ANY (@resource_ids::uuid[]))
ORDER BY opens_at;

-- name: UpdateBusinessHours :one
UPDATE business_hours
SET weekday   = COALESCE(sqlc.narg(weekday), weekday),
    opens_at  = COALESCE(make_interval(mins => sqlc.narg(opens_minute)::int)::time, opens_at),
    closes_at = COALESCE(make_interval(mins => sqlc.narg(closes_minute)::int)::time, closes_at),
    updated_at = now()
WHERE id = @id AND provider_id = @provider_id
RETURNING id, provider_id, resource_id, weekday,
          (EXTRACT(EPOCH FROM opens_at) / 60)::int AS opens_minute,
          (EXTRACT(EPOCH FROM closes_at) / 60)::int AS closes_minute,
          created_at, updated_at;

-- name: DeleteBusinessHours :execrows
DELETE FROM business_hours WHERE id = @id AND provider_id = @provider_id;

-- ---------------------------------------------------------------- exceptions

-- name: CreateScheduleException :one
INSERT INTO schedule_exceptions (provider_id, resource_id, date, is_closed, opens_at, closes_at, reason)
VALUES (
    @provider_id, sqlc.narg(resource_id), @date, @is_closed,
    make_interval(mins => sqlc.narg(opens_minute)::int)::time,
    make_interval(mins => sqlc.narg(closes_minute)::int)::time,
    sqlc.narg(reason)
)
RETURNING id, provider_id, resource_id, date, is_closed,
          opens_at, closes_at,
          reason, created_at, updated_at;

-- name: ListScheduleExceptions :many
SELECT id, provider_id, resource_id, date, is_closed,
       opens_at, closes_at,
       reason, created_at, updated_at
FROM schedule_exceptions
WHERE provider_id = @provider_id
  AND (sqlc.narg(from_date)::date IS NULL OR date >= sqlc.narg(from_date))
  AND (sqlc.narg(to_date)::date IS NULL OR date <= sqlc.narg(to_date))
ORDER BY date;

-- Availability step 3: an exception for this date overrides business_hours.
-- name: ListScheduleExceptionsForDate :many
SELECT id, provider_id, resource_id, date, is_closed,
       opens_at, closes_at,
       reason
FROM schedule_exceptions
WHERE provider_id = @provider_id AND date = @date;

-- name: UpdateScheduleException :one
UPDATE schedule_exceptions
SET date       = COALESCE(sqlc.narg(date), date),
    is_closed  = COALESCE(sqlc.narg(is_closed), is_closed),
    opens_at   = make_interval(mins => sqlc.narg(opens_minute)::int)::time,
    closes_at  = make_interval(mins => sqlc.narg(closes_minute)::int)::time,
    reason     = COALESCE(sqlc.narg(reason), reason),
    updated_at = now()
WHERE id = @id AND provider_id = @provider_id
RETURNING id, provider_id, resource_id, date, is_closed,
          opens_at, closes_at,
          reason, created_at, updated_at;

-- name: DeleteScheduleException :execrows
DELETE FROM schedule_exceptions WHERE id = @id AND provider_id = @provider_id;
