# Review Queue Migration: Merge into Entries

**Status:** Planned — UI bridge in place, engine migration deferred  
**Date:** 2026-07-16

## Problem

`review_queue` is a satellite table that duplicates lifecycle state already modelled by `entries.status`. The engine writes detection metadata there; the API joins both tables; the frontend needed two endpoints. The split exists for historical reasons (review started as a separate inbox), but with the Ledger page combining them, the separation is purely accretive complexity.

## Goal

`GET /entries?status=all` is the single source of truth for the Ledger. Every entry — whether pending the user's first decision or already active — is one row with all its data. The `review_queue` table becomes an engine implementation detail that is eventually dropped.

## Fields to Migrate

`review_queue` columns that have no counterpart on `entries`:

| Column | Type | Purpose |
|---|---|---|
| `alert_type` | `TEXT` CHECK `(new, drift, ended)` | What kind of engine event triggered this |
| `confidence` | `NUMERIC(4,3)` | Overall engine confidence |
| `merchant_confidence` | `NUMERIC(4,3)` | Per-dimension score |
| `timing_confidence` | `NUMERIC(4,3)` | Per-dimension score |
| `amount_confidence` | `NUMERIC(4,3)` | Per-dimension score |
| `suggested_name` | `TEXT` | Engine's label suggestion |
| `suggested_entry_type` | `TEXT` | Engine's type suggestion |
| `suggested_conditions` | `JSONB` | Engine's condition suggestion |
| `suggested_rate_per_day` | `NUMERIC(12,4)` | Engine's rate suggestion |
| `sample_merchants` | `TEXT[]` | Merchants that triggered detection |
| `matched_transaction_count` | `INTEGER` | Transactions seen |
| `job_id` | `UUID` | Processing job that detected this |
| `reviewed_by` | `UUID` | Audit: who acted |
| `reviewed_at` | `TIMESTAMPTZ` | Audit: when acted |

`suggested_*` fields are temporary — they hold the engine's proposal until the user approves (copies them to the canonical entry fields) or rejects (discards them).

## Migration Steps

### 1. Schema migration (Go service)

Add columns to `entries`:

```sql
ALTER TABLE entries
  ADD COLUMN alert_type                TEXT CHECK (alert_type IN ('new', 'drift', 'ended')),
  ADD COLUMN confidence                NUMERIC(4,3),
  ADD COLUMN merchant_confidence       NUMERIC(4,3),
  ADD COLUMN timing_confidence         NUMERIC(4,3),
  ADD COLUMN amount_confidence         NUMERIC(4,3),
  ADD COLUMN suggested_name            TEXT,
  ADD COLUMN suggested_entry_type      TEXT,
  ADD COLUMN suggested_conditions      JSONB,
  ADD COLUMN suggested_rate_per_day    NUMERIC(12,4),
  ADD COLUMN sample_merchants          TEXT[],
  ADD COLUMN matched_transaction_count INTEGER,
  ADD COLUMN review_job_id             UUID REFERENCES processing_jobs(id),
  ADD COLUMN reviewed_by               UUID REFERENCES users(id),
  ADD COLUMN reviewed_at               TIMESTAMPTZ;
```

Backfill from `review_queue` (latest pending item per entry):

```sql
UPDATE entries e
SET
  alert_type                = rq.alert_type,
  confidence                = rq.confidence,
  merchant_confidence       = rq.merchant_confidence,
  timing_confidence         = rq.timing_confidence,
  amount_confidence         = rq.amount_confidence,
  suggested_name            = rq.suggested_name,
  suggested_entry_type      = rq.suggested_entry_type,
  suggested_conditions      = rq.suggested_conditions,
  suggested_rate_per_day    = rq.suggested_rate_per_day,
  sample_merchants          = rq.sample_merchants,
  matched_transaction_count = rq.matched_transaction_count,
  review_job_id             = rq.job_id,
  reviewed_by               = rq.reviewed_by,
  reviewed_at               = rq.reviewed_at
FROM (
  SELECT DISTINCT ON (entry_id)
    entry_id, alert_type, confidence, merchant_confidence, timing_confidence,
    amount_confidence, suggested_name, suggested_entry_type, suggested_conditions,
    suggested_rate_per_day, sample_merchants, matched_transaction_count,
    job_id, reviewed_by, reviewed_at
  FROM review_queue
  ORDER BY entry_id, created_at DESC
) rq
WHERE e.id = rq.entry_id;
```

### 2. Engine update (Rust service)

The Rust engine currently writes to `review_queue`. Change it to write directly to `entries`:

- On new pattern detection: `INSERT INTO entries (..., alert_type, confidence, suggested_*, status) VALUES (..., 'new', ..., 'pending_review')`
- On drift detection: `UPDATE entries SET alert_type='drift', confidence=..., reviewed_at=NULL WHERE id=...`
- On end detection: `UPDATE entries SET alert_type='ended' WHERE id=...`

The engine's `job_id` goes into `entries.review_job_id`.

### 3. API changes (Go service)

- `store/entries.go`: remove the `LEFT JOIN LATERAL review_queue` bridge (added as a temporary measure); the columns are now native to entries
- `handler/entries.go`: `toEntryView` maps the new native fields directly
- `handler/review.go`: delete the file — all review operations now go through `/entries/{id}/approve` and `/entries/{id}/reject`
- `store/review.go`: delete — no longer needed
- `handler/entries.go` `ApproveEntryReview` / `RejectEntryReview`: these store methods stop touching `review_queue` and instead directly update `entries` columns (`alert_type = NULL`, `reviewed_by`, `reviewed_at`)

### 4. Approve/reject semantics post-migration

**Approve:**
- `alert_type = 'new'`: copy `suggested_conditions` → `conditions`, `suggested_entry_type` → `entry_type`, set `status = 'active'`, clear `suggested_*`, set `reviewed_at/by`, trigger `account.analyze` job
- `alert_type = 'drift'`: update `conditions`/`entry_type` if user accepted suggestions, clear `alert_type`, set `reviewed_at/by`
- `alert_type = 'ended'`: set `status = 'inactive'`, `end_date = NOW()`, clear `alert_type`

**Reject:**
- `alert_type = 'new'`: set `status = 'inactive'`, clear `suggested_*`
- `alert_type = 'drift'` or `'ended'`: clear `alert_type` and `suggested_*` (keep entry as-is)

### 5. Drop review_queue

After the Rust engine no longer writes to `review_queue` and all rows are confirmed migrated:

```sql
DROP TABLE review_queue;
```

## Current Bridge (in place)

Until this migration is done:

- `store/entries.go` `ApproveEntryReview` / `RejectEntryReview`: look up `review_queue` by `entry_id` internally, drive entry state transitions, and mark the review item as approved/rejected — the frontend never touches `review_queue` IDs
- `GET /entries?status=all` returns entries without the `suggested_*` / confidence fields (those remain in `review_queue`); the Ledger UI renders with whatever is available and gains the full fields once the migration lands
- `GET /review` endpoint remains functional for the engine but is no longer consumed by the frontend

## Impact on Frontend

Zero changes required when the migration lands. The `EntryView` type will gain nullable `alert_type`, `confidence`, `suggested_*` etc. fields. The Ledger can start rendering confidence scores and suggested values for `pending_review` rows once the data is there.
