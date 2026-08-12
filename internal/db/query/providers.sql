-- The `location` column is a PostGIS geography, which sqlc has no Go mapping
-- for. Every query therefore projects it as an explicit has_location/lng/lat
-- triple and never selects the raw column.

-- name: CreateProvider :one
INSERT INTO providers (
    slug, name, phone, email, description, city, address, location,
    timezone, license_number, booking_mode, min_lead_minutes
)
VALUES (
    @slug, @name, sqlc.narg(phone), sqlc.narg(email), sqlc.narg(description),
    sqlc.narg(city), sqlc.narg(address),
    CASE
        WHEN sqlc.narg(lng)::float8 IS NULL OR sqlc.narg(lat)::float8 IS NULL THEN NULL
        ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lng)::float8, sqlc.narg(lat)::float8), 4326)::geography
    END,
    @timezone, sqlc.narg(license_number), @booking_mode, @min_lead_minutes
)
RETURNING
    id, slug, name, phone, email, description, city, address,
    (location IS NOT NULL)::bool AS has_location,
    COALESCE(ST_X(location::geometry), 0)::float8 AS lng,
    COALESCE(ST_Y(location::geometry), 0)::float8 AS lat,
    timezone, logo_url, logo_public_id, license_number, status,
    rating_avg::float8 AS rating_avg, rating_count, booking_mode,
    min_lead_minutes, created_at, updated_at;

-- name: GetProviderByID :one
SELECT
    id, slug, name, phone, email, description, city, address,
    (location IS NOT NULL)::bool AS has_location,
    COALESCE(ST_X(location::geometry), 0)::float8 AS lng,
    COALESCE(ST_Y(location::geometry), 0)::float8 AS lat,
    timezone, logo_url, logo_public_id, license_number, status,
    rating_avg::float8 AS rating_avg, rating_count, booking_mode,
    min_lead_minutes, created_at, updated_at
FROM providers
WHERE id = @id;

-- name: GetProviderBySlug :one
SELECT
    id, slug, name, phone, email, description, city, address,
    (location IS NOT NULL)::bool AS has_location,
    COALESCE(ST_X(location::geometry), 0)::float8 AS lng,
    COALESCE(ST_Y(location::geometry), 0)::float8 AS lat,
    timezone, logo_url, logo_public_id, license_number, status,
    rating_avg::float8 AS rating_avg, rating_count, booking_mode,
    min_lead_minutes, created_at, updated_at
FROM providers
WHERE slug = @slug;

-- name: UpdateProvider :one
UPDATE providers
SET name           = COALESCE(sqlc.narg(name), name),
    phone          = COALESCE(sqlc.narg(phone), phone),
    email          = COALESCE(sqlc.narg(email), email),
    description    = COALESCE(sqlc.narg(description), description),
    city           = COALESCE(sqlc.narg(city), city),
    address        = COALESCE(sqlc.narg(address), address),
    timezone       = COALESCE(sqlc.narg(timezone), timezone),
    license_number = COALESCE(sqlc.narg(license_number), license_number),
    booking_mode   = COALESCE(sqlc.narg(booking_mode), booking_mode),
    min_lead_minutes = COALESCE(sqlc.narg(min_lead_minutes), min_lead_minutes),
    -- location moves only when both halves of the coordinate are supplied
    location = CASE
        WHEN sqlc.narg(lng)::float8 IS NULL OR sqlc.narg(lat)::float8 IS NULL THEN location
        ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lng)::float8, sqlc.narg(lat)::float8), 4326)::geography
    END,
    updated_at = now()
WHERE id = @id
RETURNING
    id, slug, name, phone, email, description, city, address,
    (location IS NOT NULL)::bool AS has_location,
    COALESCE(ST_X(location::geometry), 0)::float8 AS lng,
    COALESCE(ST_Y(location::geometry), 0)::float8 AS lat,
    timezone, logo_url, logo_public_id, license_number, status,
    rating_avg::float8 AS rating_avg, rating_count, booking_mode,
    min_lead_minutes, created_at, updated_at;

-- name: SetProviderStatus :one
UPDATE providers
SET status = @status, updated_at = now()
WHERE id = @id
RETURNING
    id, slug, name, status, booking_mode, timezone, min_lead_minutes,
    created_at, updated_at;

-- name: ListProviders :many
SELECT
    id, slug, name, phone, email, city, address,
    (location IS NOT NULL)::bool AS has_location,
    COALESCE(ST_X(location::geometry), 0)::float8 AS lng,
    COALESCE(ST_Y(location::geometry), 0)::float8 AS lat,
    timezone, logo_url, status,
    rating_avg::float8 AS rating_avg, rating_count, booking_mode,
    created_at, updated_at
FROM providers
WHERE (@status::text = '' OR status = @status)
  AND (@search::text = '' OR name ILIKE '%' || @search || '%' OR slug ILIKE '%' || @search || '%')
ORDER BY created_at DESC
LIMIT @result_limit OFFSET @result_offset;

-- name: CountProviders :one
SELECT count(*) FROM providers
WHERE (@status::text = '' OR status = @status)
  AND (@search::text = '' OR name ILIKE '%' || @search || '%' OR slug ILIKE '%' || @search || '%');

-- name: SlugExists :one
SELECT EXISTS (SELECT 1 FROM providers WHERE slug = @slug);
