-- name: CreateCategory :one
INSERT INTO categories (slug, name, icon, parent_id, sort_order, is_active)
VALUES (@slug, @name, sqlc.narg(icon), sqlc.narg(parent_id), @sort_order, @is_active)
RETURNING id, slug, name, icon, parent_id, sort_order, is_active, created_at, updated_at;

-- name: GetCategoryByID :one
SELECT id, slug, name, icon, parent_id, sort_order, is_active, created_at, updated_at
FROM categories
WHERE id = @id;

-- name: GetCategoryBySlug :one
SELECT id, slug, name, icon, parent_id, sort_order, is_active, created_at, updated_at
FROM categories
WHERE slug = @slug;

-- name: ListCategories :many
SELECT id, slug, name, icon, parent_id, sort_order, is_active, created_at, updated_at
FROM categories
WHERE (@active_only::bool = false OR is_active)
ORDER BY sort_order, name;

-- name: UpdateCategory :one
UPDATE categories
SET slug       = COALESCE(sqlc.narg(slug), slug),
    name       = COALESCE(sqlc.narg(name), name),
    icon       = COALESCE(sqlc.narg(icon), icon),
    parent_id  = COALESCE(sqlc.narg(parent_id), parent_id),
    sort_order = COALESCE(sqlc.narg(sort_order), sort_order),
    is_active  = COALESCE(sqlc.narg(is_active), is_active),
    updated_at = now()
WHERE id = @id
RETURNING id, slug, name, icon, parent_id, sort_order, is_active, created_at, updated_at;

-- name: DeleteCategory :execrows
DELETE FROM categories WHERE id = @id;

-- ---------------------------------------------------------------- provider_categories

-- name: AddProviderCategory :exec
INSERT INTO provider_categories (provider_id, category_id)
VALUES (@provider_id, @category_id)
ON CONFLICT DO NOTHING;

-- name: RemoveProviderCategory :execrows
DELETE FROM provider_categories
WHERE provider_id = @provider_id AND category_id = @category_id;

-- name: ListProviderCategories :many
SELECT c.id, c.slug, c.name, c.icon, c.parent_id, c.sort_order, c.is_active
FROM provider_categories pc
JOIN categories c ON c.id = pc.category_id
WHERE pc.provider_id = @provider_id
ORDER BY c.sort_order, c.name;

-- name: ReplaceProviderCategories :exec
WITH deleted AS (
    DELETE FROM provider_categories
    WHERE provider_id = @provider_id
      AND category_id <> ALL (@category_ids::uuid[])
)
INSERT INTO provider_categories (provider_id, category_id)
SELECT @provider_id, unnest(@category_ids::uuid[])
ON CONFLICT DO NOTHING;
