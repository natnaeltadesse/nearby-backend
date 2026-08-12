-- name: CreateBooking :one
-- May fail with 23P01 (bookings_no_overlap). The store maps that to SLOT_TAKEN.
INSERT INTO bookings (
    code, provider_id, service_id, resource_id, customer_id,
    starts_at, ends_at, status,
    service_name, price_cents, currency, duration_minutes, attributes,
    customer_note, customer_name, customer_phone
)
VALUES (
    @code, @provider_id, sqlc.narg(service_id), @resource_id, sqlc.narg(customer_id),
    @starts_at, @ends_at, @status,
    @service_name, @price_cents, @currency, @duration_minutes, @attributes,
    sqlc.narg(customer_note), sqlc.narg(customer_name), sqlc.narg(customer_phone)
)
RETURNING id, code, provider_id, service_id, resource_id, customer_id,
          starts_at, ends_at, status, service_name, price_cents, currency,
          duration_minutes, attributes, customer_note, customer_name,
          customer_phone, cancelled_by, cancel_reason, created_at, updated_at;

-- name: AddBookingOption :exec
INSERT INTO booking_options (booking_id, option_id, name, price_delta_cents, duration_delta_minutes, sort_order)
VALUES (@booking_id, sqlc.narg(option_id), @name, @price_delta_cents, @duration_delta_minutes, @sort_order);

-- name: GetBooking :one
SELECT id, code, provider_id, service_id, resource_id, customer_id,
       starts_at, ends_at, status, service_name, price_cents, currency,
       duration_minutes, attributes, customer_note, customer_name,
       customer_phone, cancelled_by, cancel_reason, created_at, updated_at
FROM bookings
WHERE id = @id;

-- name: ListBookingOptions :many
SELECT booking_id, option_id, name, price_delta_cents, duration_delta_minutes, sort_order
FROM booking_options
WHERE booking_id = ANY (@booking_ids::uuid[])
ORDER BY booking_id, sort_order;

-- name: ListBookingsByCustomer :many
SELECT b.id, b.code, b.provider_id, b.service_id, b.resource_id, b.customer_id,
       b.starts_at, b.ends_at, b.status, b.service_name, b.price_cents, b.currency,
       b.duration_minutes, b.attributes, b.customer_note, b.cancelled_by,
       b.cancel_reason, b.created_at, b.updated_at,
       p.slug AS provider_slug, p.name AS provider_name, p.logo_url AS provider_logo_url,
       p.city AS provider_city, p.timezone AS provider_timezone
FROM bookings b
JOIN providers p ON p.id = b.provider_id
WHERE b.customer_id = @customer_id
  AND (@status::text = '' OR b.status = @status)
ORDER BY b.starts_at DESC
LIMIT @result_limit OFFSET @result_offset;

-- name: CountBookingsByCustomer :one
SELECT count(*) FROM bookings
WHERE customer_id = @customer_id AND (@status::text = '' OR status = @status);

-- The provider's inbox: a day at a time, optionally narrowed to one resource.
-- name: ListBookingsByProvider :many
SELECT b.id, b.code, b.provider_id, b.service_id, b.resource_id, b.customer_id,
       b.starts_at, b.ends_at, b.status, b.service_name, b.price_cents, b.currency,
       b.duration_minutes, b.attributes, b.customer_note, b.customer_name,
       b.customer_phone, b.cancelled_by, b.cancel_reason, b.created_at, b.updated_at,
       r.name AS resource_name,
       u.name AS customer_user_name, u.phone AS customer_user_phone
FROM bookings b
JOIN resources r ON r.id = b.resource_id
LEFT JOIN users u ON u.id = b.customer_id
WHERE b.provider_id = @provider_id
  AND (@status::text = '' OR b.status = @status)
  AND (sqlc.narg(resource_id)::uuid IS NULL OR b.resource_id = sqlc.narg(resource_id))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR b.starts_at >= sqlc.narg(from_time))
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR b.starts_at < sqlc.narg(to_time))
ORDER BY b.starts_at
LIMIT @result_limit OFFSET @result_offset;

