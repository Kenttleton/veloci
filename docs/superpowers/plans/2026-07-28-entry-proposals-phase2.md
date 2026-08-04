# Entry Proposals Phase 2 Implementation Plan — Drift Detection

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `drift` proposal detection for live standing entries whose actual rate has diverged significantly from their projected rate; surface drift proposals in the Review tab with two acceptance paths: in-place rate update or close-and-create-new.

**Architecture:** Scope is standing entries only (variable entries use only ended detection from Phase 1). Detection runs in a new `pipeline/drift.rs` module called from `run_from_stage3` in `mod.rs` after the day-crawl. Detection compares the latest snapshot's `actual_rate_per_day` against `entries.projected_rate_per_day`; if the delta exceeds `DRIFT_THRESHOLD` (20%) and the entry has at least `MIN_DRIFT_MATCHES` transactions (3), a drift proposal is inserted. Phase 2 detects rate drift only — period/anchor drift is deferred.

**Prerequisite:** Phase 1 must be complete and deployed. Phase 2 extends the `entry_proposals` schema and the `ProposalsHandler` from Phase 1.

**Tech Stack:** Go 1.26, Rust (sqlx), PostgreSQL, Templ

## Global Constraints

- No ALTER TABLE — all schema changes go directly into `migrations/app/002_financial_schema.sql`; rebuild with `just clean && just infra && just migrate`
- All tests must pass before each commit: `cargo test` in `services/engine`, `go test ./...` in `services/web`
- Drift detection applies to `entry_type = 'standing'` entries only; `variable` and `system` entries are skipped
- Drift detection applies only when `entries.projected_rate_per_day IS NOT NULL` — a null projected rate means the engine hasn't committed a rate for that entry yet
- `DRIFT_THRESHOLD = 0.20` (20% delta ratio) — if `|actual - projected| / projected > 0.20`, propose drift
- `MIN_DRIFT_MATCHES = 3` — fewer than 3 matched transactions is not enough evidence
- Both approval paths (in-place, close+new) must dispatch `account.analyze` after completion
- Close+new: new entry inherits all fields from old entry except `projected_rate_per_day`, `start_date`, `end_date`, `next_due_date`; new entry's `source` stays `'engine'`
- Period/anchor drift detection is **out of scope** — `proposed_period_days` and `proposed_recurrence_anchor` columns are added to schema but not populated by the engine in this phase

---

## File Map

| File | Change |
|---|---|
| `migrations/app/002_financial_schema.sql` | Add `'drift'` to `proposal_type` CHECK; add drift columns to `entry_proposals` |
| `services/engine/src/pipeline/drift.rs` | New: `detect_drift_proposals` function; `DRIFT_THRESHOLD`, `MIN_DRIFT_MATCHES` constants |
| `services/engine/src/pipeline/mod.rs` | Wire `drift::detect_drift_proposals` call after day-crawl in `run_from_stage3` |
| `services/web/store/proposals.go` | Add: `ApproveDriftProposalInPlace`, `ApproveDriftProposalCloseNew`, `RejectDriftProposal` |
| `services/web/handler/proposals.go` | Extend: `ApproveProposal` handles `drift` with `path` body param; `RejectProposal` handles `drift` |
| `services/web/page/ledger.templ` | Add: drift proposal card with "Update rate" and "Close & new" buttons |

---

## Task 1: Schema — add drift to entry_proposals

**Files:**
- Modify: `migrations/app/002_financial_schema.sql`

**Interfaces:**
- Produces: `entry_proposals.proposal_type` CHECK includes `'drift'`; drift columns present and nullable

- [ ] **Step 1: Add `'drift'` to the `proposal_type` CHECK**

In `002_financial_schema.sql`, find the `entry_proposals` table and change:

```sql
  proposal_type               TEXT        NOT NULL CHECK (proposal_type IN ('new', 'ended')),
```

To:

```sql
  proposal_type               TEXT        NOT NULL CHECK (proposal_type IN ('new', 'ended', 'drift')),
```

- [ ] **Step 2: Add drift columns to `entry_proposals`**

After the `proposed_end_date` line, add:

```sql
  -- drift proposal only
  proposed_rate_per_day       NUMERIC,    -- engine-computed new rate at detection time
  proposed_period_days        INTEGER,    -- future: period drift (not populated in Phase 2)
  proposed_recurrence_anchor  TEXT,       -- future: anchor drift (not populated in Phase 2)
  drift_delta_ratio           NUMERIC,    -- |actual - projected| / projected at detection time
```

