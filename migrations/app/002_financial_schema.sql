-- migrations/app/002_financial_schema.sql
-- Full financial data model. All tables carry entity_id for v2 RLS upgrade:
--   ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;
--   CREATE POLICY entity_isolation ON <t> USING (entity_id = current_setting('app.current_entity_id')::uuid);

-- ── INSTITUTION MAPPINGS ────────────────────────────────────────────────────
-- CSV column config per bank/institution. Used by Stage 0 normalization.

CREATE TABLE institution_mappings (
  id                     UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID         NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  institution_name       TEXT         NOT NULL,
  source_type            TEXT         NOT NULL DEFAULT 'csv'
                         CHECK (source_type IN ('csv', 'integration')),
  -- days before import timestamp a transaction is considered settled (authoritative)
  settlement_window_days INTEGER      NOT NULL DEFAULT 14,
  -- date tolerance when matching the same transaction across overlapping imports
  dedup_window_days      INTEGER      NOT NULL DEFAULT 3,
  -- amount tolerance for fuzzy matching (handles FX rounding across imports)
  -- csv default: 0.5%; integration default: 2%
  amount_tolerance_pct   NUMERIC(5,4) NOT NULL DEFAULT 0.005,
  date_col               TEXT         NOT NULL,
  amount_col             TEXT         NOT NULL,
  merchant_col           TEXT         NOT NULL,
  imported_id_col        TEXT,
  balance_col            TEXT,
  debit_credit_col       TEXT,
  amount_sign_convention TEXT         NOT NULL
                         CHECK (amount_sign_convention IN ('positive_is_credit', 'positive_is_debit')),
  UNIQUE (entity_id, institution_name)
);

-- ── ACCOUNTS ────────────────────────────────────────────────────────────────

CREATE TABLE accounts (
  id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id          UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  institution_id     UUID        REFERENCES institution_mappings(id),
  name               TEXT        NOT NULL,
  account_type       TEXT        NOT NULL
                     CHECK (account_type IN ('checking', 'savings', 'credit', 'loan', 'mortgage', 'investment')),
  status             TEXT        NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active', 'passive')),
  interest_rate      NUMERIC(8,4),
  balance_cents      BIGINT,
  credit_limit_cents BIGINT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (entity_id, name)
);

-- ── PROCESSING JOBS ─────────────────────────────────────────────────────────
-- Audit log for every job dispatched. Partial unique index prevents the
-- check-then-act race: only one queued/processing job per (entity, type) at a time.

CREATE TABLE processing_jobs (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id    UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  job_type     TEXT        NOT NULL
               CHECK (job_type IN ('import.process', 'rules.reprocess', 'account.analyze')),
  triggered_by UUID        NOT NULL REFERENCES users(id),
  status       TEXT        NOT NULL DEFAULT 'queued'
               CHECK (status IN ('queued', 'processing', 'complete', 'failed')),
  queued_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at   TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  error        TEXT,
  metadata     JSONB
);

CREATE INDEX ON processing_jobs (entity_id, status);
-- enforces at most one active job per (entity, type): the application's
-- "check for existing job" logic becomes a conflict, not a race
CREATE UNIQUE INDEX processing_jobs_one_active
  ON processing_jobs (entity_id, job_type)
  WHERE status IN ('queued', 'processing');

-- ── PENDING IMPORTS ─────────────────────────────────────────────────────────
-- Staging area for uploaded CSVs. Retained after processing for audit.

CREATE TABLE pending_imports (
  id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id         UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  account_id        UUID        NOT NULL REFERENCES accounts(id),
  institution_id    UUID        REFERENCES institution_mappings(id),
  uploaded_by       UUID        NOT NULL REFERENCES users(id),
  uploaded_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  csv_bytes         BYTEA       NOT NULL,
  date_range_start  DATE        NOT NULL,
  date_range_end    DATE        NOT NULL,
  row_count         INTEGER,
  status            TEXT        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'processing', 'complete', 'failed')),
  job_id            UUID        REFERENCES processing_jobs(id),
  error             TEXT
);

