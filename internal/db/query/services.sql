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
