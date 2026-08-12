-- Development seed data: `make seed`, or `make db-reset` for a clean slate.
--
-- Everything uses fixed UUIDs and ON CONFLICT DO NOTHING so the script is
-- re-runnable and so tests and API clients can hardcode ids.
--
-- Every account's password is: password123
-- (argon2id, m=64MiB t=3 p=2 — the same parameters the API mints today.)

BEGIN;

-- ---------------------------------------------------------------- users
INSERT INTO users (id, name, email, phone, password_hash, platform_role, email_verified)
VALUES
    ('00000000-0000-0000-0000-0000000000a1', 'Platform Admin', 'admin@nearby.et', '+251911000001',
     '$argon2id$v=19$m=65536,t=3,p=2$v/x2oKPkofmtjSeStasxRg$KPNxA3ctVCp0SYSwnT2j6spy+syHM4yJgHbQHcDrXDU',
     'admin', true),
    ('00000000-0000-0000-0000-0000000000a2', 'Dawit Bekele', 'owner@shinewash.et', '+251911000002',
     '$argon2id$v=19$m=65536,t=3,p=2$v/x2oKPkofmtjSeStasxRg$KPNxA3ctVCp0SYSwnT2j6spy+syHM4yJgHbQHcDrXDU',
     'user', true),
    ('00000000-0000-0000-0000-0000000000a3', 'Sara Tesfaye', 'staff@shinewash.et', '+251911000003',
     '$argon2id$v=19$m=65536,t=3,p=2$v/x2oKPkofmtjSeStasxRg$KPNxA3ctVCp0SYSwnT2j6spy+syHM4yJgHbQHcDrXDU',
     'user', true),
    ('00000000-0000-0000-0000-0000000000a4', 'Abebe Kebede', 'customer@example.et', '+251911000004',
     '$argon2id$v=19$m=65536,t=3,p=2$v/x2oKPkofmtjSeStasxRg$KPNxA3ctVCp0SYSwnT2j6spy+syHM4yJgHbQHcDrXDU',
     'user', true)
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------- categories
INSERT INTO categories (id, slug, name, icon, sort_order, is_active)
VALUES
    ('00000000-0000-0000-0000-0000000000c1', 'car-wash',   'Car wash',        'car',       1, true),
    ('00000000-0000-0000-0000-0000000000c2', 'mens-salon', 'Men''s barber',   'scissors',  2, true),
    ('00000000-0000-0000-0000-0000000000c3', 'laundry',    'Laundry',         'shirt',     3, true)
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------- attributes
--
-- This table is the whole point of the catalog model: adding "pet grooming"
-- with its own fields is an INSERT here, not a migration and not an app
-- release. Both clients read these to build their forms.

INSERT INTO category_attributes
    (id, category_id, key, label, data_type, options, required, applies_to, filterable, sort_order)