- [ ] **Step 3: Commit**

```bash
git add migrations/app/002_financial_schema.sql
git commit -m "feat: add drift to entry_proposals schema (proposal_type, drift delta columns)"
```

---

## Task 2: Rust — drift.rs detection module

**Files:**
- Create: `services/engine/src/pipeline/drift.rs`

**Interfaces:**
- Consumes: `entry_proposals` table (from Task 1); `snapshots` table (latest `actual_rate_per_day` at `computed_as_of`); `entries` table (`projected_rate_per_day`, `matched_transaction_count`)
- Produces: `detect_drift_proposals(entity_id, computed_as_of, pool)` async function; inserts `entry_proposals (type='drift', status='pending')` rows

**Algorithm:**

For each live standing engine/user entry where `projected_rate_per_day IS NOT NULL`:
1. Load the latest `actual_rate_per_day` from `snapshots` at `computed_as_of`
2. Compute `delta_ratio = |actual - projected| / projected`
3. If `delta_ratio > DRIFT_THRESHOLD` AND `matched_transaction_count >= MIN_DRIFT_MATCHES`:
   - Skip if a pending drift proposal already exists for this entry
   - Skip if a dismissed drift proposal exists with `dismissed_transaction_count >= current matched_transaction_count`
   - Insert drift proposal

- [ ] **Step 1: Write the unit test for `should_propose_drift`**

Create `services/engine/src/pipeline/drift.rs` with:

```rust
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn drift_above_threshold_triggers() {
        // 25% delta exceeds 20% threshold with enough matches.
        assert!(should_propose_drift(100.0, 125.0, 3));
    }

    #[test]
    fn drift_below_threshold_skips() {
        // 10% delta is under threshold.
        assert!(!should_propose_drift(100.0, 110.0, 3));
    }

    #[test]
    fn drift_exact_threshold_skips() {
        // 20% delta is not strictly greater than threshold.
        assert!(!should_propose_drift(100.0, 120.0, 3));
    }

    #[test]
    fn drift_insufficient_matches_skips() {
        // 50% delta but only 2 matches — not enough evidence.
        assert!(!should_propose_drift(100.0, 150.0, 2));
    }

    #[test]
    fn drift_zero_projected_skips() {
        // Divide-by-zero guard: if projected is zero, no drift.
        assert!(!should_propose_drift(0.0, 50.0, 5));
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd services/engine && cargo test drift
```

Expected: FAIL — `should_propose_drift` undefined.

- [ ] **Step 3: Implement `drift.rs`**