-- ── IMPORT BATCHES ──────────────────────────────────────────────────────────
-- One record per completed import.process run with dedup counts.

CREATE TABLE import_batches (
  id                             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  pending_import_id              UUID        NOT NULL REFERENCES pending_imports(id),
  entity_id                      UUID        NOT NULL REFERENCES entities(id),
  account_id                     UUID        NOT NULL REFERENCES accounts(id),
  processed_at                   TIMESTAMPTZ NOT NULL,
  date_range_start               DATE        NOT NULL,
  date_range_end                 DATE        NOT NULL,
  transactions_imported          INTEGER     NOT NULL DEFAULT 0,
  transactions_skipped_duplicate INTEGER     NOT NULL DEFAULT 0,
  transactions_superseded        INTEGER     NOT NULL DEFAULT 0
);

-- ── RAW TRANSACTIONS ────────────────────────────────────────────────────────
-- Source of truth for all financial calculations.
-- Financial columns (date, amount_cents, imported_payee, merchant_normalized,
-- imported_id) are immutable — never modified after insert.
-- settlement_status is set once at insert time and never changed.
-- Flux rows may be deleted and replaced during supersession (Stage 0 dedup);
-- settled rows are never deleted.
-- positive amount_cents = inflow (income, credit); negative = outflow (expense, debit)

CREATE TABLE raw_transactions (
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id           UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  account_id          UUID        NOT NULL REFERENCES accounts(id),
  import_batch_id     UUID        NOT NULL REFERENCES import_batches(id),
  date                DATE        NOT NULL,
  amount_cents        BIGINT      NOT NULL,
  imported_payee      TEXT        NOT NULL,
  merchant_normalized TEXT        NOT NULL,
  imported_id         TEXT,
  -- set at insert time: 'settled' if date < uploaded_at - settlement_window_days,
  -- 'flux' otherwise. never changed after insert.
  -- effective settlement status is derived lazily at query time:
  --   flux rows where NOW() - imported_at > settlement_window_days are
  --   treated as effectively settled without any row mutation.
  settlement_status   TEXT        NOT NULL DEFAULT 'flux'
                      CHECK (settlement_status IN ('flux', 'settled')),
  -- wall-clock insert time; used by Stage 0 to compute effective settlement
  -- status at query time. intentional exception to the engine determinism
  -- invariant — this is an import audit field, not a financial calculation input.
  imported_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON raw_transactions (entity_id, account_id, date);
CREATE INDEX ON raw_transactions (entity_id, date);
CREATE INDEX ON raw_transactions (entity_id, account_id, settlement_status, imported_at);

-- ── RULES ───────────────────────────────────────────────────────────────────
-- Match configuration. User-facing term is "Entry"; internal term is "rule".
-- conditions JSONB must be validated by veloci-api on write (tree shape enforced
-- at application layer; engine errors at the rule level on malformed trees).

CREATE TABLE rules (
  id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  name                   TEXT        NOT NULL,
  direction              TEXT        NOT NULL CHECK (direction IN ('income', 'expense')),
  entry_type             TEXT        NOT NULL
                         CHECK (entry_type IN ('standing', 'single', 'hit', 'boost', 'variable')),
  smoothing_window_days  INTEGER     NOT NULL DEFAULT 30,
  variable_method        TEXT        CHECK (variable_method IN ('avg', 'max')),
  projected_rate_per_day NUMERIC(12,4),
  conditions             JSONB       NOT NULL,
  stage                  TEXT        NOT NULL DEFAULT 'pre' CHECK (stage IN ('pre', 'post')),
  priority               INTEGER     NOT NULL DEFAULT 100,
  status                 TEXT        NOT NULL DEFAULT 'pending_review'
                         CHECK (status IN ('pending_review', 'active', 'inactive')),
  source                 TEXT        NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'engine')),
  created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON rules (entity_id, status);
CREATE INDEX ON rules (entity_id, stage, priority);

-- ── TRANSACTION RULE ASSIGNMENTS ────────────────────────────────────────────
-- Many-to-many. A transaction may match multiple rules (DAG set-union handles
-- dedup at the label level). confidence = 1.0 for user rules, 0.0–1.0 for engine.