VALUES
    -- Car wash. vehicle_size is asked at reservation time, because it is a
    -- fact about the customer's car rather than about the service on offer.
    ('00000000-0000-0000-0000-0000000000d1', '00000000-0000-0000-0000-0000000000c1',
     'vehicle_size', 'Vehicle size', 'enum', '["sedan","suv","pickup"]'::jsonb,
     true, 'booking', false, 1),
    ('00000000-0000-0000-0000-0000000000d2', '00000000-0000-0000-0000-0000000000c1',
     'wash_type', 'Wash type', 'enum', '["exterior","interior","full"]'::jsonb,
     false, 'service', true, 2),
    ('00000000-0000-0000-0000-0000000000d3', '00000000-0000-0000-0000-0000000000c1',
     'plate_number', 'Plate number', 'text', NULL,
     false, 'booking', false, 3),
    ('00000000-0000-0000-0000-0000000000d4', '00000000-0000-0000-0000-0000000000c1',
     'indoor_bay', 'Indoor bay', 'bool', NULL,
     false, 'service', true, 4),

    -- Men's barber.
    ('00000000-0000-0000-0000-0000000000d5', '00000000-0000-0000-0000-0000000000c2',
     'hair_type', 'Hair type', 'enum', '["short","medium","long","coily"]'::jsonb,
     false, 'booking', false, 1),
    ('00000000-0000-0000-0000-0000000000d6', '00000000-0000-0000-0000-0000000000c2',
     'specialties', 'Specialties', 'multi_enum', '["fade","beard","kids","dye"]'::jsonb,
     false, 'provider', true, 2),

    -- Laundry.
    ('00000000-0000-0000-0000-0000000000d7', '00000000-0000-0000-0000-0000000000c3',
     'fabric_care', 'Fabric care', 'multi_enum', '["delicate","wool","leather"]'::jsonb,
     false, 'service', true, 1)
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------- provider
--
-- The launch tenant: a car wash that takes reservations only, with no
-- walk-ins, which is why booking_mode is 'instant' — a confirmed slot the
-- moment the customer takes it.
INSERT INTO providers (
    id, slug, name, phone, email, description, city, address, location, timezone,
    license_number, status, booking_mode, min_lead_minutes
)
VALUES (
    '00000000-0000-0000-0000-0000000000b1',
    'shine-wash', 'Shine Car Wash',
    '+251911223344', 'hello@shinewash.et',
    'Two-bay hand wash on Bole Road. Reservations only.',
    'Addis Ababa', 'Bole Road, near Friendship Center',
    ST_SetSRID(ST_MakePoint(38.7869, 8.9950), 4326)::geography,
    'Africa/Addis_Ababa',
    'ET-AA-2024-00817', 'active', 'instant', 30
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO provider_categories (provider_id, category_id)
VALUES ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000c1')
ON CONFLICT DO NOTHING;

INSERT INTO members (provider_id, user_id, role)
VALUES
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000a2', 'owner'),
    ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000a3', 'staff')
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------- services
INSERT INTO services (
    id, provider_id, category_id, name, description,
    price_cents, currency, duration_minutes, buffer_minutes, attributes, is_active
)
VALUES
    ('00000000-0000-0000-0000-0000000000e1', '00000000-0000-0000-0000-0000000000b1',
     '00000000-0000-0000-0000-0000000000c1',
     'Exterior wash', 'Hand wash, wheels and windows.',
     24500, 'ETB', 45, 10, '{"wash_type":"exterior","indoor_bay":true}'::jsonb, true),
    ('00000000-0000-0000-0000-0000000000e2', '00000000-0000-0000-0000-0000000000b1',
     '00000000-0000-0000-0000-0000000000c1',
     'Full valet', 'Exterior wash plus interior vacuum, dashboard and mats.',
     45000, 'ETB', 90, 15, '{"wash_type":"full","indoor_bay":true}'::jsonb, true)
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------- options
--
-- Vehicle size is a required single-select: the price of a wash depends on it.
-- Extras is an unbounded multi-select. One mechanism, both semantics.
INSERT INTO option_groups
    (id, service_id, name, selection_type, is_required, min_select, max_select, sort_order)
VALUES
    ('00000000-0000-0000-0000-0000000000f1', '00000000-0000-0000-0000-0000000000e1',
     'Vehicle size', 'single', true, 1, 1, 1),
    ('00000000-0000-0000-0000-0000000000f2', '00000000-0000-0000-0000-0000000000e1',
     'Extras', 'multi', false, 0, NULL, 2),
    ('00000000-0000-0000-0000-0000000000f3', '00000000-0000-0000-0000-0000000000e2',
     'Vehicle size', 'single', true, 1, 1, 1)
ON CONFLICT (id) DO NOTHING;

INSERT INTO service_options
    (id, group_id, name, price_delta_cents, duration_delta_minutes, is_active, sort_order)
VALUES
    -- Exterior wash → vehicle size
    ('00000000-0000-0000-0000-00000000010a', '00000000-0000-0000-0000-0000000000f1', 'Sedan',   0,    0,  true, 1),
    ('00000000-0000-0000-0000-00000000010b', '00000000-0000-0000-0000-0000000000f1', 'SUV',     5000, 10, true, 2),
    ('00000000-0000-0000-0000-00000000010c', '00000000-0000-0000-0000-0000000000f1', 'Pickup',  8000, 15, true, 3),
    -- Exterior wash → extras
    ('00000000-0000-0000-0000-00000000010d', '00000000-0000-0000-0000-0000000000f2', 'Wax',      3000, 20, true, 1),
    ('00000000-0000-0000-0000-00000000010e', '00000000-0000-0000-0000-0000000000f2', 'Interior', 2500, 15, true, 2),
    -- Full valet → vehicle size
    ('00000000-0000-0000-0000-00000000010f', '00000000-0000-0000-0000-0000000000f3', 'Sedan',   0,    0,  true, 1),
    ('00000000-0000-0000-0000-000000000110', '00000000-0000-0000-0000-0000000000f3', 'SUV',     6000, 15, true, 2)
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------- resources
INSERT INTO resources (id, provider_id, name, user_id, is_active)
VALUES
    ('00000000-0000-0000-0000-000000000201', '00000000-0000-0000-0000-0000000000b1', 'Bay 1', NULL, true),
    ('00000000-0000-0000-0000-000000000202', '00000000-0000-0000-0000-0000000000b1', 'Bay 2',
     '00000000-0000-0000-0000-0000000000a3', true)
ON CONFLICT (id) DO NOTHING;

-- Both bays can do both services, so a slot is offered while either is free.
INSERT INTO resource_services (resource_id, service_id)
VALUES
    ('00000000-0000-0000-0000-000000000201', '00000000-0000-0000-0000-0000000000e1'),
    ('00000000-0000-0000-0000-000000000201', '00000000-0000-0000-0000-0000000000e2'),
    ('00000000-0000-0000-0000-000000000202', '00000000-0000-0000-0000-0000000000e1'),
    ('00000000-0000-0000-0000-000000000202', '00000000-0000-0000-0000-0000000000e2')
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------- hours
--
-- Provider-wide default: Monday to Saturday, 08:00–18:00 local. No row for
-- Sunday (weekday 0) means closed. Resource-specific rows would override these.
INSERT INTO business_hours (provider_id, resource_id, weekday, opens_at, closes_at)
SELECT '00000000-0000-0000-0000-0000000000b1', NULL, weekday, TIME '08:00', TIME '18:00'
FROM generate_series(1, 6) AS weekday
ON CONFLICT DO NOTHING;

-- A worked example of the override: closed on the next New Year's Day.
INSERT INTO schedule_exceptions (provider_id, resource_id, date, is_closed, reason)
VALUES ('00000000-0000-0000-0000-0000000000b1', NULL, DATE '2027-01-01', true, 'Public holiday')
ON CONFLICT DO NOTHING;

COMMIT;

\echo ''
\echo 'Seeded. Every account uses the password: password123'
\echo '  admin@nearby.et      platform admin'
\echo '  owner@shinewash.et   owner of Shine Car Wash'
\echo '  staff@shinewash.et   staff at Shine Car Wash'
\echo '  customer@example.et  a plain customer'
\echo ''
\echo 'Provider 00000000-0000-0000-0000-0000000000b1 (shine-wash)'
\echo 'Service  00000000-0000-0000-0000-0000000000e1 (Exterior wash, 45m +10m buffer)'
\echo ''