```rust
//! Drift proposal detection.
//!
//! Runs after the flux-window day-crawl. For each live standing entry whose
//! actual rate (from the latest snapshot) has diverged more than DRIFT_THRESHOLD
//! from its stored projected rate, an `entry_proposals` row of type 'drift' is
//! inserted so the user can review.

use anyhow::{Context, Result};
use chrono::NaiveDate;
use sqlx::PgPool;
use uuid::Uuid;

/// Minimum relative delta to trigger a drift proposal (20%).
const DRIFT_THRESHOLD: f64 = 0.20;

/// Minimum matched transaction count required before proposing drift.
const MIN_DRIFT_MATCHES: i32 = 3;

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Detect drift for all live standing entries of an entity.
///
/// Compares the latest `snapshots.actual_rate_per_day` at `computed_as_of`
/// against `entries.projected_rate_per_day`. Inserts an `entry_proposals` row
/// when the delta ratio exceeds DRIFT_THRESHOLD and the entry has enough
/// transaction evidence, unless a proposal is already pending or recently dismissed.
pub async fn detect_drift_proposals(
    entity_id: Uuid,
    computed_as_of: NaiveDate,
    pool: &PgPool,
) -> Result<()> {
    let candidates = load_drift_candidates(entity_id, computed_as_of, pool).await?;

    for c in candidates {
        if !should_propose_drift(c.projected_rate, c.actual_rate, c.matched_count) {
            continue;
        }

        let already_blocked: Option<(i32,)> = sqlx::query_as(
            "SELECT 1 FROM entry_proposals
             WHERE reference_entry_id = $1
               AND proposal_type = 'drift'
               AND (status = 'pending'
                    OR (status = 'dismissed'
                        AND dismissed_transaction_count >= $2))",
        )
        .bind(c.entry_id)
        .bind(c.matched_count)
        .fetch_optional(pool)
        .await
        .context("failed to check existing drift proposal")?;

        if already_blocked.is_some() {
            continue;
        }

        let delta_ratio = (c.actual_rate - c.projected_rate).abs() / c.projected_rate.abs();

        sqlx::query(
            "INSERT INTO entry_proposals
               (entity_id, proposal_type, status, reference_entry_id,
                proposed_rate_per_day, drift_delta_ratio)
             VALUES ($1, 'drift', 'pending', $2, $3, $4)",
        )
        .bind(entity_id)
        .bind(c.entry_id)
        .bind(c.actual_rate)
        .bind(delta_ratio)
        .execute(pool)
        .await
        .context("failed to insert drift proposal")?;
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// Pure predicate (unit-testable)
// ---------------------------------------------------------------------------

/// Returns true when a drift proposal should be created.
///
/// `projected` and `actual` are both in the same unit (cents/day); `matched_count`
/// is the total transactions assigned to this entry.
pub(crate) fn should_propose_drift(projected: f64, actual: f64, matched_count: i32) -> bool {
    if projected == 0.0 {
        return false;
    }
    if matched_count < MIN_DRIFT_MATCHES {
        return false;
    }
    let delta_ratio = (actual - projected).abs() / projected.abs();
    delta_ratio > DRIFT_THRESHOLD
}

// ---------------------------------------------------------------------------
// DB loader
// ---------------------------------------------------------------------------

#[derive(Debug)]
struct DriftCandidate {
    entry_id:       Uuid,
    projected_rate: f64,
    actual_rate:    f64,
    matched_count:  i32,
}

async fn load_drift_candidates(
    entity_id: Uuid,
    computed_as_of: NaiveDate,
    pool: &PgPool,
) -> Result<Vec<DriftCandidate>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        entry_id:                Uuid,
        projected_rate_per_day:  sqlx::types::BigDecimal,
        actual_rate_per_day:     sqlx::types::BigDecimal,
        matched_transaction_count: i32,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT
            e.id                         AS entry_id,
            e.projected_rate_per_day,
            s.actual_rate_per_day,
            COALESCE(e.matched_transaction_count, 0) AS matched_transaction_count
        FROM entries e
        JOIN LATERAL (
            SELECT actual_rate_per_day
            FROM snapshots
            WHERE entity_id = $1
              AND node_id = e.id
              AND node_type = 'entry'
              AND snapshot_date <= $2
            ORDER BY snapshot_date DESC
            LIMIT 1
        ) s ON true
        WHERE e.entity_id = $1
          AND (e.end_date IS NULL OR e.end_date >= $2)
          AND e.entry_type = 'standing'
          AND e.source != 'system'
          AND e.projected_rate_per_day IS NOT NULL
        "#,
    )
    .bind(entity_id)
    .bind(computed_as_of)
    .fetch_all(pool)
    .await
    .context("failed to load drift candidates")?;

    Ok(rows
        .into_iter()
        .filter_map(|r| {
            let projected = r.projected_rate_per_day.to_string().parse::<f64>().ok()?;
            let actual    = r.actual_rate_per_day.to_string().parse::<f64>().ok()?;
            Some(DriftCandidate {
                entry_id:      r.entry_id,
                projected_rate: projected,
                actual_rate:    actual,
                matched_count:  r.matched_transaction_count,
            })
        })
        .collect())
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd services/engine && cargo test drift
```

Expected: 5 unit tests pass.

- [ ] **Step 5: Commit**

```bash
git add services/engine/src/pipeline/drift.rs
git commit -m "feat: drift.rs module with detect_drift_proposals and should_propose_drift"
```

---

## Task 3: Wire drift detection into the pipeline

**Files:**
- Modify: `services/engine/src/pipeline/mod.rs`

**Interfaces:**
- Consumes: `drift::detect_drift_proposals` from Task 2
- Produces: drift detection runs once per pipeline execution, after the day-crawl and before stage 7

- [ ] **Step 1: Add the drift module declaration**

In `services/engine/src/pipeline/mod.rs`, find the `pub mod` block and add:

```rust
pub mod drift;
```

- [ ] **Step 2: Wire the call in `run_from_stage3`**

In `run_from_stage3`, find the block after the day-crawl loop and the system-entry rate sync, immediately before `run_stage7(...)`. Add:

