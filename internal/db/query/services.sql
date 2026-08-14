-- name: CreateService :one
INSERT INTO services (
    provider_id, category_id, name, description, price_cents, currency,
    duration_minutes, buffer_minutes, attributes, is_active
)
VALUES (
    @provider_id, @category_id, @name, sqlc.narg(description), @price_cents, @currency,
    @duration_minutes, @buffer_minutes, @attributes, @is_active
)
RETURNING id, provider_id, category_id, name, description, price_cents, currency,
          duration_minutes, buffer_minutes, attributes, image_url, image_public_id,
          is_active, created_at, updated_at;

-- name: GetService :one
SELECT id, provider_id, category_id, name, description, price_cents, currency,
       duration_minutes, buffer_minutes, attributes, image_url, image_public_id,
       is_active, created_at, updated_at
FROM services
WHERE id = @id;

-- Everything booking needs about a service in one round trip: the provider's
-- booking_mode, timezone and status decide the rest of the flow.
-- name: GetServiceWithProvider :one
SELECT s.id, s.provider_id, s.category_id, s.name, s.description, s.price_cents,
       s.currency, s.duration_minutes, s.buffer_minutes, s.attributes,
       s.image_url, s.is_active,
       p.slug AS provider_slug, p.name AS provider_name, p.status AS provider_status,
       p.booking_mode, p.timezone, p.min_lead_minutes
FROM services s
JOIN providers p ON p.id = s.provider_id
WHERE s.id = @id;

-- name: ListServicesByProvider :many
SELECT id, provider_id, category_id, name, description, price_cents, currency,
       duration_minutes, buffer_minutes, attributes, image_url, image_public_id,
       is_active, created_at, updated_at
FROM services
WHERE provider_id = @provider_id
  AND (@active_only::bool = false OR is_active)
ORDER BY name;

-- name: UpdateService :one
UPDATE services
SET category_id      = COALESCE(sqlc.narg(category_id), category_id),
    name             = COALESCE(sqlc.narg(name), name),
    description      = COALESCE(sqlc.narg(description), description),
    price_cents      = COALESCE(sqlc.narg(price_cents), price_cents),
    currency         = COALESCE(sqlc.narg(currency), currency),
    duration_minutes = COALESCE(sqlc.narg(duration_minutes), duration_minutes),
    buffer_minutes   = COALESCE(sqlc.narg(buffer_minutes), buffer_minutes),
    attributes       = COALESCE(sqlc.narg(attributes), attributes),
    is_active        = COALESCE(sqlc.narg(is_active), is_active),
    updated_at       = now()
WHERE id = @id AND provider_id = @provider_id
RETURNING id, provider_id, category_id, name, description, price_cents, currency,
          duration_minutes, buffer_minutes, attributes, image_url, image_public_id,
          is_active, created_at, updated_at;

-- name: SetServiceImage :one
UPDATE services
SET image_url = sqlc.narg(image_url), image_public_id = sqlc.narg(image_public_id),
    updated_at = now()
WHERE id = @id AND provider_id = @provider_id
RETURNING id, image_url, image_public_id;

-- name: DeleteService :execrows
DELETE FROM services WHERE id = @id AND provider_id = @provider_id;

-- The org catalog list: search, filter, sort and paginate, all in the
-- database. sqlc cannot interpolate an ORDER BY, so the sort key arrives as a
-- whitelisted string and is resolved by CASE — which keeps it a bound
-- parameter rather than concatenated SQL.
--
-- The join is what lets `category` be a sortable column: sorting on
-- category_id would order by a uuid, which is nothing a reader recognises.
-- name: ListServicesPage :many
SELECT s.id, s.provider_id, s.category_id, c.name AS category_name,
       s.name, s.description, s.price_cents, s.currency,
       s.duration_minutes, s.buffer_minutes, s.attributes,
       s.image_url, s.image_public_id, s.is_active, s.created_at, s.updated_at
