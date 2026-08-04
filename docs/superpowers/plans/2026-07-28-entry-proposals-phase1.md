# Entry Proposals Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `alert_type`-on-entries review model with a dedicated `entry_proposals` table; entries start `live` immediately on engine detection; proposals are the review queue for `new`, `drift`, and `ended` detections.

**Architecture:** Three-layer change. Schema: add `entry_proposals`, remove `alert_type` and `status` from `entries`; live/ended is derived at query time from `end_date` vs `computed_as_of`. Rust engine: Stage 2 inserts entries (no status column) and creates a `new` proposal; Stage 7 runs a lapse check (`computed_as_of > next_due_date`) per live entry and inserts `ended` proposals for entries that have not received a transaction since their expected due date. Go layer: new `proposals.go` store + handler with type-specific approve/reject logic; old `ApproveEntryReview` / `RejectEntryReview` removed. Template: Review tab queries proposals instead of `status = 'pending'` entries.

**Tech Stack:** Go 1.26, Rust (sqlx), PostgreSQL, Templ

## Global Constraints

- No ALTER TABLE — all schema changes go directly into `migrations/app/002_financial_schema.sql`
- All tests must pass before each commit: `cargo test` in `services/engine`, `go test ./...` in `services/web`
- `entryCols` / `entryFrom` constants in `store/entries.go` must stay consistent — all SELECT queries that read entries must use them
- `entry_proposals` rows are never hard-deleted by application code; the table is cleaned by a periodic job (out of scope — leave rows in place)
- Drift proposals are **out of scope** for this plan — no drift detection, no `drift` proposal_type handling required
- Rejected `new` proposals: hard delete the entry and cascade-delete its `entry_proposals` row (FK `ON DELETE CASCADE`); the entry was engine-detected and never user-approved, so no historical record is warranted
- `sample_merchants` column stays on `entries` in this plan — display field cleanup is deferred.

---

## File Map

| File | Change |
|---|---|
| `migrations/app/002_financial_schema.sql` | Add `entry_proposals` table; remove `alert_type` and `status` from `entries` |
| `services/engine/src/pipeline/stage2.rs` | Change: insert entry (no status column); insert/upsert `entry_proposals` for `new` type; remove `alert_type = 'new'` from INSERT |
| `services/engine/src/pipeline/stage7.rs` | Change: eligible entries query uses temporal `end_date` check; add ended-proposal detection |
| `services/web/store/proposals.go` | New: `EntryProposalRow`, `ListPendingProposals`, `ApproveNewProposal`, `RejectNewProposal`, `ApproveEndedProposal`, `RejectEndedProposal` |
| `services/web/store/entries.go` | Remove: `AlertType` field, `alert_type` from `entryCols`, `ApproveEntryReview`, `RejectEntryReview`; update `CountEntriesByStatus` to count proposals |
| `services/web/handler/proposals.go` | New: `ProposalsHandler`, `ApproveProposal`, `RejectProposal`, route registration |
| `services/web/handler/entries.go` | Remove: `ApproveEntry`, `RejectEntry` handlers and route registrations |
| `services/web/page/handler.go` | Update: Review tab uses `ListPendingProposals`; remove `alertTypeLabel`; update `LedgerData` |
| `services/web/page/ledger.templ` | Update: Review tab renders proposal cards; remove pending-entry review banner |

---

## Task 1: Schema — entry_proposals table + entries cleanup

**Files:**
- Modify: `migrations/app/002_financial_schema.sql`

**Interfaces:**
- Produces: `entry_proposals` table; `entries` without `alert_type` or `status`; live/ended derived from `end_date` vs `computed_as_of`

- [ ] **Step 1: Read the current entries table definition**

Open `migrations/app/002_financial_schema.sql` and find the `entries` table block (around line 190–236). Note the exact column order — we will edit it directly.

- [ ] **Step 2: Remove `alert_type` from `entries`**

In `002_financial_schema.sql`, find and remove these two lines from the `entries` table body:

```sql
  -- alert_type: 'new' = first detection, 'drift' = rate changed, 'ended' = signal gone.
  alert_type                TEXT          CHECK (alert_type IN ('new', 'drift', 'ended')),
```

- [ ] **Step 3: Remove `entries.status` column**

Find and remove these two lines from the `entries` table body:

```sql
  status                 TEXT          NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'live', 'ended')),
```