```rust
// Drift proposal detection: compare latest snapshot rates vs stored projected rates.
// Runs once at computed_as_of (not per-day) since we need the final crawl output.
drift::detect_drift_proposals(entity_id, computed_as_of, &pools.write)
    .await
    .context("drift proposal detection failed")?;
```

The full sequence in `run_from_stage3` should be:
1. Day-crawl loop (stages 3–6)
2. System entry projected_rate sync (existing)
3. **`drift::detect_drift_proposals`** (new)
4. `run_stage7` (existing)

- [ ] **Step 3: Run all engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass (215 + the 5 new drift tests = 220).

- [ ] **Step 4: Commit**

```bash
git add services/engine/src/pipeline/mod.rs
git commit -m "feat: wire drift proposal detection into run_from_stage3 pipeline"
```

---

## Task 4: Go store — drift proposal approval paths

**Files:**
- Modify: `services/web/store/proposals.go`

**Interfaces:**
- Produces:
  - `func (s *Store) ApproveDriftProposalInPlace(ctx, entityID, proposalID, userID string) error`
  - `func (s *Store) ApproveDriftProposalCloseNew(ctx, entityID, proposalID, userID string) (newEntryID string, err error)`
  - `func (s *Store) RejectDriftProposal(ctx, entityID, proposalID, userID string) error`

- [ ] **Step 1: Write the constant test for drift proposal type**

In `services/web/store/proposals_test.go`, add:

```go
func TestDriftProposalTypeConstant(t *testing.T) {
    if proposalTypeDrift != "drift" {
        t.Errorf("expected 'drift', got %q", proposalTypeDrift)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd services/web && go test ./store/... -run TestDriftProposalTypeConstant -v
```

Expected: FAIL — `proposalTypeDrift` undefined.

- [ ] **Step 3: Add `proposalTypeDrift` constant and three store functions to `proposals.go`**

Add to the constants block at the top of `store/proposals.go`:

```go
proposalTypeDrift  = "drift"
```

Then add the three functions after `RejectEndedProposal`:

