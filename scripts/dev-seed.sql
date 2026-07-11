-- scripts/dev-seed.sql
--
-- Development seed: entity, user, institution mapping, and checking account.
-- Run after: just migrate
-- Idempotent: safe to re-run after volume wipes.
--
-- DEPENDENCY: The app-side user record is created by the API on first login.
-- If starting from a clean volume, start veloci-auth and veloci-api, log in
-- once as admin@veloci.local, then run this seed. The entity and entity_users
-- rows below will be skipped (ON CONFLICT) if the API already created them.
--
-- Usage:
--   psql -h localhost -U postgres -d veloci_app -f scripts/dev-seed.sql
--   or: just dev-seed

BEGIN;

-- ── Entity ────────────────────────────────────────────────────────────────────
INSERT INTO entities (id, name)
VALUES ('0526338b-977d-4934-a273-b444d014e0b9', 'Test Family')
ON CONFLICT (id) DO NOTHING;

-- ── App-side user (auth_credential_id matches what veloci-auth seeds) ─────────
-- If the API has already created this user via the login flow, this is a no-op.
-- On a fresh volume, the auth service seeds admin@veloci.local with a new
-- credential UUID — update auth_credential_id here to match after first login.
INSERT INTO users (id, auth_credential_id, email, name)
VALUES (
    '620c635f-ae4c-435f-966f-61ab18c7fffc',
    'de2e0e70-436a-4a2c-a278-131693fa08a0',
    'admin@veloci.local',
    'Server Admin'
)
ON CONFLICT (email) DO NOTHING;

-- ── Entity membership ─────────────────────────────────────────────────────────
INSERT INTO entity_users (user_id, entity_id, entity_role)
VALUES (
    (SELECT id FROM users WHERE email = 'admin@veloci.local'),
    '0526338b-977d-4934-a273-b444d014e0b9',
    'entity_admin'
)
ON CONFLICT (user_id, entity_id) DO NOTHING;

-- ── Institution mapping for test CSV (Date / Description / Amount / Balance) ──
INSERT INTO institution_mappings (
    id, entity_id, institution_name, source_type,
    settlement_window_days, dedup_window_days, amount_tolerance_pct,
    date_col, amount_col, merchant_col, balance_col,
    amount_sign_convention
) VALUES (
    'a1a1a1a1-0000-0000-0000-000000000001',
    '0526338b-977d-4934-a273-b444d014e0b9',
    'Test Bank CSV',
    'csv',
    3,
    5,
    0.005,
    'Date',
    'Amount',
    'Description',
    'Balance',
    'positive_is_credit'
)
ON CONFLICT (entity_id, institution_name) DO NOTHING;

-- ── Checking account ──────────────────────────────────────────────────────────
INSERT INTO accounts (
    id, entity_id, institution_id, name, account_type, status
) VALUES (
    'b2b2b2b2-0000-0000-0000-000000000001',
    '0526338b-977d-4934-a273-b444d014e0b9',
    (SELECT id FROM institution_mappings
     WHERE entity_id  = '0526338b-977d-4934-a273-b444d014e0b9'
       AND institution_name = 'Test Bank CSV'),
    'Test Checking',
    'checking',
    'active'
)
ON CONFLICT (entity_id, name) DO NOTHING;

COMMIT;

-- Print the IDs needed for enqueue-import
SELECT
    '0526338b-977d-4934-a273-b444d014e0b9' AS entity_id,
    id                                      AS account_id
FROM accounts
WHERE entity_id = '0526338b-977d-4934-a273-b444d014e0b9'
  AND name = 'Test Checking';
