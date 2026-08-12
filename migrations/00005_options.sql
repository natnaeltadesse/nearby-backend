-- +goose Up
-- Radio-vs-checkbox semantics with one mechanism: a group is `single` or
-- `multi`, and min/max bound how many of its options a booking may pick.
CREATE TABLE option_groups (
    id             uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    service_id     uuid NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    name           text NOT NULL,                 -- 'Vehicle size' | 'Extras'
    selection_type text NOT NULL CHECK (selection_type IN ('single', 'multi')),
    is_required    boolean NOT NULL DEFAULT false,
    min_select     integer NOT NULL DEFAULT 0 CHECK (min_select >= 0),
    max_select     integer CHECK (max_select IS NULL OR max_select >= 1),
    sort_order     integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT option_groups_min_le_max CHECK (
        max_select IS NULL OR min_select <= max_select
    ),
    -- a single-select group can never take more than one option
    CONSTRAINT option_groups_single_max_one CHECK (
        selection_type <> 'single' OR max_select IS NULL OR max_select = 1
    )
);

CREATE INDEX option_groups_service_id_idx ON option_groups (service_id);

CREATE TABLE service_options (
    id                     uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    group_id               uuid NOT NULL REFERENCES option_groups (id) ON DELETE CASCADE,
    name                   text NOT NULL,         -- 'SUV' | 'Massage' | 'Beard trim'
    price_delta_cents      integer NOT NULL DEFAULT 0,
    duration_delta_minutes integer NOT NULL DEFAULT 0,
    is_active              boolean NOT NULL DEFAULT true,
    sort_order             integer NOT NULL DEFAULT 0,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX service_options_group_id_idx ON service_options (group_id);

-- +goose Down
DROP TABLE IF EXISTS service_options;
DROP TABLE IF EXISTS option_groups;
