-- +goose Up
CREATE TABLE providers (
    id             uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug           text NOT NULL UNIQUE,
    name           text NOT NULL,
    phone          text,
    email          text,
    description    text,
    city           text,
    address        text,
    location       geography(Point, 4326),          -- lng/lat
    -- all slot math happens in this zone; every instant is still stored as UTC
    timezone       text NOT NULL DEFAULT 'Africa/Addis_Ababa',
    logo_url       text,
    logo_public_id text,
    license_number text,
    status         text NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'active', 'suspended')),
    -- denormalized review cache, kept in the same transaction as the review
    rating_avg     numeric(2,1) NOT NULL DEFAULT 0,
    rating_count   integer NOT NULL DEFAULT 0,
    -- 'instant' skips `pending` and confirms at creation; 'request' does not
    booking_mode   text NOT NULL DEFAULT 'request'
                       CHECK (booking_mode IN ('request', 'instant')),
    -- availability step 4: drop slots earlier than now + this
    min_lead_minutes integer NOT NULL DEFAULT 30 CHECK (min_lead_minutes >= 0),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX providers_location_idx ON providers USING gist (location);
CREATE INDEX providers_active_idx ON providers (status) WHERE status = 'active';

CREATE TABLE members (
    provider_id uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role        text NOT NULL CHECK (role IN ('owner', 'admin', 'staff')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_id, user_id)
);

-- Read on every sign-in to build the JWT `memberships` claim.
CREATE INDEX members_user_id_idx ON members (user_id);

CREATE TABLE invitations (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    email       text NOT NULL,
    role        text NOT NULL CHECK (role IN ('owner', 'admin', 'staff')),
    token_hash  text NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    accepted_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX invitations_provider_id_idx ON invitations (provider_id);
CREATE UNIQUE INDEX invitations_pending_email_idx
    ON invitations (provider_id, lower(email))
    WHERE accepted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS members;
DROP TABLE IF EXISTS providers;
