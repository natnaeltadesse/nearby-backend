-- name: ListServiceMedia :many
SELECT id, service_id, provider_id, image_url, image_public_id, caption,
       sort_order, created_at
FROM service_media
WHERE service_id = @service_id
ORDER BY sort_order, created_at;

-- name: CountServiceMedia :one
SELECT count(*) FROM service_media WHERE service_id = @service_id;

-- New images land after the ones already there, so upload order is preserved
-- without the client having to say so.
-- name: NextServiceMediaSortOrder :one
SELECT COALESCE(max(sort_order) + 1, 0)::int
FROM service_media
WHERE service_id = @service_id;

-- name: CreateServiceMedia :one
INSERT INTO service_media (service_id, provider_id, image_url, image_public_id, caption, sort_order)
VALUES (@service_id, @provider_id, @image_url, @image_public_id, sqlc.narg(caption), @sort_order)
RETURNING id, service_id, provider_id, image_url, image_public_id, caption, sort_order, created_at;

-- Scoped on provider_id as well as id: a leaked media id from another tenant
-- still finds nothing.
-- name: GetServiceMedia :one
SELECT id, service_id, provider_id, image_url, image_public_id, caption,
       sort_order, created_at
FROM service_media
WHERE id = @id AND provider_id = @provider_id;

-- name: UpdateServiceMedia :one
UPDATE service_media
SET caption    = COALESCE(sqlc.narg(caption), caption),
    sort_order = COALESCE(sqlc.narg(sort_order), sort_order)
WHERE id = @id AND provider_id = @provider_id
RETURNING id, service_id, provider_id, image_url, image_public_id, caption, sort_order, created_at;

-- name: DeleteServiceMedia :one
DELETE FROM service_media
WHERE id = @id AND provider_id = @provider_id
RETURNING image_public_id;

-- name: ServiceExistsForProvider :one
SELECT EXISTS (
    SELECT 1 FROM services WHERE id = @id AND provider_id = @provider_id
);
