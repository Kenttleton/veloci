# Snapshot Backfill & Dirty Detection Design

**Date:** 2026-08-11
**Status:** Draft
**Scope:** Extend the pipeline to generate full-history snapshots on first import and re-compute only affected (entry, date) pairs on subsequent imports using per-entry dirty detection.

---

## 1. Problem

The pipeline currently generates snapshots only for the flux/settlement window — the most recent N days before `computed_as_of`. For a first import spanning a full year, only the last N days get snapshots. January through month N-1 are never computed, making the Budget and Reports pages show only a narrow slice of history.

Additionally, when new transactions arrive that affect historical periods, the pipeline has no mechanism to selectively re-run only the affected (entry, date) pairs. It either re-runs the entire flux window for all entries (wasteful) or skips older history entirely (incorrect).

---

## 2. Goals

- On first import: generate snapshots from `history_start` (MIN transaction date) to `computed_as_of` for all active entries.
- On subsequent imports: compute only (entry, date) pairs whose inputs changed. Skip pairs with valid existing snapshots and no change in inputs.
- On new account added: automatically backfill any entries that received new transaction assignments from the new account's data.
- Ended entries with complete snapshots are never re-run unless their inputs changed.
- `entries.reprocess` and `account.analyze` jobs bypass dirty detection and do a full re-run.

---

## 3. Core Invariant

A snapshot for `(entry_id, snapshot_date)` is recomputed if and only if at least one of these is true:

1. **Gap fill** — no snapshot exists for that `(entry_id, snapshot_date)` pair
2. **Flux window** — `snapshot_date >= flux_start` (settlement volatility; existing behavior)
3. **Touched entry** — the entry was marked dirty for a date range that includes `snapshot_date`

Pairs matching none of these conditions are skipped. Their existing snapshot rows are authoritative.

---

## 4. Touch Sources — What Marks an Entry Dirty

An entry is "touched" when any of the following occur during an import:

### Source A — New Transaction Assignment
Stage 1 assigns a transaction (newly inserted by stage 0) to an entry. The entry is dirty from the gap start: wherever its snapshot history left off.

### Source B — Superseded Old Assignment
Stage 0 deletes a `young_flux` transaction and inserts a replacement. The entry that was previously assigned to the deleted transaction loses that assignment. Because the old transaction is deleted before stage 1 runs, stage 0 must capture the affected entry IDs **before** deletion by querying `transaction_entry_assignments`.

### Source C — Entry Metadata Change
An entry whose `updated_at` is more recent than its last `computed_as_of` has had conditions, period_days, start_date, end_date, or other config updated. Since these fields affect rate computation across the full history, the entry is dirty from its `start_date`.

---

## 5. Dirty From — The Gap Start

**Sources A and B** (transaction-driven):

```text
dirty_from = last_snapshot_date_for_entry  (MAX snapshot_date for this entry)
           = entry.start_date              (if no snapshots exist yet)
```

The transaction date, settlement buffer, or period_days do not factor into `dirty_from`. Entries represent rate over time, so the correct re-computation boundary is the gap from the last known good state forward — not a window around any individual transaction date.

**Source C** (entry metadata change):

```text
dirty_from = entry.start_date  (always)
```

A config change to conditions, period_days, start_date, or end_date affects all historical rate computations for that entry — including dates that already have snapshot rows. Recomputing only from the last snapshot would leave earlier snapshots stale. Source C always recomputes the full entry history.

**Dirty range upper bound (`dirty_to`):**

```text
dirty_to = entry.end_date   if end_date is set AND end_date <= computed_as_of
dirty_to = computed_as_of   if end_date is null OR end_date > computed_as_of
```

The full dirty range for a touched entry is `[dirty_from, dirty_to]`. No snapshots are produced outside this range — snapshots after `end_date` are never written, and snapshots before `dirty_from` are left untouched.

When an entry appears in multiple touch sources, the earliest `dirty_from` across all sources wins:

```rust
dirty_entry_starts.entry(entry_id)
    .and_modify(|d| *d = (*d).min(dirty_from))
    .or_insert(dirty_from);
```

---

## 6. Entry Ceilings — Ended Entries

An entry with `end_date` set never receives snapshots beyond that date. The crawl respects a per-entry ceiling:

```
ceiling = entry.end_date   (for ended entries)
ceiling = computed_as_of   (for live entries)
```

An ended entry whose existing snapshots already cover `[start_date, end_date]` with no gaps, and whose inputs haven't changed, produces no dirty pairs. It is skipped entirely — no recomputation, no DB access for that entry.

---

## 7. Pre-flight Queries

Between stage 1 and the day-crawl, the following are assembled:

