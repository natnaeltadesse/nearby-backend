-- +goose Up
CREATE TABLE bookings (
    id               uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    code             text NOT NULL UNIQUE,        -- short human ref, e.g. 'BK-7QK2'
    provider_id      uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    service_id       uuid REFERENCES services (id) ON DELETE SET NULL,
    resource_id      uuid NOT NULL REFERENCES resources (id) ON DELETE RESTRICT,
    customer_id      uuid REFERENCES users (id) ON DELETE SET NULL,  -- NULL for staff walk-ins
    starts_at        timestamptz NOT NULL,
    ends_at          timestamptz NOT NULL,        -- includes buffer
    status           text NOT NULL CHECK (status IN (
                         'pending', 'confirmed', 'in_progress', 'completed',
                         'cancelled_by_customer', 'cancelled_by_provider', 'no_show')),

    -- Snapshots. Never join to live catalog rows for historical bookings: a
    -- price change must not rewrite what someone already paid.
    service_name     text NOT NULL,
    price_cents      integer NOT NULL CHECK (price_cents >= 0),
    currency         text NOT NULL DEFAULT 'ETB',
    duration_minutes integer NOT NULL CHECK (duration_minutes > 0),
    attributes       jsonb NOT NULL DEFAULT '{}',

    customer_note    text,
    customer_name    text,                        -- walk-ins have no user row
    customer_phone   text,
    cancelled_by     text CHECK (cancelled_by IN ('customer', 'provider')),
    cancel_reason    text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT bookings_ends_after_starts CHECK (ends_at > starts_at)
);

-- Double-booking prevention belongs in Postgres, not application code. Two
-- customers tapping the same slot: one INSERT wins, the other gets 23P01,
-- which the booking store maps to 409 SLOT_TAKEN. No locking, no race.
ALTER TABLE bookings ADD CONSTRAINT bookings_no_overlap
    EXCLUDE USING gist (
        resource_id WITH =,
        tstzrange(starts_at, ends_at) WITH &&
    ) WHERE (status IN ('pending', 'confirmed', 'in_progress'));

CREATE INDEX bookings_provider_starts_at_idx ON bookings (provider_id, starts_at DESC);
CREATE INDEX bookings_customer_idx ON bookings (customer_id, starts_at DESC)
    WHERE customer_id IS NOT NULL;
-- the availability query: busy ranges for one resource on one day
CREATE INDEX bookings_resource_active_idx ON bookings (resource_id, starts_at)
    WHERE status IN ('pending', 'confirmed', 'in_progress');

-- Snapshot of the add-ons chosen at reservation time, for the same reason.
CREATE TABLE booking_options (
    booking_id             uuid NOT NULL REFERENCES bookings (id) ON DELETE CASCADE,
    option_id              uuid REFERENCES service_options (id) ON DELETE SET NULL,
    name                   text NOT NULL,
    price_delta_cents      integer NOT NULL,
    duration_delta_minutes integer NOT NULL,
    sort_order             integer NOT NULL DEFAULT 0
);

CREATE INDEX booking_options_booking_id_idx ON booking_options (booking_id);
-- an option may appear at most once per booking (option_id NULL only after the
-- catalog row is deleted, at which point the snapshot columns carry the truth)
CREATE UNIQUE INDEX booking_options_unique_idx ON booking_options (booking_id, option_id)
    WHERE option_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS booking_options;
DROP TABLE IF EXISTS bookings;
