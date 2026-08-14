-- +goose Up

-- Photos of work done, shown on a service. Separate from `services.image_url`
-- (the single hero image): that is what the service *is*, this is a portfolio
-- of what it produced.
--
-- provider_id is denormalised so ownership is checkable without joining
-- through services — the org middleware already knows the provider, and a
-- delete that scopes on both columns cannot touch another tenant's row even
-- if a service id leaks.
CREATE TABLE service_media (
    id              uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    service_id      uuid NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    provider_id     uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    image_url       text NOT NULL,
    -- The storage key, kept so the bytes can be removed with the row. Which
    -- backend wrote it is not recorded: the key is opaque to this table.
    image_public_id text NOT NULL,
    caption         text,
    sort_order      integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX service_media_service_idx
    ON service_media (service_id, sort_order, created_at);
CREATE INDEX service_media_provider_idx ON service_media (provider_id);

-- +goose Down
DROP TABLE IF EXISTS service_media;