-- name: CountBookingsByProvider :one
SELECT count(*) FROM bookings b
WHERE b.provider_id = @provider_id
  AND (@status::text = '' OR b.status = @status)
  AND (sqlc.narg(resource_id)::uuid IS NULL OR b.resource_id = sqlc.narg(resource_id))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR b.starts_at >= sqlc.narg(from_time))
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR b.starts_at < sqlc.narg(to_time));

-- Availability step 3: what already occupies these resources in this window.
-- name: ListBusyIntervals :many
SELECT resource_id, starts_at, ends_at
FROM bookings
WHERE resource_id = ANY (@resource_ids::uuid[])
  AND status IN ('pending', 'confirmed', 'in_progress')
  AND starts_at < @window_end
  AND ends_at > @window_start
ORDER BY resource_id, starts_at;

-- The state machine writes through here. The `from_status` guard makes the
-- transition atomic: a concurrent update leaves this returning no rows.
-- name: TransitionBooking :one
UPDATE bookings
SET status        = @to_status,
    cancelled_by  = sqlc.narg(cancelled_by),
    cancel_reason = sqlc.narg(cancel_reason),
    updated_at    = now()
WHERE id = @id AND status = @from_status
RETURNING id, code, provider_id, service_id, resource_id, customer_id,
          starts_at, ends_at, status, service_name, price_cents, currency,
          duration_minutes, attributes, customer_note, customer_name,
          customer_phone, cancelled_by, cancel_reason, created_at, updated_at;

-- name: ProviderBookingStats :one
SELECT
    count(*) FILTER (WHERE status = 'pending')                     AS pending_count,
    count(*) FILTER (WHERE status = 'confirmed')                   AS confirmed_count,
    count(*) FILTER (WHERE status = 'in_progress')                 AS in_progress_count,
    count(*) FILTER (WHERE status = 'completed')                   AS completed_count,
    count(*) FILTER (WHERE status IN ('cancelled_by_customer', 'cancelled_by_provider'))
                                                                   AS cancelled_count,
    count(*) FILTER (WHERE status = 'no_show')                     AS no_show_count,
    COALESCE(sum(price_cents) FILTER (WHERE status = 'completed'), 0)::bigint
                                                                   AS completed_revenue_cents
FROM bookings
WHERE provider_id = @provider_id
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR starts_at >= sqlc.narg(from_time))
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR starts_at < sqlc.narg(to_time));

-- name: BookingCodeExists :one
SELECT EXISTS (SELECT 1 FROM bookings WHERE code = @code);

-- Platform-admin cross-tenant listing. Deliberately separate from the
-- provider inbox query: it is not org-scoped, so it must never share a code
-- path with one that is.
-- name: ListAllBookings :many
SELECT b.id, b.code, b.provider_id, b.service_id, b.resource_id, b.customer_id,
       b.starts_at, b.ends_at, b.status, b.service_name, b.price_cents, b.currency,
       b.duration_minutes, b.created_at, b.updated_at,
       p.slug AS provider_slug, p.name AS provider_name,
       u.name AS customer_user_name, u.email AS customer_user_email
FROM bookings b
JOIN providers p ON p.id = b.provider_id
LEFT JOIN users u ON u.id = b.customer_id
WHERE (@status::text = '' OR b.status = @status)
  AND (sqlc.narg(provider_id)::uuid IS NULL OR b.provider_id = sqlc.narg(provider_id))
ORDER BY b.starts_at DESC
LIMIT @result_limit OFFSET @result_offset;

-- name: CountAllBookings :one
SELECT count(*) FROM bookings b
WHERE (@status::text = '' OR b.status = @status)
  AND (sqlc.narg(provider_id)::uuid IS NULL OR b.provider_id = sqlc.narg(provider_id));