```go
// ApproveDriftProposalInPlace accepts a drift proposal by updating the
// entry's projected_rate_per_day to the proposed value in place.
// The entry keeps its start_date, label, and all other fields unchanged.
func (s *Store) ApproveDriftProposalInPlace(ctx context.Context, entityID, proposalID, userID string) error {
    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx) //nolint:errcheck

    var entryID string
    var proposedRate float64
    err = tx.QueryRow(ctx, `
        SELECT ep.reference_entry_id::text, ep.proposed_rate_per_day::float8
        FROM entry_proposals ep
        WHERE ep.entity_id = $1 AND ep.id = $2::uuid
          AND ep.proposal_type = 'drift' AND ep.status = 'pending'
    `, entityID, proposalID).Scan(&entryID, &proposedRate)
    if err != nil {
        return err
    }

    if _, err = tx.Exec(ctx, `
        UPDATE entries SET projected_rate_per_day = $2
        WHERE entity_id = $1 AND id = $3::uuid
    `, entityID, proposedRate, entryID); err != nil {
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

// ApproveDriftProposalCloseNew accepts a drift proposal by closing the current
// entry (end_date = yesterday, status = 'ended') and creating a new entry that
// inherits all fields except projected_rate_per_day, start_date, end_date, and
// next_due_date. Returns the new entry's ID.
func (s *Store) ApproveDriftProposalCloseNew(ctx context.Context, entityID, proposalID, userID string) (string, error) {
    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return "", err
    }
    defer tx.Rollback(ctx) //nolint:errcheck

    // Load proposal + existing entry fields needed to clone.
    var entryID string
    var proposedRate float64
    err = tx.QueryRow(ctx, `
        SELECT ep.reference_entry_id::text, ep.proposed_rate_per_day::float8
        FROM entry_proposals ep
        WHERE ep.entity_id = $1 AND ep.id = $2::uuid
          AND ep.proposal_type = 'drift' AND ep.status = 'pending'
    `, entityID, proposalID).Scan(&entryID, &proposedRate)
    if err != nil {
        return "", err
    }

    // Load fields to clone from the existing entry.
    var (
        labelID           *string
        direction         string
        entryType         string
        periodDays        *int
        recurrenceAnchor  *string
        conditions        *string
        rateMethod        string
        fitness           *float64
        source            string
    )
    err = tx.QueryRow(ctx, `
        SELECT
            label_id::text, direction, entry_type, period_days,
            recurrence_anchor, conditions::text, rate_method,
            fitness, source
        FROM entries
        WHERE entity_id = $1 AND id = $2::uuid
    `, entityID, entryID).Scan(
        &labelID, &direction, &entryType, &periodDays,
        &recurrenceAnchor, &conditions, &rateMethod,
        &fitness, &source,
    )
    if err != nil {
        return "", err
    }

    // Close the old entry: set end_date to yesterday.
    // status column no longer exists — ended is derived from end_date < computed_as_of.
    if _, err = tx.Exec(ctx, `
        UPDATE entries
        SET end_date = (NOW()::date - 1)
        WHERE entity_id = $1 AND id = $2::uuid
    `, entityID, entryID); err != nil {
        return "", err
    }

    // Create the new entry with the proposed rate and start_date = today.
    var newEntryID string
    err = tx.QueryRow(ctx, `
        INSERT INTO entries (
            entity_id, label_id, direction, entry_type, period_days,
            recurrence_anchor, conditions, projected_rate_per_day,
            rate_method, fitness, source, start_date
        ) VALUES (
            $1, $2::uuid, $3, $4, $5,
            $6, $7::jsonb, $8,
            $9, $10, $11, NOW()::date
        )
        RETURNING id::text
    `, entityID, labelID, direction, entryType, periodDays,
        recurrenceAnchor, conditions, proposedRate,
        rateMethod, fitness, source,
    ).Scan(&newEntryID)
    if err != nil {
        return "", err
    }

    // Mark proposal accepted.
    if _, err = tx.Exec(ctx, `
        UPDATE entry_proposals
        SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
        WHERE entity_id = $1 AND id = $2::uuid
    `, entityID, proposalID, userID); err != nil {
        return "", err
    }

    return newEntryID, tx.Commit(ctx)
}

// RejectDriftProposal dismisses a drift proposal and records the current
// matched_transaction_count as the re-trigger threshold.
func (s *Store) RejectDriftProposal(ctx context.Context, entityID, proposalID, userID string) error {
    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx) //nolint:errcheck

    var matchedCount int
    err = tx.QueryRow(ctx, `
        SELECT COALESCE(e.matched_transaction_count, 0)
        FROM entry_proposals ep
        JOIN entries e ON e.id = ep.reference_entry_id
        WHERE ep.entity_id = $1 AND ep.id = $2::uuid
          AND ep.proposal_type = 'drift' AND ep.status = 'pending'
    `, entityID, proposalID).Scan(&matchedCount)
    if err != nil {
        return err
    }

    if _, err = tx.Exec(ctx, `
        UPDATE entry_proposals
        SET status = 'dismissed',
            dismissed_transaction_count = $3,
            reviewed_by = $4::uuid,
            reviewed_at = NOW()
        WHERE entity_id = $1 AND id = $2::uuid
    `, entityID, proposalID, matchedCount, userID); err != nil {
        return err
    }

    return tx.Commit(ctx)
}
```

- [ ] **Step 4: Run tests**

```bash
cd services/web && go test ./store/... -run TestDriftProposalTypeConstant -v
```

Expected: PASS.

- [ ] **Step 5: Build**

```bash
cd services/web && go build ./...
```

Expected: clean compile.

- [ ] **Step 6: Commit**

```bash
git add services/web/store/proposals.go services/web/store/proposals_test.go
git commit -m "feat: ApproveDriftProposalInPlace, ApproveDriftProposalCloseNew, RejectDriftProposal store functions"
```

---

## Task 5: Go handler — extend ProposalsHandler for drift

**Files:**
- Modify: `services/web/handler/proposals.go`

**Interfaces:**
- Consumes: `store.ApproveDriftProposalInPlace`, `store.ApproveDriftProposalCloseNew`, `store.RejectDriftProposal`
- Produces: `ApproveProposal` handles `drift` proposal type with a `path` body field (`"inplace"` or `"close_new"`); `RejectProposal` handles `drift`

- [ ] **Step 1: Add drift case to `ApproveProposal`**

In `services/web/handler/proposals.go`, find the `ApproveProposal` handler. It currently has a `switch proposalType` block. Add the `"drift"` case:

