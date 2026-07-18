# Canonical Merchants Design

**Date:** 2026-07-18
**Status:** Approved for implementation

## Problem

`normalize_merchant()` is a noise-cleaning function (strips punctuation, collapses whitespace, title-cases). It makes no semantic judgment. As a result, "Netflixcom", "Netflix LLC", and "Netflix Service" are treated as distinct merchants throughout the pipeline, causing fragmented entries and unreliable rate calculations.

`extract_brand()` in Stage 2 was an attempt at brand-level normalization via store-number stripping, but it is too brittle to be reliable and does not cover the general case.

The canonical merchant layer replaces both with a persistent, learnable mapping from normalized strings to a single canonical identity.

## Goals

- Map multiple normalized merchant names to a single canonical merchant identity
- Feed that identity into the entry conditions system so entries match broadly and consistently
- Allow users to pre-seed, correct, merge, and split canonical merchants via the config page
- Surface changes through the existing entry lifecycle (pending\_review, sample\_merchants) — no new notification system

## Non-Goals

- Entity-scoped canonical merchants — canonical merchants are global, like labels
- Storing canonical\_merchant\_id as a FK on transactions — canonical resolution is a condition ingredient, not a transaction annotation
- Replacing the dedup logic in Stage 0 — dedup continues to operate on raw `merchant_normalized`

## Schema

Two new global tables:

```sql
CREATE TABLE canonical_merchants (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL,
    source     TEXT        NOT NULL DEFAULT 'engine',  -- 'engine' | 'user'
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (name)
);

CREATE TABLE canonical_merchant_aliases (
    normalized_name       TEXT        PRIMARY KEY,
    canonical_merchant_id UUID        NOT NULL REFERENCES canonical_merchants(id) ON DELETE CASCADE,
    source                TEXT        NOT NULL DEFAULT 'engine',  -- 'engine' | 'user'
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

No FK is added to `transactions`. Canonical merchant identity lives in the conditions system and is resolved at match time by Stage 1 via a pre-loaded alias map.

## Stage 0 Changes

Order of operations within Stage 0 is unchanged for dedup. Canonical resolution runs **after** dedup classification, only for candidates that will be inserted or superseded.

```text
parse CSV → candidates
  → dedup passes 1–5 (unchanged, operates on merchant_normalized)
  → for Insert + Supersede only: resolve_canonical(merchant_normalized)
  → batch_insert
