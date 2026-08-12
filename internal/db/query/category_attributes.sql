-- name: CreateCategoryAttribute :one
INSERT INTO category_attributes (
    category_id, key, label, data_type, options, required, applies_to, filterable, sort_order
)
VALUES (
    @category_id, @key, @label, @data_type, sqlc.narg(options), @required,
    @applies_to, @filterable, @sort_order
)
RETURNING id, category_id, key, label, data_type, options, required, applies_to,
          filterable, sort_order, created_at, updated_at;

-- name: GetCategoryAttribute :one
SELECT id, category_id, key, label, data_type, options, required, applies_to,
       filterable, sort_order, created_at, updated_at
FROM category_attributes
WHERE id = @id;

-- Drives the dynamic forms on both clients, and the validator on every write.
-- name: ListCategoryAttributes :many
SELECT id, category_id, key, label, data_type, options, required, applies_to,
       filterable, sort_order, created_at, updated_at
FROM category_attributes
WHERE category_id = @category_id
  AND (@applies_to::text = '' OR applies_to = @applies_to)
ORDER BY sort_order, key;

-- discovery/filter.go builds its jsonb containment filter from these, never
-- from raw query params.
-- name: ListFilterableAttributes :many
SELECT id, category_id, key, label, data_type, options, required, applies_to,
       filterable, sort_order
FROM category_attributes
WHERE category_id = @category_id AND filterable
ORDER BY sort_order, key;

-- name: UpdateCategoryAttribute :one
UPDATE category_attributes
SET label      = COALESCE(sqlc.narg(label), label),
    data_type  = COALESCE(sqlc.narg(data_type), data_type),
    options    = COALESCE(sqlc.narg(options), options),
    required   = COALESCE(sqlc.narg(required), required),
    filterable = COALESCE(sqlc.narg(filterable), filterable),
    sort_order = COALESCE(sqlc.narg(sort_order), sort_order),
    updated_at = now()
WHERE id = @id
RETURNING id, category_id, key, label, data_type, options, required, applies_to,
          filterable, sort_order, created_at, updated_at;

-- name: DeleteCategoryAttribute :execrows
DELETE FROM category_attributes WHERE id = @id;