```go
case "drift":
    var body struct {
        Path string `json:"path"` // "inplace" or "close_new"
    }
    if err := c.Bind(&body); err != nil || (body.Path != "inplace" && body.Path != "close_new") {
        return echo.NewHTTPError(http.StatusBadRequest, "path must be 'inplace' or 'close_new'")
    }
    if body.Path == "close_new" {
        _, err = h.s.ApproveDriftProposalCloseNew(ctx, entityID, id, userID)
    } else {
        err = h.s.ApproveDriftProposalInPlace(ctx, entityID, id, userID)
    }
```

- [ ] **Step 2: Add drift case to `RejectProposal`**

In the `RejectProposal` handler, in the `switch proposalType` block, add:

```go
case "drift":
    err = h.s.RejectDriftProposal(ctx, entityID, id, userID)
```

- [ ] **Step 3: Build**

```bash
cd services/web && go build ./...
```

Expected: clean compile.

- [ ] **Step 4: Run all tests**

```bash
cd services/web && go test ./...
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add services/web/handler/proposals.go
git commit -m "feat: ProposalsHandler handles drift proposals with inplace/close_new path"
```

---

## Task 6: Template — drift proposal card

**Files:**
- Modify: `services/web/page/ledger.templ`
- Regenerate: `services/web/page/ledger_templ.go` (via `templ generate`)

**Interfaces:**
- Consumes: `EntryProposalRow.ProposalType == "drift"`, `.ProposedRate` (need to add to the joined SELECT in `ListPendingProposals`)
- Produces: drift proposal cards show current rate, proposed rate, delta; two action buttons ("Update rate" → `path=inplace`, "Close & new" → `path=close_new`)

- [ ] **Step 1: Add `ProposedRate` and `DriftDeltaRatio` to `EntryProposalRow`**

In `services/web/store/proposals.go`, add to the `EntryProposalRow` struct:

```go
ProposedRate     *float64 `db:"proposed_rate_per_day"`
DriftDeltaRatio  *float64 `db:"drift_delta_ratio"`
```

And add these columns to the SELECT in `ListPendingProposals`:

```sql
ep.proposed_rate_per_day::float8 AS proposed_rate_per_day,
ep.drift_delta_ratio::float8     AS drift_delta_ratio,
```

- [ ] **Step 2: Build to catch any missing field errors**

```bash
cd services/web && go build ./...
```

- [ ] **Step 3: Add the `driftProposalCard` component in `ledger.templ`**

Find where `proposalCard` is defined (added in Phase 1). Add a separate component for drift:

```templ
templ driftProposalCard(p store.EntryProposalRow) {
    <div style="border-bottom:1px solid var(--border);padding:12px 20px">
        <div style="display:flex;align-items:flex-start;gap:12px">
            <div style="flex:1;min-width:0">
                <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
                    <span style="font-size:13px;font-weight:600;color:var(--text)">
                        if p.EntryLabelName != nil {
                            { *p.EntryLabelName }
                        } else {
                            Unknown
                        }
                    </span>
                    <span style="font-size:10px;font-weight:700;padding:1px 5px;border-radius:10px;background:var(--warning,#f59e0b);color:#fff">
                        Drift
                    </span>
                    <span style="font-size:11px;color:var(--text3)">{ p.EntryDirection } · { p.EntryType }</span>
                </div>
                <div style="font-size:11px;color:var(--text3);display:flex;gap:12px">
                    if p.EntryProjectedRate != nil {
                        <span>Current: { fmt.Sprintf("$%.2f/day", *p.EntryProjectedRate) }</span>
                    }
                    if p.ProposedRate != nil {
                        <span style="color:var(--accent)">Proposed: { fmt.Sprintf("$%.2f/day", *p.ProposedRate) }</span>
                    }
                    if p.DriftDeltaRatio != nil {
                        <span>({ fmt.Sprintf("%.0f%%", *p.DriftDeltaRatio*100) } change)</span>
                    }
                </div>
            </div>
            <div style="display:flex;gap:6px;align-items:center;flex-shrink:0">
                <button
                    class="js-proposal-btn"
                    data-action="reject"
                    data-proposal-id={ p.ID }
                    style="padding:5px 14px;border-radius:5px;border:1px solid var(--border);cursor:pointer;font-size:13px;font-weight:500;font-family:inherit;background:transparent;color:var(--text2)"
                >Reject</button>
                <button
                    class="js-proposal-btn"
                    data-action="approve"
                    data-proposal-id={ p.ID }
                    data-path="inplace"
                    style="padding:5px 14px;border-radius:5px;border:1px solid var(--income);cursor:pointer;font-size:13px;font-weight:500;font-family:inherit;background:transparent;color:var(--income)"
                >Update rate</button>
                <button
                    class="js-proposal-btn"
                    data-action="approve"
                    data-proposal-id={ p.ID }
                    data-path="close_new"
                    style="padding:5px 14px;border-radius:5px;border:none;cursor:pointer;font-size:13px;font-weight:500;font-family:inherit;background:var(--income);color:#fff"
                >Close &amp; new</button>
            </div>
        </div>
    </div>
}
```

