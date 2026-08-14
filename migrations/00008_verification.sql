-- +goose Up

-- One-time codes proving someone controls the address they signed up with.
--
-- Kept in their own table rather than as columns on `users` so the same
-- mechanism covers phone verification (and, later, password resets) without a
-- second migration: `channel` says which address `destination` refers to.
CREATE TABLE verification_codes (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel     text NOT NULL CHECK (channel IN ('email', 'phone')),
    -- Snapshot of the address the code was sent to. Comparing against this
    -- rather than the live users row means changing an email cannot retarget
    -- a code that is already in flight.
    destination text NOT NULL,
    -- Stored hashed, like every other credential here. A six-digit code has
    -- little entropy to protect, so the real defences are the short TTL and
    -- the attempt counter below.
    code_hash   text NOT NULL,
    attempts    integer NOT NULL DEFAULT 0,
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Lookup is always "the live code for this address", so the partial index
-- matches the only query shape that exists.
CREATE INDEX verification_codes_live_idx
    ON verification_codes (channel, lower(destination), created_at DESC)
    WHERE consumed_at IS NULL;

CREATE INDEX verification_codes_user_id_idx ON verification_codes (user_id);

-- +goose Down
DROP TABLE IF EXISTS verification_codes;