FROM services s
LEFT JOIN categories c ON c.id = s.category_id
WHERE s.provider_id = @provider_id
  AND (sqlc.narg(is_active)::bool IS NULL OR s.is_active = sqlc.narg(is_active))
  AND (sqlc.narg(category_id)::uuid IS NULL OR s.category_id = sqlc.narg(category_id))
  AND (@search::text = ''
       OR s.name ILIKE '%' || @search || '%'
       OR COALESCE(s.description, '') ILIKE '%' || @search || '%')
ORDER BY
    CASE WHEN @sort_by::text = 'name'     AND NOT @sort_desc::bool THEN s.name END ASC,
    CASE WHEN @sort_by::text = 'name'     AND     @sort_desc::bool THEN s.name END DESC,
    CASE WHEN @sort_by::text = 'category' AND NOT @sort_desc::bool THEN c.name END ASC,
    CASE WHEN @sort_by::text = 'category' AND     @sort_desc::bool THEN c.name END DESC,
    CASE WHEN @sort_by::text = 'price'    AND NOT @sort_desc::bool THEN s.price_cents END ASC,
    CASE WHEN @sort_by::text = 'price'    AND     @sort_desc::bool THEN s.price_cents END DESC,
    CASE WHEN @sort_by::text = 'duration' AND NOT @sort_desc::bool THEN s.duration_minutes END ASC,
    CASE WHEN @sort_by::text = 'duration' AND     @sort_desc::bool THEN s.duration_minutes END DESC,
    CASE WHEN @sort_by::text = 'created'  AND NOT @sort_desc::bool THEN s.created_at END ASC,
    CASE WHEN @sort_by::text = 'created'  AND     @sort_desc::bool THEN s.created_at END DESC,
    -- Ties broken by id so paging never repeats or skips a row: without a
    -- total order, LIMIT/OFFSET can return the same row on two pages.
    s.name ASC, s.id ASC
LIMIT @result_limit OFFSET @result_offset;

-- name: CountServicesPage :one
SELECT count(*)
FROM services s
WHERE s.provider_id = @provider_id
  AND (sqlc.narg(is_active)::bool IS NULL OR s.is_active = sqlc.narg(is_active))
  AND (sqlc.narg(category_id)::uuid IS NULL OR s.category_id = sqlc.narg(category_id))
  AND (@search::text = ''
       OR s.name ILIKE '%' || @search || '%'
       OR COALESCE(s.description, '') ILIKE '%' || @search || '%');

-- Catalog-wide figures for the provider dashboard's stat cards. Deliberately
-- unfiltered: these describe the whole catalog, while the table beneath them
-- describes the current query.
-- name: ServiceCatalogStats :one
SELECT
    count(*)::bigint                                          AS total,
    count(*) FILTER (WHERE is_active)::bigint                 AS active,
    COALESCE(min(price_cents), 0)::int                        AS price_min_cents,
    COALESCE(max(price_cents), 0)::int                        AS price_max_cents,
    COALESCE(round(avg(price_cents)), 0)::int                 AS price_avg_cents,
    COALESCE(min(duration_minutes), 0)::int                   AS duration_min_minutes,
    COALESCE(max(duration_minutes), 0)::int                   AS duration_max_minutes,
    COALESCE(round(avg(duration_minutes)), 0)::int            AS duration_avg_minutes
FROM services
WHERE provider_id = @provider_id;

-- name: ServiceCountByCategory :many
SELECT s.category_id, c.name AS category_name, count(*)::bigint AS service_count
FROM services s
LEFT JOIN categories c ON c.id = s.category_id
WHERE s.provider_id = @provider_id
GROUP BY s.category_id, c.name
ORDER BY count(*) DESC, c.name;

-- Services added per month over a rolling window, for the catalog-growth area
-- chart. `prior_total` is what existed before the window, so the running total
-- the client draws starts from the truth rather than from zero.
-- name: ServicesAddedByMonth :many
SELECT date_trunc('month', created_at)::date AS month, count(*)::bigint AS added
FROM services
WHERE provider_id = @provider_id
  AND created_at >= date_trunc('month', now()) - make_interval(months => @months_back::int)
GROUP BY 1
ORDER BY 1;

-- name: ServicesCreatedBefore :one
SELECT count(*)::bigint
FROM services
WHERE provider_id = @provider_id
  AND created_at < date_trunc('month', now()) - make_interval(months => @months_back::int);