- [ ] **Step 4: Route proposal types in the Review tab loop**

Find the `proposalCard` call in the Review tab loop (added in Phase 1):

```templ
for _, p := range data.Proposals {
    @proposalCard(p)
}
```

Change to dispatch by type:

```templ
for _, p := range data.Proposals {
    if p.ProposalType == "drift" {
        @driftProposalCard(p)
    } else {
        @proposalCard(p)
    }
}
```

- [ ] **Step 5: Update the JS handler to pass `path` for drift approvals**

Find the JS handler that handles `.js-proposal-btn` clicks (wired in Phase 1). Update it to include the `path` field in the request body when present:

```javascript
document.addEventListener('click', async (e) => {
    const btn = e.target.closest('.js-proposal-btn');
    if (!btn) return;
    const action = btn.dataset.action;
    const id = btn.dataset.proposalId;
    const path = btn.dataset.path; // undefined for new/ended, set for drift
    const url = `/api/proposals/${id}/${action}`;
    const body = path ? JSON.stringify({ path }) : '{}';
    await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body,
    });
    // Reload the review tab to reflect the change.
    window.location.reload();
});
```

**Note:** Check the existing handler pattern — if Phase 1 used a different event pattern (e.g., Datastar attributes), match it rather than using raw fetch.

- [ ] **Step 6: Regenerate compiled template**

```bash
cd services/web && templ generate
```

- [ ] **Step 7: Build and test**

```bash
cd services/web && go build ./... && go test ./...
```

Expected: clean compile, all tests pass.

- [ ] **Step 8: Commit**

```bash
git add services/web/page/ledger.templ services/web/page/ledger_templ.go services/web/store/proposals.go
git commit -m "feat: drift proposal card with Update rate and Close & new actions"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|---|---|
| `'drift'` added to `proposal_type` CHECK | Task 1 |
| Drift columns (`proposed_rate_per_day`, `drift_delta_ratio`, etc.) | Task 1 |
| `detect_drift_proposals` Rust function | Task 2 |
| `should_propose_drift` pure predicate with tests | Task 2 |
| DRIFT_THRESHOLD = 20%, MIN_DRIFT_MATCHES = 3 | Task 2 |
| Standing entries only, system entries excluded | Task 2 |
| Re-trigger guard: `dismissed_transaction_count` check | Task 2 |
| Drift detection wired into `run_from_stage3` after day-crawl | Task 3 |
| `ApproveDriftProposalInPlace` store function | Task 4 |
| `ApproveDriftProposalCloseNew` store function (closes old, creates new) | Task 4 |
| `RejectDriftProposal` store function with re-trigger threshold | Task 4 |
| Handler: `drift` case with `path` param | Task 5 |
| Drift proposal card with current/proposed rate display | Task 6 |
| "Update rate" (inplace) and "Close & new" buttons | Task 6 |
| Proposal type routing in Review tab loop | Task 6 |
| `account.analyze` dispatched after both approval paths | Task 5 (inherited from `ApproveProposal`) |

### Not in this plan (deferred)

- **Period/anchor drift detection** — `proposed_period_days` / `proposed_recurrence_anchor` columns exist but are not populated by the engine. Detecting timing drift requires comparing observed transaction intervals vs `period_days` / `recurrence_anchor` — a separate analysis.
- **DRIFT_THRESHOLD configurability** — currently a compile-time constant; could be moved to `entity_config` in a future plan.
- **Variable entry drift** — out of scope by design; variable entries only get ended proposals.
- **`close_new` entry `conditions` inheritance** — the new entry clones `conditions` from the old entry. If conditions reference merchants that no longer apply, the user should update them manually after the close+new.

---

Plan complete and saved to `docs/superpowers/plans/2026-07-28-entry-proposals-phase2.md`.

**Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks

**2. Inline Execution** — execute tasks in this session using executing-plans

**Which approach?**
