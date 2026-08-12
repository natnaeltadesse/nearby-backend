-- name: CreateOptionGroup :one
INSERT INTO option_groups (service_id, name, selection_type, is_required, min_select, max_select, sort_order)
VALUES (@service_id, @name, @selection_type, @is_required, @min_select, sqlc.narg(max_select), @sort_order)
RETURNING id, service_id, name, selection_type, is_required, min_select, max_select,
          sort_order, created_at, updated_at;

-- name: GetOptionGroup :one
SELECT id, service_id, name, selection_type, is_required, min_select, max_select,
       sort_order, created_at, updated_at
FROM option_groups
WHERE id = @id;

-- name: ListOptionGroupsByService :many
SELECT id, service_id, name, selection_type, is_required, min_select, max_select,
       sort_order, created_at, updated_at
FROM option_groups
WHERE service_id = @service_id
ORDER BY sort_order, name;

-- name: UpdateOptionGroup :one
UPDATE option_groups
SET name           = COALESCE(sqlc.narg(name), name),
    selection_type = COALESCE(sqlc.narg(selection_type), selection_type),
    is_required    = COALESCE(sqlc.narg(is_required), is_required),
    min_select     = COALESCE(sqlc.narg(min_select), min_select),
    max_select     = COALESCE(sqlc.narg(max_select), max_select),
    sort_order     = COALESCE(sqlc.narg(sort_order), sort_order),
    updated_at     = now()
WHERE id = @id AND service_id = @service_id
RETURNING id, service_id, name, selection_type, is_required, min_select, max_select,
          sort_order, created_at, updated_at;

-- name: DeleteOptionGroup :execrows
DELETE FROM option_groups WHERE id = @id AND service_id = @service_id;

-- ---------------------------------------------------------------- options

-- name: CreateServiceOption :one
INSERT INTO service_options (group_id, name, price_delta_cents, duration_delta_minutes, is_active, sort_order)
VALUES (@group_id, @name, @price_delta_cents, @duration_delta_minutes, @is_active, @sort_order)
RETURNING id, group_id, name, price_delta_cents, duration_delta_minutes, is_active,
          sort_order, created_at, updated_at;

-- name: ListOptionsByGroup :many
SELECT id, group_id, name, price_delta_cents, duration_delta_minutes, is_active,
       sort_order, created_at, updated_at
FROM service_options
WHERE group_id = @group_id
ORDER BY sort_order, name;

-- One round trip for a service's whole option tree.
-- name: ListOptionsByService :many
SELECT o.id, o.group_id, o.name, o.price_delta_cents, o.duration_delta_minutes,
       o.is_active, o.sort_order, o.created_at, o.updated_at
FROM service_options o
JOIN option_groups g ON g.id = o.group_id
WHERE g.service_id = @service_id
ORDER BY g.sort_order, o.sort_order, o.name;

-- The pricing query: given the option ids a client claims it picked, return
-- what they actually are. Totals are recomputed from this, never trusted.
-- name: ListOptionsByIDs :many
SELECT o.id, o.group_id, o.name, o.price_delta_cents, o.duration_delta_minutes,
       o.is_active, o.sort_order,
       g.service_id, g.name AS group_name, g.selection_type, g.is_required,
       g.min_select, g.max_select
FROM service_options o
JOIN option_groups g ON g.id = o.group_id
WHERE o.id = ANY (@ids::uuid[])
ORDER BY g.sort_order, o.sort_order;

-- name: UpdateServiceOption :one
UPDATE service_options
SET name                   = COALESCE(sqlc.narg(name), name),
    price_delta_cents      = COALESCE(sqlc.narg(price_delta_cents), price_delta_cents),
    duration_delta_minutes = COALESCE(sqlc.narg(duration_delta_minutes), duration_delta_minutes),
    is_active              = COALESCE(sqlc.narg(is_active), is_active),
    sort_order             = COALESCE(sqlc.narg(sort_order), sort_order),
    updated_at             = now()
WHERE id = @id AND group_id = @group_id
RETURNING id, group_id, name, price_delta_cents, duration_delta_minutes, is_active,
          sort_order, created_at, updated_at;

-- name: DeleteServiceOption :execrows
DELETE FROM service_options WHERE id = @id AND group_id = @group_id;