```

`resolve_canonical(normalized_name)` logic (runs once per batch against a pre-loaded canonical snapshot):

1. Exact lookup in `canonical_merchant_aliases`
2. On miss: LCS ratio ≥ 0.70 against all canonical merchant names
3. Hit → persist new alias (source = 'engine'), return existing canonical\_merchant\_id
4. Miss → INSERT new canonical merchant (source = 'engine'), INSERT first alias, return new id

The canonical snapshot (all names + aliases) is loaded once at Stage 0 start, shared across the concurrent dedup + post-dedup resolution steps. Skipped transactions already have a canonical from their original import and are not re-resolved.

The resolved `canonical_merchant_id` is not stored on the transaction row. Stage 2 groups by loading `canonical_merchant_aliases` at its start and doing exact lookups against each unmatched transaction's `merchant_normalized` — the alias table was just updated by Stage 0, so all new aliases are immediately available.

## New Condition Type

A new leaf node added to the entry conditions JSONB schema:

```json
{ "type": "canonical_merchant", "canonical_merchant_id": "uuid" }
```

**Stage 1 evaluation:**

At stage start, load all `canonical_merchant_aliases` into a `HashMap<Uuid, HashSet<String>>` keyed by `canonical_merchant_id`. Evaluation of the `canonical_merchant` leaf is:

```text
aliases_for(canonical_merchant_id).contains(txn.merchant_normalized)
```

O(1) hash lookup — no fuzzy matching at match time. Fuzzy is only used at canonical resolution time (Stage 0) when a new normalized name first appears.

**Compiled condition node added to `CompiledConditionTree`:**

```rust
CanonicalMerchant(Uuid)
```

## Stage 2 Changes

Clustering is replaced by grouping on the canonical\_merchant\_id resolved in Stage 0.

**Before:** O(n²) LCS clustering over `merchant_normalized` using `extract_brand()` + `lcs_ratio()`.

**After:** Group unmatched transactions by canonical\_merchant\_id (resolved in Stage 0). Each group becomes one candidate cluster passed to `score_cluster()`.

Generated `pending_review` entries use `canonical_merchant` conditions instead of `imported_payee_contains`:

```json
{
  "op": "AND",
  "children": [
    { "type": "canonical_merchant", "canonical_merchant_id": "uuid" }
  ]
}
```

`extract_brand()` is removed.

## Entry Lifecycle Integration

A new canonical merchant does **not** imply a new entry. The sequence is:

1. Stage 0 resolves canonical (possibly creating a new one)
2. Stage 1 evaluates all active entry conditions against all transactions — if an existing entry's conditions already cover the new canonical's transactions, they are matched and no new entry is needed
3. Stage 2 only creates `pending_review` entries for transactions that remain unmatched after Stage 1

Entry state changes for canonical-related events:

| Event | Entry effect |
| --- | --- |
| New canonical merchant, no existing entry | Stage 2 creates `pending_review` entry, `alert_type = 'new'` |
| New canonical merchant, existing entry covers it | Stage 1 picks up transactions, `sample_merchants` updated on re-run |
| New alias added to existing canonical | Stage 1 picks up new transactions under the existing entry naturally |
| User merges two canonicals | `entries.reprocess` re-evaluates all entries referencing either canonical |
| User splits a canonical | `entries.reprocess` re-evaluates all entries referencing the affected canonical |

The `sample_merchants` array on entries is updated by Stage 2 on each run to reflect the current set of normalized names seen under the canonical.

## API Endpoints

All under `/api/canonical-merchants`:

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/canonical-merchants` | List all with alias count, transaction count, source |
| POST | `/api/canonical-merchants` | Create (user-initiated, source = 'user') |
| PUT | `/api/canonical-merchants/:id` | Rename |
| DELETE | `/api/canonical-merchants/:id` | Delete (cascades aliases; triggers entries.reprocess) |
| GET | `/api/canonical-merchants/:id/aliases` | List aliases for a canonical |
| POST | `/api/canonical-merchants/:id/aliases` | Add alias manually |
| DELETE | `/api/canonical-merchants/:id/aliases/:normalized_name` | Remove alias |
| POST | `/api/canonical-merchants/:id/merge/:other_id` | Merge other into this (moves all aliases, deletes other, triggers entries.reprocess) |
| POST | `/api/canonical-merchants/:id/split` | Body: `{ "aliases": [...] }` — moves listed aliases to a new canonical, triggers entries.reprocess |

## Config Page

New **Merchants** tab added to the configuration page, between Labels and Institution Mappings.

**List view:** Table of canonical merchants showing canonical name, alias count, source badge (engine/user). Click row to expand aliases inline.

**Actions per canonical:**

- Rename (inline edit)
- Add alias (inline input)
- Remove alias (per alias row)
- Merge into another (select target from dropdown)
- Split (select aliases to move, name the new canonical)
- Delete

**Create new:** "New merchant" button at top opens an inline row for name entry. User can then add aliases immediately after creation.

Merge and split actions show a confirmation step noting that an `entries.reprocess` job will run.

## Removals

- `extract_brand()` in `stage2.rs` — removed entirely
- `imported_payee_contains` conditions in Stage 2-generated entries — replaced by `canonical_merchant` conditions
- LCS clustering loop in Stage 2 — replaced by canonical grouping

## Migration

1. Add `canonical_merchants` and `canonical_merchant_aliases` tables
2. No changes to existing `entries`, `transactions`, or `labels` tables
3. On first run after deployment, Stage 0 will auto-populate canonical merchants from new imports; existing entries retain their current `imported_payee_contains` conditions and continue to work — they are not migrated automatically
4. Users can create canonicals manually via config page and update entry conditions to use them over time, or wait for the engine to build the canonical list through normal import runs
