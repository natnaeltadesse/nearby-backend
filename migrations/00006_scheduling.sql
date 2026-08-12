-- +goose Up
-- A bay, a chair, a barber. The scheduling engine knows nothing else about it.
CREATE TABLE resources (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    name        text NOT NULL,
    user_id     uuid REFERENCES users (id) ON DELETE SET NULL,  -- staff who can log in
    is_active   boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX resources_provider_id_idx ON resources (provider_id);

CREATE TABLE resource_services (
    resource_id uuid NOT NULL REFERENCES resources (id) ON DELETE CASCADE,
    service_id  uuid NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    PRIMARY KEY (resource_id, service_id)
);

CREATE INDEX resource_services_service_id_idx ON resource_services (service_id);

CREATE TABLE business_hours (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    resource_id uuid REFERENCES resources (id) ON DELETE CASCADE,  -- NULL = provider-wide default
    weekday     integer NOT NULL CHECK (weekday BETWEEN 0 AND 6),  -- 0 = Sunday
    opens_at    time NOT NULL,
    closes_at   time NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT business_hours_opens_before_closes CHECK (opens_at < closes_at)
);

CREATE INDEX business_hours_provider_weekday_idx
    ON business_hours (provider_id, weekday);
CREATE INDEX business_hours_resource_idx
    ON business_hours (resource_id, weekday) WHERE resource_id IS NOT NULL;

CREATE TABLE schedule_exceptions (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    resource_id uuid REFERENCES resources (id) ON DELETE CASCADE,  -- NULL = whole provider
    date        date NOT NULL,
    is_closed   boolean NOT NULL DEFAULT true,
    opens_at    time,                                              -- set when is_closed = false
    closes_at   time,
    reason      text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- an open exception must say when; a closed one must not
    CONSTRAINT schedule_exceptions_open_needs_window CHECK (
        (is_closed AND opens_at IS NULL AND closes_at IS NULL)
        OR (NOT is_closed AND opens_at IS NOT NULL AND closes_at IS NOT NULL
            AND opens_at < closes_at)
    )
);

CREATE INDEX schedule_exceptions_provider_date_idx
    ON schedule_exceptions (provider_id, date);
-- one exception per (scope, date); NULL resource_id is its own scope
CREATE UNIQUE INDEX schedule_exceptions_provider_scope_idx
    ON schedule_exceptions (provider_id, date) WHERE resource_id IS NULL;
CREATE UNIQUE INDEX schedule_exceptions_resource_scope_idx
    ON schedule_exceptions (resource_id, date) WHERE resource_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS schedule_exceptions;
DROP TABLE IF EXISTS business_hours;
DROP TABLE IF EXISTS resource_services;
DROP TABLE IF EXISTS resources;