Live vs ended is now derived at query time from `end_date` relative to `computed_as_of` (the entity's transaction watermark from `MAX(snapshots.computed_as_of)`):

- **Live**: `end_date IS NULL OR end_date >= (SELECT MAX(computed_as_of) FROM snapshots WHERE entity_id = $entity_id)`
- **Ended**: `end_date IS NOT NULL AND end_date < (SELECT MAX(computed_as_of) FROM snapshots WHERE entity_id = $entity_id)`

In Rust engine code, `computed_as_of` is already a parameter in scope — use it directly. In Go queries, use the inline subquery above.

- [ ] **Step 4: Add `entry_proposals` table**

Add the following block after the `entries` table definition (after the `CREATE INDEX ON entries` line):

```sql
-- entry_proposals holds the engine's review queue.
-- A proposal is created when the engine detects a new pattern ('new') or
-- a missed expected transaction ('ended'). Proposals link to a live entry
-- via reference_entry_id. Users approve or dismiss each proposal; the
-- outcome updates the referenced entry.
--
-- Re-trigger thresholds:
--   new/ended dismissed: re-propose when dismissed_transaction_count < current
--     matched_transaction_count on the live entry (new), or when
--     dismissed_computed_as_of < current computed_as_of (ended).
CREATE TABLE entry_proposals (
  id                          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id                   UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  proposal_type               TEXT        NOT NULL CHECK (proposal_type IN ('new', 'ended')),
  status                      TEXT        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'dismissed', 'accepted')),
  reference_entry_id          UUID        NOT NULL REFERENCES entries(id) ON DELETE CASCADE,

  -- ended proposal only
  proposed_end_date           DATE,

  -- re-trigger thresholds (set on dismissal)
  dismissed_transaction_count INTEGER,    -- new: re-propose when live entry matched_transaction_count > this
  dismissed_computed_as_of    DATE,       -- ended: re-propose when computed_as_of advances past this

  -- audit
  reviewed_by                 UUID        REFERENCES users(id),
  reviewed_at                 TIMESTAMPTZ,
  created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON entry_proposals (entity_id, status);
CREATE INDEX ON entry_proposals (reference_entry_id, proposal_type, status);
```

- [ ] **Step 5: Commit**

```bash
git add migrations/app/002_financial_schema.sql
git commit -m "feat: add entry_proposals table; remove alert_type and status from entries"
```

---

## Task 2: Rust stage2 — live entries + new proposals

**Files:**
- Modify: `services/engine/src/pipeline/stage2.rs`

**Interfaces:**
- Consumes: `entry_proposals` table (new from Task 1)
- Produces: entries inserted as `status = 'live'`; `entry_proposals (type='new', status='pending')` inserted on first detection; existing proposals updated (entry metadata re-scored) on re-run

**Note on upsert logic change:** Currently stage2 looks for `status = 'pending'` entries to update. With `pending` removed, the upsert logic becomes: find an existing engine-created `live` entry for this label; find an existing `pending` `new` proposal for it. If a pending proposal exists, update the entry's score data. If no engine entry exists, insert both. If an engine entry exists but its proposal was already accepted/dismissed, do nothing (skip re-classification of an approved entry).

- [ ] **Step 1: Write the failing test for live-entry insertion**

In `services/engine/src/pipeline/stage2.rs`, find the test module. Locate the test `score_regular_transactions_standing` (or similar). The SQL path changes but the scoring logic is unchanged — the test for the INSERT/upsert behavior is an integration test that can't run without a DB. Read the existing tests to understand the coverage pattern and proceed to the implementation step.

- [ ] **Step 2: Update the INSERT SQL to remove `alert_type` and `status`**

Find the INSERT block (around line 741–755). Change:

```sql
-- Before
"INSERT INTO entries (
   entity_id, label_id, direction, entry_type, period_days, next_due_date,
   recurrence_anchor, conditions, projected_rate_per_day,
   status, source, start_date, rate_method,
   alert_type, fitness, merchant_fit, timing_fit, amount_fit,
   sample_merchants, matched_transaction_count
 ) VALUES (
   $1, $2, $3, $4, $5, $6,
   $7, $8, $9,
   'pending', 'engine', $10, 'median',
   'new', $11, $12, $13, $14,
   $15, $16
 )
 RETURNING id"
```

To:

```sql
-- After
"INSERT INTO entries (
   entity_id, label_id, direction, entry_type, period_days, next_due_date,
   recurrence_anchor, conditions, projected_rate_per_day,
   source, start_date, rate_method,
   fitness, merchant_fit, timing_fit, amount_fit,
   sample_merchants, matched_transaction_count
 ) VALUES (
   $1, $2, $3, $4, $5, $6,
   $7, $8, $9,
   'engine', $10, 'median',
   $11, $12, $13, $14,
   $15, $16
 )
 RETURNING id"
```

The bind order is unchanged (`$11`–`$16` still map to fitness/merchant_fit/timing_fit/amount_fit/sample_merchants/matched_transaction_count).

- [ ] **Step 3: Update the UPDATE SQL to remove `alert_type`**

Find the UPDATE block (around line 712–738). The UPDATE fires when an existing engine entry for this label already exists with a pending proposal. The current query selects by `status = 'pending'`. Change the lookup query from:

```rust
"SELECT id FROM entries
 WHERE entity_id = $1 AND label_id = $2 AND source = 'engine' AND status = 'pending'",
```

To (look for live engine entry with an outstanding new proposal):

```rust
"SELECT e.id FROM entries e
 JOIN entry_proposals ep ON ep.reference_entry_id = e.id
 WHERE e.entity_id = $1 AND e.label_id = $2 AND e.source = 'engine'
   AND e.end_date IS NULL
   AND ep.proposal_type = 'new' AND ep.status = 'pending'
 LIMIT 1",
```

The UPDATE query itself does not reference `alert_type` — no changes needed there.

- [ ] **Step 4: Insert a new proposal after the entry INSERT**

After the `fetch_one(pool)` that returns the new entry ID (after the INSERT block), add:

```rust
sqlx::query(
    "INSERT INTO entry_proposals (entity_id, proposal_type, status, reference_entry_id)
     VALUES ($1, 'new', 'pending', $2)",
)
.bind(entity_id)
.bind(id)
.execute(pool)
.await
.context("failed to insert new proposal for entry")?;
```

- [ ] **Step 5: Run engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass. (The schema tests don't run against a live DB; logic tests for scoring are unchanged.)

- [ ] **Step 6: Commit**

```bash
git add services/engine/src/pipeline/stage2.rs
git commit -m "feat: stage2 creates live entries + new proposals; remove alert_type from INSERT"
```

---

## Task 3: Rust stage7 — remove pending from eligible entries; add ended proposals

**Files:**
- Modify: `services/engine/src/pipeline/stage7.rs`

**Interfaces:**
- Produces: eligible entries query uses temporal `end_date` check; ended proposals inserted for entries whose `next_due_date + period_days < computed_as_of` and no pending ended proposal exists

- [ ] **Step 1: Update the eligible entries query to use temporal end_date check**

In `stage7.rs`, find `load_eligible_entries` (around line 390–428). Change:

```sql
-- Before
AND status IN ('live', 'pending')
```

```sql
-- After
AND (end_date IS NULL OR end_date >= $computed_as_of)
```

Where `$computed_as_of` is the `computed_as_of: NaiveDate` parameter already in scope for `stage7::run`. This replaces the removed `status` column.

- [ ] **Step 2: Add ended proposal detection after projections are written**

At the end of `stage7::run`, after `write_projections(...)`, add a call to the new function:

```rust
detect_ended_entries(entity_id, computed_as_of, &entries, write_pool).await?;
```

Add the function after `write_projections`:

```rust
/// Lapse check: for each live entry where computed_as_of > next_due_date,
/// the expected transaction has not arrived since the last matched one.
/// Inserts an ended proposal unless one is already pending or was recently dismissed.
///
/// next_due_date is maintained by stage1 as (last_matched_txn_date + period_days).
/// Passing the lapse check means we are past the date the next transaction was due.
async fn detect_ended_entries(
    entity_id: Uuid,
    computed_as_of: NaiveDate,
    entries: &[EligibleEntry],
    pool: &PgPool,
) -> Result<()> {
    for entry in entries {
        let Some(next_due) = entry.next_due_date else { continue };

        // Lapse check: use tolerance window — entry has not lapsed until computed_as_of
        // is more than TIMING_VARIANCE_THRESHOLD_DAYS past next_due.
        if !entry_has_lapsed(next_due, computed_as_of, super::TIMING_VARIANCE_THRESHOLD_DAYS as i64) {
            continue;
        }

        // Skip if a pending ended proposal already exists, or if a dismissed one
        // was recorded at or after computed_as_of (user already dismissed this lapse).
        let already_blocked: Option<(i32,)> = sqlx::query_as(
            "SELECT 1 FROM entry_proposals
             WHERE reference_entry_id = $1
               AND proposal_type = 'ended'
               AND (status = 'pending'
                    OR (status = 'dismissed'
                        AND dismissed_computed_as_of >= $2))",
        )
        .bind(entry.id)
        .bind(computed_as_of)
        .fetch_optional(pool)
        .await
        .context("failed to check existing ended proposal")?;

        if already_blocked.is_some() {
            continue;
        }

        // proposed_end_date = next_due (the date the pattern was expected but didn't arrive).
        sqlx::query(
            "INSERT INTO entry_proposals
               (entity_id, proposal_type, status, reference_entry_id, proposed_end_date)
             VALUES ($1, 'ended', 'pending', $2, $3)",
        )
        .bind(entity_id)
        .bind(entry.id)
        .bind(next_due)
        .execute(pool)
        .await
        .context("failed to insert ended proposal")?;
    }
    Ok(())
}
```

- [ ] **Step 4: Run engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add services/engine/src/pipeline/stage7.rs
git commit -m "feat: stage7 ended proposal detection; remove pending from eligible entries"
```

---

## Task 4: Go store — proposals.go

**Files:**
- Create: `services/web/store/proposals.go`

**Interfaces:**
- Produces:
  - `EntryProposalRow` struct with all `entry_proposals` columns
  - `func (s *Store) ListPendingProposals(ctx, entityID string) ([]EntryProposalRow, error)`
  - `func (s *Store) ApproveNewProposal(ctx, entityID, proposalID, userID string) error`
  - `func (s *Store) RejectNewProposal(ctx, entityID, proposalID, userID string) error`
  - `func (s *Store) ApproveEndedProposal(ctx, entityID, proposalID, userID string) error`
  - `func (s *Store) RejectEndedProposal(ctx, entityID, proposalID, userID string) error`

- [ ] **Step 1: Write the test**

Create `services/web/store/proposals_test.go`:

```go
package store

import (
    "testing"
)

func TestProposalTypeConstants(t *testing.T) {
    // Verify the proposal type constants used throughout the store are correct strings.
    if proposalTypeNew != "new" {
        t.Errorf("expected 'new', got %q", proposalTypeNew)
    }
    if proposalTypeEnded != "ended" {
        t.Errorf("expected 'ended', got %q", proposalTypeEnded)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd services/web && go test ./store/... -run TestProposalTypeConstants -v
```

Expected: FAIL — `proposalTypeNew` undefined

- [ ] **Step 3: Create `services/web/store/proposals.go`**

```go
package store

import (
    "context"
    "time"

    "github.com/jackc/pgx/v5"
)

const (
    proposalTypeNew   = "new"
    proposalTypeEnded = "ended"
)

// EntryProposalRow is a full entry_proposals row joined with enough entry data
// to render the review card without a second query.
type EntryProposalRow struct {
    // proposal fields
    ID              string  `db:"id"`
    EntityID        string  `db:"entity_id"`
    ProposalType    string  `db:"proposal_type"`
    Status          string  `db:"status"`
    ReferenceEntryID string `db:"reference_entry_id"`
    ProposedEndDate *time.Time `db:"proposed_end_date"`
    CreatedAt       time.Time  `db:"created_at"`

    // joined entry fields (for review card display)
    EntryDirection  string   `db:"entry_direction"`
    EntryType       string   `db:"entry_type"`
    EntryLabelName  *string  `db:"entry_label_name"`
    EntryFitness    *float64 `db:"entry_fitness"`
    EntryMerchantFit *float64 `db:"entry_merchant_fit"`
    EntryTimingFit  *float64 `db:"entry_timing_fit"`
    EntryAmountFit  *float64 `db:"entry_amount_fit"`
    EntryMatchedCount *int   `db:"entry_matched_count"`
    EntrySampleMerchants []string `db:"entry_sample_merchants"`
    EntryProjectedRate *float64 `db:"entry_projected_rate"`
    EntryPeriodDays *int     `db:"entry_period_days"`
    EntrySource     string   `db:"entry_source"`
}

// ListPendingProposals returns all pending proposals for an entity,
// joined to their referenced entry's display fields.
func (s *Store) ListPendingProposals(ctx context.Context, entityID string) ([]EntryProposalRow, error) {
    rows, err := s.pool.Query(ctx, `
        SELECT
            ep.id::text, ep.entity_id::text, ep.proposal_type, ep.status,
            ep.reference_entry_id::text, ep.proposed_end_date, ep.created_at,
            e.direction            AS entry_direction,
            e.entry_type           AS entry_type,
            l.name                 AS entry_label_name,
            e.fitness              AS entry_fitness,
            e.merchant_fit         AS entry_merchant_fit,
            e.timing_fit           AS entry_timing_fit,
            e.amount_fit           AS entry_amount_fit,
            e.matched_transaction_count AS entry_matched_count,
            e.sample_merchants     AS entry_sample_merchants,
            e.projected_rate_per_day::float8 AS entry_projected_rate,
            e.period_days          AS entry_period_days,
            e.source               AS entry_source
        FROM entry_proposals ep
        JOIN entries e ON e.id = ep.reference_entry_id
        LEFT JOIN labels l ON l.id = e.label_id
        WHERE ep.entity_id = $1
          AND ep.status = 'pending'
        ORDER BY ep.created_at ASC
    `, entityID)
    if err != nil {
        return nil, err
    }
    return pgx.CollectRows(rows, pgx.RowToStructByName[EntryProposalRow])
}

// ApproveNewProposal accepts a 'new' proposal: marks it accepted and records the reviewer.
// The referenced entry is already live — no changes to entries needed.
func (s *Store) ApproveNewProposal(ctx context.Context, entityID, proposalID, userID string) error {
    _, err := s.pool.Exec(ctx, `
        UPDATE entry_proposals
        SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
        WHERE entity_id = $1 AND id = $2::uuid AND proposal_type = 'new' AND status = 'pending'
    `, entityID, proposalID, userID)
    return err
}

// RejectNewProposal hard-deletes the referenced entry (and its proposal, via FK
// ON DELETE CASCADE). The entry was engine-detected and never user-approved,
// so no historical record is warranted.
func (s *Store) RejectNewProposal(ctx context.Context, entityID, proposalID, userID string) error {
    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx) //nolint:errcheck

    var entryID string
    err = tx.QueryRow(ctx, `
        SELECT reference_entry_id::text
        FROM entry_proposals
        WHERE entity_id = $1 AND id = $2::uuid
          AND proposal_type = 'new' AND status = 'pending'
    `, entityID, proposalID).Scan(&entryID)
    if err != nil {
        return err
    }

    // Hard delete the entry. The proposal row is deleted via FK ON DELETE CASCADE.
    // transaction_entry_assignments for this entry are also cascade-deleted.
    if _, err = tx.Exec(ctx, `
        DELETE FROM entries WHERE entity_id = $1 AND id = $2::uuid
    `, entityID, entryID); err != nil {
        return err
    }

    return tx.Commit(ctx)
}

// ApproveEndedProposal accepts an 'ended' proposal: sets end_date on the referenced
// entry and marks the proposal accepted.
func (s *Store) ApproveEndedProposal(ctx context.Context, entityID, proposalID, userID string) error {
    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx) //nolint:errcheck

    var entryID string
    var proposedEndDate *time.Time
    err = tx.QueryRow(ctx, `
        SELECT reference_entry_id::text, proposed_end_date
        FROM entry_proposals
        WHERE entity_id = $1 AND id = $2::uuid
          AND proposal_type = 'ended' AND status = 'pending'
    `, entityID, proposalID).Scan(&entryID, &proposedEndDate)
    if err != nil {
        return err
    }

    endDate := proposedEndDate
    if endDate == nil {
        now := time.Now().UTC()
        endDate = &now
    }

    if _, err = tx.Exec(ctx, `
        UPDATE entries SET end_date = $2
        WHERE entity_id = $1 AND id = $3::uuid
    `, entityID, *endDate, entryID); err != nil {
        return err
    }

    if _, err = tx.Exec(ctx, `
        UPDATE entry_proposals
        SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
        WHERE entity_id = $1 AND id = $2::uuid
    `, entityID, proposalID, userID); err != nil {
        return err
    }

    return tx.Commit(ctx)
}

// RejectEndedProposal dismisses an 'ended' proposal and records dismissed_computed_as_of
// so the engine does not re-propose until further time has passed.
func (s *Store) RejectEndedProposal(ctx context.Context, entityID, proposalID, userID string, computedAsOf time.Time) error {
    _, err := s.pool.Exec(ctx, `
        UPDATE entry_proposals
        SET status = 'dismissed',
            dismissed_computed_as_of = $3,
            reviewed_by = $4::uuid,
            reviewed_at = NOW()
        WHERE entity_id = $1 AND id = $2::uuid
          AND proposal_type = 'ended' AND status = 'pending'
    `, entityID, proposalID, computedAsOf, userID)
    return err
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd services/web && go test ./store/... -run TestProposalTypeConstants -v
```

Expected: PASS

- [ ] **Step 5: Build**

```bash
cd services/web && go build ./...
```

Expected: clean compile.

- [ ] **Step 6: Commit**

```bash
git add services/web/store/proposals.go services/web/store/proposals_test.go
git commit -m "feat: add proposals store (ListPendingProposals, Approve/Reject new+ended)"
```

---

## Task 5: Go store — remove alert_type from entries.go

**Files:**
- Modify: `services/web/store/entries.go`

**Interfaces:**
- Produces: `EntryRow` without `AlertType` or `Status`; `entryCols` without `e.alert_type` or `e.status`; `ApproveEntryReview` and `RejectEntryReview` removed; `CountEntriesByStatus` uses temporal live/ended definition

- [ ] **Step 1: Remove `AlertType` and `Status` from `EntryRow`**

In `services/web/store/entries.go`, find the `EntryRow` struct. Remove:

```go
AlertType                *string    `db:"alert_type"`
Status                   string     `db:"status"`
```

- [ ] **Step 2: Remove `alert_type` and `status` from `entryCols`**

Find the `entryCols` constant. Remove `e.alert_type,` and `e.status` from the SELECT list. The alert_type line currently reads something like:

```go
e.alert_type, e.fitness, e.merchant_fit, e.timing_fit,
```

Change to:

```go
e.fitness, e.merchant_fit, e.timing_fit,
```

Also find and remove `e.status` from wherever it appears in `entryCols`.

- [ ] **Step 3: Fix the RETURNING clauses in CreateEntry and UpdateEntry**

Both `CreateEntry` and `UpdateEntry` have inline RETURNING queries. Remove `NULL::text AS alert_type` and any `status` reference from each RETURNING block.

`CreateEntry` RETURNING (around line 241):
```go
// Before
NULL::text AS alert_type, NULL::numeric AS fitness,
// After
NULL::numeric AS fitness,
```

Also remove any `e.status` or `'live'::text AS status` from the RETURNING column list.

`UpdateEntry` RETURNING (around line 310): same removals.

- [ ] **Step 4: Remove `ApproveEntryReview` and `RejectEntryReview`**

Delete the two functions entirely (lines ~491–529). They are replaced by the functions in `proposals.go`.

- [ ] **Step 5: Update `CountEntriesByStatus` to use temporal live/ended definition**

`status` is gone from `entries`. Live/ended is derived from `end_date` vs the entity's `computed_as_of` (from `MAX(snapshots.computed_as_of)`). Replace the entire `CountEntriesByStatus` function:

```go
// CountEntriesByStatus returns pill counts for the Ledger filter bar.
// Live/Ended are derived from end_date vs the entity's computed_as_of watermark.
// Pending = pending proposals (the review queue).
func (s *Store) CountEntriesByStatus(ctx context.Context, entityID string) (EntryCounts, error) {
    var c EntryCounts

    err := s.pool.QueryRow(ctx, `
        WITH watermark AS (
            SELECT COALESCE(MAX(computed_as_of), CURRENT_DATE) AS cof
            FROM snapshots WHERE entity_id = $1
        )
        SELECT
            COUNT(*) FILTER (WHERE end_date IS NULL OR end_date >= w.cof)     AS live,
            COUNT(*) FILTER (WHERE end_date IS NOT NULL AND end_date < w.cof) AS ended
        FROM entries, watermark w
        WHERE entity_id = $1 AND source != 'system'
    `, entityID).Scan(&c.Live, &c.Ended)
    if err != nil {
        return EntryCounts{}, err
    }

    if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)::int FROM entry_proposals
        WHERE entity_id = $1 AND status = 'pending'
    `, entityID).Scan(&c.Pending); err != nil {
        return EntryCounts{}, err
    }

    if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)::int FROM entries WHERE entity_id = $1 AND source = 'system'
    `, entityID).Scan(&c.System); err != nil {
        return EntryCounts{}, err
    }
    return c, nil
}
```

- [ ] **Step 6: Build**

```bash
cd services/web && go build ./...
```

Fix any compile errors from removed `AlertType` references (handler/entries.go and page/handler.go will complain — leave them for the next tasks).

- [ ] **Step 7: Commit**

```bash
git add services/web/store/entries.go
git commit -m "feat: remove alert_type and status from EntryRow; temporal live/ended counts; proposals drive pending count"
```

---

## Task 6: Go handler — proposals.go + remove old review endpoints

**Files:**
- Create: `services/web/handler/proposals.go`
- Modify: `services/web/handler/entries.go`

**Interfaces:**
- Consumes: `store.ApproveNewProposal`, `store.RejectNewProposal`, `store.ApproveEndedProposal`, `store.RejectEndedProposal` from Task 4
- Produces: `POST /api/proposals/:id/approve` and `POST /api/proposals/:id/reject`; old `ApproveEntry` and `RejectEntry` removed

- [ ] **Step 1: Create `services/web/handler/proposals.go`**

```go
package handler

import (
    "net/http"
    "time"

    "github.com/labstack/echo/v4"
    "github.com/veloci/veloci/middleware"
    "github.com/veloci/veloci/queue"
    "github.com/veloci/veloci/store"
    "encoding/json"
)

type ProposalsHandler struct {
    s   *store.Store
    pub *queue.Publisher
}

func NewProposalsHandler(s *store.Store, pub *queue.Publisher) *ProposalsHandler {
    return &ProposalsHandler{s: s, pub: pub}
}

func (h *ProposalsHandler) RegisterRoutes(write *echo.Group) {
    write.POST("/proposals/:id/approve", h.ApproveProposal)
    write.POST("/proposals/:id/reject", h.RejectProposal)
}

func (h *ProposalsHandler) ApproveProposal(c echo.Context) error {
    ctx := c.Request().Context()
    entityID := middleware.EntityID(ctx)
    userID := middleware.UserID(ctx)
    id := c.Param("id")

    // Read proposal type without loading full row.
    var proposalType string
    if err := h.s.GetProposalType(ctx, entityID, id, &proposalType); err != nil {
        return echo.NewHTTPError(http.StatusNotFound, "not found")
    }

    var err error
    switch proposalType {
    case "new":
        err = h.s.ApproveNewProposal(ctx, entityID, id, userID)
    case "ended":
        err = h.s.ApproveEndedProposal(ctx, entityID, id, userID)
    default:
        return echo.NewHTTPError(http.StatusBadRequest, "unknown proposal type")
    }
    if err != nil {
        return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
    }

    // Trigger account.analyze after any approval.
    meta, _ := json.Marshal(map[string]string{})
    if job, jobErr := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta); jobErr == nil {
        h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
            JobID:    job.ID,
            Type:     "account.analyze",
            EntityID: entityID,
            Metadata: meta,
        })
    }
    return c.NoContent(http.StatusNoContent)
}

