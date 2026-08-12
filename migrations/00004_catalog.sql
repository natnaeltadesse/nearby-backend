-- +goose Up
CREATE TABLE categories (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    icon       text,
    parent_id  uuid REFERENCES categories (id) ON DELETE SET NULL,
    sort_order integer NOT NULL DEFAULT 0,
    is_active  boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX categories_parent_id_idx ON categories (parent_id);

CREATE TABLE provider_categories (
    provider_id uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    category_id uuid NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    PRIMARY KEY (provider_id, category_id)
);

CREATE INDEX provider_categories_category_id_idx ON provider_categories (category_id);

-- The whole point of the platform: adding a vertical is an INSERT here, not a
-- migration and not an app release. Both clients render their forms from these.
CREATE TABLE category_attributes (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    category_id uuid NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    key         text NOT NULL,                    -- 'vehicle_size'
    label       text NOT NULL,
    data_type   text NOT NULL
                    CHECK (data_type IN ('enum', 'multi_enum', 'text', 'number', 'bool')),
    options     jsonb,                            -- ['sedan','suv','pickup'] for (multi_)enum
    required    boolean NOT NULL DEFAULT false,
    applies_to  text NOT NULL CHECK (applies_to IN ('service', 'booking', 'provider')),
    filterable  boolean NOT NULL DEFAULT false,
    sort_order  integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (category_id, key, applies_to),
    -- enum types are meaningless without a non-empty option list
    CONSTRAINT category_attributes_enum_needs_options CHECK (
        data_type NOT IN ('enum', 'multi_enum')
        OR (options IS NOT NULL AND jsonb_array_length(options) > 0)
    )
);

CREATE TABLE services (
    id               uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id      uuid NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    category_id      uuid NOT NULL REFERENCES categories (id) ON DELETE RESTRICT,
    name             text NOT NULL,
    description      text,
    -- minor units (santim): 24500 = 245.00 ETB
    price_cents      integer NOT NULL CHECK (price_cents >= 0),
    currency         text NOT NULL DEFAULT 'ETB',
    -- the scheduling engine's only real input
    duration_minutes integer NOT NULL CHECK (duration_minutes > 0),
    buffer_minutes   integer NOT NULL DEFAULT 0 CHECK (buffer_minutes >= 0),
    attributes       jsonb NOT NULL DEFAULT '{}',
    image_url        text,
    image_public_id  text,
    is_active        boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- `@>` containment for discovery's attribute filters
CREATE INDEX services_attributes_idx ON services USING gin (attributes jsonb_path_ops);
CREATE INDEX services_provider_id_idx ON services (provider_id) WHERE is_active;
CREATE INDEX services_category_id_idx ON services (category_id);

-- +goose Down
DROP TABLE IF EXISTS services;
DROP TABLE IF EXISTS category_attributes;
DROP TABLE IF EXISTS provider_categories;
DROP TABLE IF EXISTS categories;