CREATE TABLE transaction_rule_assignments (
  transaction_id UUID           NOT NULL REFERENCES raw_transactions(id) ON DELETE CASCADE,
  rule_id        UUID           NOT NULL REFERENCES rules(id) ON DELETE CASCADE,
  confidence     NUMERIC(4,3)   NOT NULL DEFAULT 1.0,
  PRIMARY KEY (transaction_id, rule_id)
);

CREATE INDEX ON transaction_rule_assignments (rule_id);

-- ── LABELS ──────────────────────────────────────────────────────────────────
-- DAG aggregation nodes. May contain rules or other labels as members.

CREATE TABLE labels (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id  UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  name       TEXT        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (entity_id, name)
);

-- ── LABEL MEMBERS ───────────────────────────────────────────────────────────
-- DAG edges. member_type distinguishes rule leaves from label sub-nodes.
-- member_id is a soft reference (no FK) because it points to either rules or
-- labels depending on member_type. Application layer must cascade deletes when
-- a rule or label is removed.

CREATE TABLE label_members (
  label_id    UUID NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  member_id   UUID NOT NULL,
  member_type TEXT NOT NULL CHECK (member_type IN ('rule', 'label')),
  PRIMARY KEY (label_id, member_id)
);

-- ── LABEL RULES ─────────────────────────────────────────────────────────────
-- Automated conditions for applying a label to matching rules.
-- Distinct from /rules (entry matching); these are label automation conditions.

CREATE TABLE label_rules (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  label_id   UUID        NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  entity_id  UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  conditions JSONB       NOT NULL,
  priority   INTEGER     NOT NULL DEFAULT 100,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── REVIEW QUEUE ────────────────────────────────────────────────────────────
-- Engine-detected candidate rules awaiting user approval.
-- suggested_conditions is transparent and editable before the user approves.

CREATE TABLE review_queue (
  id                        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id                 UUID         NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  rule_id                   UUID         NOT NULL REFERENCES rules(id) ON DELETE CASCADE,
  job_id                    UUID         NOT NULL REFERENCES processing_jobs(id),
  suggested_name            TEXT         NOT NULL,
  suggested_entry_type      TEXT         NOT NULL,
  suggested_conditions      JSONB        NOT NULL,
  suggested_rate_per_day    NUMERIC(12,4) NOT NULL,
  matched_transaction_count INTEGER      NOT NULL,
  confidence                NUMERIC(4,3) NOT NULL,
  sample_merchants          TEXT[]       NOT NULL,
  status                    TEXT         NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'approved', 'rejected', 'modified')),
  reviewed_by               UUID         REFERENCES users(id),
  reviewed_at               TIMESTAMPTZ
);

CREATE INDEX ON review_queue (entity_id, status);

-- ── COMPUTED SNAPSHOTS ──────────────────────────────────────────────────────
-- Rebuildable engine output. Safe to truncate and recompute at any time.
-- computed_as_of = MAX(raw_transactions.date) for this entity — never NOW().
-- Prior snapshots are retained to form the historical series for Stage 5 regression.

CREATE TABLE computed_snapshots (
  id                     UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  node_id                UUID          NOT NULL,
  node_type              TEXT          NOT NULL CHECK (node_type IN ('rule', 'label')),
  computed_as_of         DATE          NOT NULL,
  job_id                 UUID          NOT NULL REFERENCES processing_jobs(id),
  actual_rate_per_day    NUMERIC(12,4) NOT NULL,
  projected_rate_per_day NUMERIC(12,4) NOT NULL,
  drift_per_day          NUMERIC(12,4) NOT NULL,
  slope_per_day          NUMERIC(14,6) NOT NULL,
  r_squared              NUMERIC(4,3)  NOT NULL,
  transaction_count      INTEGER       NOT NULL,
  window_days_used       INTEGER       NOT NULL,

  UNIQUE (entity_id, node_id, computed_as_of)
);

CREATE INDEX ON computed_snapshots (entity_id, node_id, computed_as_of DESC);