func (h *ProposalsHandler) RejectProposal(c echo.Context) error {
    ctx := c.Request().Context()
    entityID := middleware.EntityID(ctx)
    userID := middleware.UserID(ctx)
    id := c.Param("id")

    var proposalType string
    if err := h.s.GetProposalType(ctx, entityID, id, &proposalType); err != nil {
        return echo.NewHTTPError(http.StatusNotFound, "not found")
    }

    var err error
    switch proposalType {
    case "new":
        err = h.s.RejectNewProposal(ctx, entityID, id, userID)
    case "ended":
        // Use current time as dismissed_computed_as_of — the engine will re-evaluate
        // when computed_as_of advances past this.
        err = h.s.RejectEndedProposal(ctx, entityID, id, userID, time.Now().UTC())
    default:
        return echo.NewHTTPError(http.StatusBadRequest, "unknown proposal type")
    }
    if err != nil {
        return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
    }
    return c.NoContent(http.StatusNoContent)
}
```

- [ ] **Step 2: Add `GetProposalType` to `store/proposals.go`**

Add this function to `services/web/store/proposals.go`:

```go
// GetProposalType loads only the proposal_type for routing approve/reject dispatch.
func (s *Store) GetProposalType(ctx context.Context, entityID, proposalID string, out *string) error {
    return s.pool.QueryRow(ctx, `
        SELECT proposal_type FROM entry_proposals
        WHERE entity_id = $1 AND id = $2::uuid AND status = 'pending'
    `, entityID, proposalID).Scan(out)
}
```

- [ ] **Step 3: Register ProposalsHandler routes in main**

Find where `EntriesHandler` routes are registered (likely `cmd/server` or similar). Add:

```go
proposalsH := handler.NewProposalsHandler(store, publisher)
proposalsH.RegisterRoutes(writeGroup)
```

Check `services/web/cmd/` or `services/web/main.go` for where `EntriesHandler.RegisterRoutes` is called — add `ProposalsHandler` registration immediately after.

- [ ] **Step 4: Remove `ApproveEntry` and `RejectEntry` from `handler/entries.go`**

Delete both functions and their route registrations:
- `func (h *EntriesHandler) ApproveEntry(...)` (around line 357–380)
- `func (h *EntriesHandler) RejectEntry(...)` (around line 382–395)
- `write.POST("/entries/:id/approve", h.ApproveEntry)` from `RegisterRoutes`
- `write.POST("/entries/:id/reject", h.RejectEntry)` from `RegisterRoutes`

- [ ] **Step 5: Build**

```bash
cd services/web && go build ./...
```

Expected: clean compile.

- [ ] **Step 6: Run tests**

```bash
cd services/web && go test ./...
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add services/web/handler/proposals.go services/web/handler/entries.go services/web/store/proposals.go
git commit -m "feat: add ProposalsHandler; remove old entry approve/reject endpoints"
```

---

## Task 7: Page handler — Review tab uses proposals

**Files:**
- Modify: `services/web/page/handler.go`

**Interfaces:**
- Consumes: `store.ListPendingProposals` from Task 4
- Produces: `LedgerData.Proposals []store.EntryProposalRow` populated when `filter == "pending"`; `alertTypeLabel` removed

- [ ] **Step 1: Add `Proposals` field to `LedgerData`**

Find the `LedgerData` struct in `page/handler.go`. Add:

```go
Proposals []store.EntryProposalRow
```

- [ ] **Step 2: Update the Ledger handler to populate Proposals for the Review tab**

In the `Ledger` handler function, find where entries are fetched based on `filter`. Add a case for `"pending"`:

```go
case "pending":
    data.Proposals, _ = s.store.ListPendingProposals(ctx, entityID)
    // entries stays empty — Review tab renders proposals, not entries
