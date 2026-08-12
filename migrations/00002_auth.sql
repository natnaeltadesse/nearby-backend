-- +goose Up
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    name           text NOT NULL,
    -- stored lowercased by the application; the unique index is the real guard
    email          text NOT NULL UNIQUE,
    phone          text,
    password_hash  text NOT NULL,
    platform_role  text NOT NULL DEFAULT 'user' CHECK (platform_role IN ('admin', 'user')),
    email_verified boolean NOT NULL DEFAULT false,
    fcm_token      text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Opaque, rotated on every use, stored only as a hash. `client_type` decides
-- the TTL at issue time: 30d for web, 90d for mobile.
CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    client_type text NOT NULL CHECK (client_type IN ('mobile', 'web')),
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);
-- supports the worker's periodic cleanup of dead tokens
CREATE INDEX refresh_tokens_expires_at_idx ON refresh_tokens (expires_at)
    WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
