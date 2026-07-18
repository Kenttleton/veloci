-- migrations/app/002_financial_schema.sql
-- Full financial data model. Operational tables carry entity_id for v2 RLS upgrade:
--   ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;
--   CREATE POLICY entity_isolation ON <t> USING (entity_id = current_setting('app.current_entity_id')::uuid);
-- Reference/taxonomy tables (labels) are global — no entity_id.

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
  amount_tolerance_pct   FLOAT8       NOT NULL DEFAULT 0.005,
  date_col               TEXT         NOT NULL,
  amount_col             TEXT         NOT NULL,
  merchant_col           TEXT         NOT NULL,
  imported_id_col        TEXT,
  balance_col            TEXT,
  debit_credit_col       TEXT,
  amount_sign_convention TEXT         NOT NULL
                         CHECK (amount_sign_convention IN ('positive_is_credit', 'positive_is_debit')),
  created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
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
  interest_rate           NUMERIC(8,4),
  -- User-provided anchor; balance_cents is always computed = starting_balance_cents + SUM(transactions)
  starting_balance_cents  BIGINT       NOT NULL DEFAULT 0,
  balance_cents           BIGINT,
  credit_limit_cents      BIGINT,
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
               CHECK (job_type IN ('import.process', 'entries.reprocess', 'account.analyze', 'balance.project')),
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