```

In the existing `switch filter` block (or wherever the entry fetch logic lives), ensure that when `filter == "pending"`, we do NOT call `ListEntries` — we only populate `Proposals`.

- [ ] **Step 3: Remove `alertTypeLabel`**

Find and delete the `alertTypeLabel` function (around line 821). It is now unused.

- [ ] **Step 4: Build**

```bash
cd services/web && go build ./...
```

Fix any remaining `AlertType` or `alertTypeLabel` references in template files — these will be addressed in Task 8.

- [ ] **Step 5: Commit**

```bash
git add services/web/page/handler.go
git commit -m "feat: Ledger Review tab populates proposals; remove alertTypeLabel"
```

---

## Task 8: Template — review queue renders proposals

**Files:**
- Modify: `services/web/page/ledger.templ`
- Regenerate: `services/web/page/ledger_templ.go` (via `templ generate`)

**Interfaces:**
- Consumes: `LedgerData.Proposals []store.EntryProposalRow` from Task 7
- Produces: Review tab renders proposal cards (label, direction, type, fitness, matched count, approve/reject buttons pointing to `/api/proposals/:id/approve|reject`); pending-entry review banner removed from entry rows

- [ ] **Step 1: Remove the `alert_type` pill from entry rows**

Find the line that renders the alert type pill (around line 941):

```templ
if e.AlertType != nil {
    <span ...>{ alertTypeLabel(e.AlertType) }</span>
}
```

Delete it entirely. `AlertType` no longer exists on `EntryRow`.

- [ ] **Step 2: Remove the pending-entry review banner**

Find the pending banner block (around line 960–974):

```templ
if e.Status == "pending" {
    <div ...>
        ...Approve...Reject buttons...
    </div>
}
```

Delete the entire block. Also remove any other `e.Status` references in entry row rendering — live/ended display logic should use `e.EndDate == nil` (live) or `e.EndDate != nil` (ended) if needed.

- [ ] **Step 3: Add proposal card rendering for the Review tab**

In the `ledger.templ` main content area, find where entries are rendered in a loop (something like `for _, e := range data.Entries`). Add a conditional block before it:

```templ
if data.Filter == "pending" {
    if len(data.Proposals) == 0 {
        <div style="padding:40px;text-align:center;color:var(--text3);font-size:13px">No pending proposals</div>
    }
    for _, p := range data.Proposals {
        @proposalCard(p)
    }
} else {
    for _, e := range data.Entries {
        @ledgerEntry(e, data)
    }
}
```

- [ ] **Step 4: Write the `proposalCard` component**

Add near the bottom of `ledger.templ`, before the closing of the file:

```templ
templ proposalCard(p store.EntryProposalRow) {
    <div style="border-bottom:1px solid var(--border);padding:12px 20px;display:flex;align-items:flex-start;gap:12px">
        <div style="flex:1;min-width:0">
            <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
                <span style="font-size:13px;font-weight:600;color:var(--text)">
                    if p.EntryLabelName != nil {
                        { *p.EntryLabelName }
                    } else {
                        Unknown
                    }
                </span>
                <span style={ "font-size:10px;font-weight:700;padding:1px 5px;border-radius:10px;background:var(--accent);color:#fff" }>
                    if p.ProposalType == "new" {
                        New
                    } else {
                        Ended
                    }
                </span>
                <span style="font-size:11px;color:var(--text3)">{ p.EntryDirection } · { p.EntryType }</span>
            </div>
            <div style="font-size:11px;color:var(--text3)">
                if p.EntryMatchedCount != nil {
                    { fmt.Sprintf("%d transactions", *p.EntryMatchedCount) }
                }
                if len(p.EntrySampleMerchants) > 0 {
                    <span style="margin-left:6px">{ strings.Join(p.EntrySampleMerchants[:min(3, len(p.EntrySampleMerchants))], " · ") }</span>
                }
            </div>
        </div>
        <div style="display:flex;gap:6px;align-items:center;flex-shrink:0">
            <button
                hx-post={ "/api/proposals/" + p.ID + "/reject" }
                hx-target="closest div[style*='border-bottom']"
                hx-swap="outerHTML"
                style="padding:5px 14px;border-radius:5px;border:1px solid var(--border);cursor:pointer;font-size:13px;font-weight:500;font-family:inherit;background:transparent;color:var(--text2)"
            >Reject</button>
            <button
                hx-post={ "/api/proposals/" + p.ID + "/approve" }
                hx-target="closest div[style*='border-bottom']"
                hx-swap="outerHTML"
                style="padding:5px 14px;border-radius:5px;border:none;cursor:pointer;font-size:13px;font-weight:500;font-family:inherit;background:var(--income);color:#fff"
            >Approve</button>
        </div>
    </div>
}
```

**Note on HTMX:** Check whether HTMX or Datastar is used for the existing review buttons (search `hx-post` or `data-action` in `ledger.templ`). The existing buttons use `data-action="approve"` with a JS handler (around line 529). Match the existing pattern rather than introducing HTMX if the rest of the page uses plain JS.

If the page uses a JS handler pattern, replace the buttons with:

```templ
<button
    class="js-proposal-btn"
    data-action="reject"
    data-proposal-id={ p.ID }
    style="..."