| Query | Purpose |
|---|---|
| `MAX(snapshot_date) per entry` | Per-entry gap start anchor |
| `MIN(snapshot_date)` across entity | Detect if history predates any snapshot |
| `MIN(transactions.date)` for entity | `history_start` — left boundary if no snapshots |
| `SELECT node_id, snapshot_date FROM snapshots WHERE entity_id = $1` | `existing_snapshots: HashSet<(Uuid, NaiveDate)>` for gap-fill check |
| Entries where `e.updated_at > MAX(s.computed_as_of)` (join entries to snapshots by node_id, NULL computed_as_of counts as dirty) | Source C touched entries |

---

## 8. Crawl Range

```rust
let crawl_start = [
    Some(flux_start),
    dirty_entry_starts.values().copied().min(),
    if min_existing_snapshot.map_or(true, |m| history_start < m) {
        Some(history_start)
    } else {
        None
    },
]
.into_iter().flatten().min().unwrap_or(flux_start);
```

If nothing is dirty outside the flux window, `crawl_start = flux_start` — identical to existing behavior with no extra computation.

---

## 9. Per-Day Dirty Set and Skip Logic

For each date in `[crawl_start, computed_as_of]`:

```rust
let dirty_entries: Vec<Uuid> = all_active_entries.iter()
    .filter(|e| {
        let ceiling = entry_ceilings[&e.id];
        if date > ceiling { return false; }  // past end_date

        let cascade_from = dirty_entry_starts
            .get(&e.id)
            .copied()
            .map(|d| d.min(flux_start))   // flux window is a floor: all entries dirty >= flux_start
            .unwrap_or(flux_start);

        date >= cascade_from                                        // Sources A, B, C + flux
            || !existing_snapshots.contains(&(e.id, date))         // gap fill
    })
    .map(|e| e.id)
    .collect();

if dirty_entries.is_empty() {
    continue;  // skip date entirely — no stage 3–6 execution, no DB access
}
```

Dates with no dirty entries are skipped entirely. This is the primary compute saving for long-history household data: years of stable history produce zero dirty pairs on a routine monthly import.

---

## 10. Stage Output Changes

### Stage 0 — `Stage0Output`

Add before transaction deletion:
```rust
// Query entries assigned to superseded tx_ids before deleting them
superseded_entry_ids: Vec<Uuid>
```

### Stage 1 — `Stage1Output`

Add alongside `unmatched_tx_ids`:
```rust
// (entry_id, tx.date) for all newly assigned transactions
new_entry_assignments: Vec<(Uuid, NaiveDate)>
```

The dates in `new_entry_assignments` are not used for `dirty_from` (see §5), but are retained for diagnostics and potential future use.

---

## 11. Stage Behavior Changes (3–6)

All stages from 3 onward receive `dirty_entries: &[Uuid]` and filter computation to those entries only.

| Stage | Change |
|---|---|
| Stage 3 | Compute rates only for `dirty_entries`. Return `entry_rates` for dirty entries only. |
| Stage 4 | Labels are UI-facing groupings. Label-level snapshot rows are not produced or maintained by the backend. Entry rates from stage 3 flow directly to stage 5. |
| Stage 5 | Compute slope/drift only for `dirty_entries`. Reads existing snapshot history as today. |
| Stage 6 | UPSERT only for `dirty_entries`. Clean entries are untouched. |

---

## 12. Job Type Behavior

| Job Type | Dirty Detection |
|---|---|
| `import.process` | Uses dirty detection — Sources A, B, C + gap fill + flux window |
| `entries.reprocess` | Bypasses dirty detection. Full crawl from `history_start`. Used when user explicitly triggers reprocess. |
| `account.analyze` | Bypasses dirty detection. Full crawl from `history_start`. Used after entry approval or manual recalculate. |
| `balance.project` | Stage 7 only — no snapshot changes. Unaffected. |

---

## 13. Edge Cases

**First import (no snapshots exist):** `existing_snapshots` is empty. Every `(entry, date)` is a gap-fill dirty pair. `crawl_start = history_start`. Full history computed in one pass.

**New account added (overlapping history):** The new account's transactions are imported via `import.process`. Stage 1 assigns them to entries. Those entries get `dirty_from = entry.start_date` (no prior snapshots for the new account's data). The crawl extends back to cover the new data.

**Entry with intentional gap (service stopped and restarted):** Snapshots are computed through the gap as-is. The gap shows up in reporting as a period of absent data. Entry lifecycle management (setting `end_date`, creating a new entry under the same label) is user-driven and out of scope for this design.

**Ended entry with complete snapshots:** No touch sources fire. No dirty pairs. Skipped entirely.

**Entry config change on multi-year entry:** Source C fires with `dirty_from = entry.start_date`. Full history for that entry is re-run. Accepted cost — it is one entry, not the entire entity.