CREATE OR REPLACE FUNCTION notify_job_status_change() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify(
        'job:' || NEW.entity_id::text,
        json_build_object(
            'job_id',       NEW.id::text,
            'job_type',     NEW.job_type,
            'status',       NEW.status,
            'error',        NEW.error,
            'queued_at',    to_char(NEW.queued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
            'completed_at', CASE WHEN NEW.completed_at IS NULL THEN NULL
                                 ELSE to_char(NEW.completed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
                            END
        )::text
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER processing_jobs_notify
AFTER UPDATE ON processing_jobs
FOR EACH ROW EXECUTE FUNCTION notify_job_status_change();

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

-- ── TRANSACTIONS ─────────────────────────────────────────────────────────────
-- Source of truth for all financial calculations.
-- Financial columns (date, amount_cents, imported_payee, merchant_normalized,
-- imported_id) are immutable — never modified after insert.
-- settlement_status is set once at insert time and never changed.
-- Flux rows may be deleted and replaced during supersession (Stage 0 dedup);
-- settled rows are never deleted.
-- positive amount_cents = inflow (income, credit); negative = outflow (expense, debit)

CREATE TABLE transactions (
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

CREATE INDEX ON transactions (entity_id, account_id, date);
CREATE INDEX ON transactions (entity_id, date);
CREATE INDEX ON transactions (entity_id, account_id, settlement_status, imported_at);

-- ── LABELS ──────────────────────────────────────────────────────────────────
-- Global name registry. Used by entries (canonical merchant/signal name) and
-- classifications (output label). Entity-scoping is on operational tables;
-- labels are pure display names referenced by ID throughout the engine.
-- Renaming a label requires no recalculation — only a UI refresh.

CREATE TABLE labels (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (name)
);

-- ── ENTRIES ──────────────────────────────────────────────────────────────────
-- One row per continuous rate signal instance (absorbs rules + rule_epochs).
-- start_date = when this signal instance began (first matching transaction date).
-- end_date = when this instance closed (NULL = currently active). All closures
-- are user-initiated: engine detects a miss → review_queue → user decides.
-- Many entries may share one label (e.g. Netflix v1 at $15.99 closed, Netflix v2
-- at $18.99 active — both reference labels.id for "Netflix").
-- conditions JSONB is nullable: user-created entries may skip auto-matching.

CREATE TABLE entries (
  id                     UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  label_id               UUID          REFERENCES labels(id) ON DELETE SET NULL,
  direction              TEXT          NOT NULL CHECK (direction IN ('income', 'expense')),
  entry_type             TEXT          NOT NULL
                         CHECK (entry_type IN ('standing', 'variable', 'irregular')),
  period_days            INTEGER       NOT NULL DEFAULT 30,
  variable_method        TEXT          CHECK (variable_method IN ('avg', 'max')),
  projected_rate_per_day NUMERIC(12,4),
  conditions             JSONB,
  priority               INTEGER       NOT NULL DEFAULT 100,
  status                 TEXT          NOT NULL DEFAULT 'pending_review'
                         CHECK (status IN ('pending_review', 'active', 'inactive')),
  source                 TEXT          NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'engine')),
  recurrence_anchor      TEXT,
  next_due_date          DATE,
  -- TRUE = include in Stage 7 projection before user approval.
  -- Stage 2 sets this when a pending_review entry has next_due_date + recurrence_anchor.
  -- Cleared on rejection; superseded by active status on approval.
  project_tentatively    BOOLEAN       NOT NULL DEFAULT FALSE,
  -- Forward versioning: user-known future price change. veloci-api applies
  -- pending_amount_cents automatically when computed_as_of >= pending_effective_date,
  -- then clears both fields. Engine reads projected_rate_per_day after API applies.
  pending_amount_cents   BIGINT,
  pending_effective_date DATE,
  -- Signal lifecycle (absorbed from rule_epochs).
  start_date             DATE          NOT NULL,
  end_date               DATE,
  created_at             TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  -- Engine review metadata (populated by Stage 2; NULL on user-created entries).
  -- alert_type: 'new' = first detection, 'drift' = rate changed, 'ended' = signal gone.
  alert_type                TEXT          CHECK (alert_type IN ('new', 'drift', 'ended')),
  confidence                NUMERIC(4,3),
  merchant_confidence       NUMERIC(4,3),
  timing_confidence         NUMERIC(4,3),
  amount_confidence         NUMERIC(4,3),
  sample_merchants          TEXT[],
  matched_transaction_count INTEGER,
  reviewed_by               UUID          REFERENCES users(id),
  reviewed_at               TIMESTAMPTZ
);

CREATE INDEX ON entries (entity_id, status);
CREATE INDEX ON entries (entity_id, priority);
CREATE INDEX ON entries (entity_id, next_due_date);
CREATE INDEX ON entries (entity_id, label_id);

-- ── CLASSIFICATIONS ──────────────────────────────────────────────────────────
-- Post-stage rules: apply labels to entries based on entry attributes and
-- existing label assignments. Entirely user-defined. Do not affect rate
-- calculations — display/grouping only.
-- Composability: conditions reference label UUIDs, enabling aggregate labels
-- built from leaf labels without a separate membership table.
-- Cycle detection (label A → classification → label B → classification → A)
-- enforced by veloci-api at write time.

CREATE TABLE classifications (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id  UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  label_id   UUID        NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  conditions JSONB       NOT NULL,
  priority   INTEGER     NOT NULL DEFAULT 100,
  status     TEXT        NOT NULL DEFAULT 'active'
             CHECK (status IN ('active', 'inactive')),
  source     TEXT        NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'engine')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON classifications (entity_id, status);
CREATE INDEX ON classifications (entity_id, label_id);

-- ── TRANSACTION ENTRY ASSIGNMENTS ────────────────────────────────────────────
-- Many-to-many. A transaction may match multiple entries.
-- confidence = 1.0 for user entries, 0.0–1.0 for engine-generated entries.

CREATE TABLE transaction_entry_assignments (
  transaction_id UUID         NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
  entry_id       UUID         NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  confidence     NUMERIC(4,3) NOT NULL DEFAULT 1.0,
  PRIMARY KEY (transaction_id, entry_id)
);

CREATE INDEX ON transaction_entry_assignments (entry_id);


-- ── SNAPSHOTS ────────────────────────────────────────────────────────────────
-- Rebuildable engine output. Safe to truncate and recompute at any time.
-- One row per calendar day per node. The engine crawls the flux window on each
-- import and UPSERTs all days in [computed_as_of - settlement_window_days .. computed_as_of].
-- Days outside the flux window have only settled transactions and are not recomputed.
--
-- node_type = 'entry'          → entry-level rate signal (Stage 3 output)
-- node_type = 'classification' → classification-level aggregate (Stage 4 output)
--
-- snapshot_date    = the calendar day this row represents (identity key).
-- computed_as_of   = MAX(transactions.date) from the import run that wrote this row.
--                    Used by Stage 3 signal expiry and Stage 7 as the projection anchor.
--                    Separate from snapshot_date: an import on Mar 15 covering data through
--                    Mar 10 produces snapshot rows for dates in the flux window, each with
--                    computed_as_of = Mar 10.
--
-- OHLC candlestick high/low are NOT stored. The API computes MAX/MIN(actual_rate_per_day)
-- over the daily series at query time. Snapshots are fetched in date-range chunks to
-- support lazy-loading the Horizon graph.

CREATE TABLE snapshots (
  id                     UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  node_id                UUID          NOT NULL,
  node_type              TEXT          NOT NULL CHECK (node_type IN ('entry', 'classification')),
  snapshot_date          DATE          NOT NULL,
  computed_as_of         DATE          NOT NULL,
  job_id                 UUID          NOT NULL REFERENCES processing_jobs(id),
  actual_rate_per_day    NUMERIC(12,4) NOT NULL,
  projected_rate_per_day NUMERIC(12,4) NOT NULL,
  drift_per_day          NUMERIC(12,4) NOT NULL,
  slope_per_day          NUMERIC(14,6) NOT NULL,
  r_squared              NUMERIC(4,3)  NOT NULL,
  transaction_count      INTEGER       NOT NULL,
  window_days_used       INTEGER       NOT NULL,
  -- SUM(matched amount_cents) for the snapshot_date - period_days window.
  -- Basis for actual_rate_per_day. For variable entries, also feeds projected_rate_per_day
  -- via variable_method over the 3*period_days projection lookback window.
  rolling_window_total_cents BIGINT    NOT NULL DEFAULT 0,
  -- running balance at this snapshot date; secondary to rates, used for bank account comparison.
  balance_cents          BIGINT        NOT NULL DEFAULT 0,

  UNIQUE (entity_id, node_id, snapshot_date)
);

CREATE INDEX ON snapshots (entity_id, node_id, snapshot_date DESC);

-- ── PROJECTIONS ───────────────────────────────────────────────────────────────
-- Forward-looking signal superposition timeline produced by Stage 7.
-- One row per (account, projected date) per job run. Safe to truncate and
-- recompute — derived entirely from active entries + their recurrence schedules.
--
-- account_id NULL = entity-level aggregate across all active accounts.
-- Rates are the primary output; projected_balance_cents is derived (running
-- integral of margin_rate_per_day) for bank account comparison only.
-- is_pinch_point = margin_rate_per_day < 0 (commitment signals exceed income
-- signals at this phase offset).

CREATE TABLE projections (
  id                       UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id                UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  account_id               UUID          REFERENCES accounts(id),
  job_id                   UUID          NOT NULL REFERENCES processing_jobs(id),
  projected_date           DATE          NOT NULL,
  income_rate_per_day      NUMERIC(12,4) NOT NULL DEFAULT 0,
  commitment_rate_per_day  NUMERIC(12,4) NOT NULL DEFAULT 0,
  margin_rate_per_day      NUMERIC(12,4) NOT NULL,
  projected_balance_cents  BIGINT        NOT NULL,
  is_pinch_point           BOOLEAN       NOT NULL DEFAULT FALSE,

  UNIQUE (entity_id, account_id, job_id, projected_date)
);

CREATE INDEX ON projections (entity_id, account_id, projected_date);

GRANT ALL ON ALL TABLES IN SCHEMA public TO veloci_app_user;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO veloci_app_user;