>Reject</button>
<button
    class="js-proposal-btn"
    data-action="approve"
    data-proposal-id={ p.ID }
    style="..."
>Approve</button>
```

And update the JS handler (around line 529) to handle `.js-proposal-btn` elements, POSTing to `/api/proposals/:id/approve` or `/api/proposals/:id/reject`.

- [ ] **Step 5: Regenerate the compiled template**

```bash
cd services/web && templ generate
```

- [ ] **Step 6: Build**

```bash
cd services/web && go build ./...
```

Expected: clean compile.

- [ ] **Step 7: Run tests**

```bash
cd services/web && go test ./...
```

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add services/web/page/ledger.templ services/web/page/ledger_templ.go
git commit -m "feat: Review tab renders proposal cards; remove pending-entry review UI"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|---|---|
| `entry_proposals` table created | Task 1 |
| `alert_type` removed from entries | Task 1, Task 5 |
| `status` column removed from `entries`; live/ended derived from `end_date` | Task 1, Task 5 |
| Stage 2 creates live entries | Task 2 |
| Stage 2 inserts `new` proposals | Task 2 |
| Stage 7 ended proposal detection | Task 3 |
| Stage 7 removes `pending` from eligible query | Task 3 |
| `ListPendingProposals` store function | Task 4 |
| `ApproveNewProposal` / `RejectNewProposal` | Task 4 |
| `ApproveEndedProposal` / `RejectEndedProposal` | Task 4 |
| Reject new → hard delete entry (cascade deletes proposal + assignments) | Task 4 |
| Reject ended → record `dismissed_computed_as_of` | Task 4 |
| `CountEntriesByStatus` counts proposals for Pending | Task 5 |
| `ProposalsHandler` with approve/reject routes | Task 6 |
| Old `ApproveEntry`/`RejectEntry` removed | Task 6 |
| Review tab populates proposals | Task 7 |
| Review tab renders proposal cards | Task 8 |
| `alert_type` pill removed from entry rows | Task 8 |
| Pending entry review banner removed | Task 8 |

### Not in this plan (deferred)

- **Drift proposals** — requires drift detection in stage 1/2, delta columns on proposals, close+new acceptance path. Separate plan.
- **`sample_merchants` removal from entries** — display cleanup, low risk. Separate cleanup task.
- **Snapshot orphans after new proposal rejection** — hard-deleting the entry removes it from future projections; any snapshots written during the brief live window for this entry's label will be cleaned on the next pipeline run when the label has no surviving entries.
- **`proposed_end_date` UI** — the ended proposal card should allow editing `proposed_end_date` before accepting. Deferred to a follow-up.

---

Plan complete and saved to `docs/superpowers/plans/2026-07-28-entry-proposals-phase1.md`.

**Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
