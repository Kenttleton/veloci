# Implementation: System Entries, Unified Rate, Status Rename

## Overview

Three related changes shipped together:

1. **Status rename** — entry status values renamed: `pending_review` → `pending`, `active` → `live`, `inactive` → `ended`
2. **Unified rolling window rate** — Stage 3 rate calculation replaces standing/variable/irregular branch logic with a single formula
3. **System entries** — Income and Spend engine-managed entries created per entity; `entity_config` table stores the configurable window width

---

## 1. Schema Changes

### Migration (append to `migrations/app/002_financial_schema.sql`)

**entries table:**
- Add `scope TEXT CHECK (scope IN ('system'))` column
- Update status CHECK: `('pending_review', 'active', 'inactive')` → `('pending', 'live', 'ended')`
- Update existing data: `UPDATE entries SET status = 'live' WHERE status = 'active'`, etc.

**entity_config table (new):**
```sql
CREATE TABLE entity_config (
  entity_id          UUID        PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
  system_window_days INTEGER     NOT NULL DEFAULT 90,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Affected CHECK constraints in entries
```sql
-- old
CHECK (status IN ('pending_review', 'active', 'inactive'))

-- new
CHECK (status IN ('pending', 'live', 'ended'))
```

---

## 2. Status Rename — Codebase Touchpoints

### Rust engine (`services/engine/src/pipeline/`)

| File | Change |
| --- | --- |
| `stage1.rs` | `status = 'active'` → `status = 'live'` in all SQL |
| `stage2.rs` | `status = 'pending_review'` → `status = 'pending'`; `status = 'active'` → `status = 'live'` |
| `stage3.rs` | `status = 'active'` → `status = 'live'` |
| `stage7.rs` | `status = 'active'` and `status = 'pending_review'` → updated values; `alert_type = 'ended'` + `status = 'pending'` on missed entries |
| `types.rs` | Any status string constants |

### Go API (`services/web/`)

| File | Change |
| --- | --- |
| `store/entries.go` | All status literals in SQL and Go constants |
| `handler/entries.go` | Status values in request/response handling |
| `page/*.templ` | Any status display logic referencing old values |

---

## 3. Unified Rolling Window Rate (Stage 3)

### Current behaviour (to remove)
Stage 3 has three branches:
- Standing: `amount / period_days` for most recent transaction
- Variable: rolling window total / period_days, using `avg` or `max`
- Irregular: most recent amount / period_days

### New behaviour
Single formula for all entry types:

```
actual_rate_per_day(t) = rolling_window_total_cents(t) / W
```

Where:
- `rolling_window_total_cents(t)` = sum of `|amount_cents|` for all assigned transactions with `date ∈ [t − W, t]` and `date ≥ entry.start_date`
- `W` = `entry.period_days` for named entries (`scope IS NULL`)
- `W` = `entity_config.system_window_days` for system entries (`scope = 'system'`)

`variable_method` (`avg` / `max`) is removed from the rate calculation. The rolling window naturally averages variable amounts. If `variable_method = 'max'` behaviour is needed it can be added as a future flag but is not part of this implementation.

### Stage 3 changes in Rust

1. Remove the `entry_type` branch in `compute_entry_rate()`
2. Load `entity_config.system_window_days` once at stage start for system entries
3. For each entry: `W = if entry.scope == "system" { system_window } else { entry.period_days }`
4. `rolling_window_total_cents` = sum of assigned transactions in `[snapshot_date - W, snapshot_date]`
5. `actual_rate_per_day` = `rolling_window_total_cents as f64 / W as f64 / 100.0` (cents to dollars)

---

## 4. System Entries

### `EnsureSystemData` (replaces / extends `EnsureSystemLabels`)

Called in `syncAdminUser` after entity is confirmed. Idempotent — safe on every startup.

```go
func (s *Store) EnsureSystemData(ctx context.Context, entityID string) error {
    // 1. Upsert system labels (existing logic)
    // 2. Upsert entity_config row with defaults
    // 3. Upsert Income and Spend system entries
}
```

System entry upsert uses `ON CONFLICT (entity_id, label_id) WHERE scope = 'system' DO NOTHING` — or a named unique constraint on `(entity_id, scope, direction)` for system entries.

### Income and Spend entry properties

| Field | Income | Spend |
| --- | --- | --- |
| `label_id` | Income system label ID | Spend system label ID |
| `direction` | `income` | `spend` |
| `entry_type` | `irregular` | `irregular` |
| `scope` | `system` | `system` |
| `period_days` | NULL (uses system_window_days) | NULL |
| `conditions` | `{"entry_direction": "income"}` | `{"entry_direction": "spend"}` |
| `status` | `live` | `live` |
| `source` | `engine` | `engine` |
| `priority` | 0 (lowest — matches everything of direction) | 0 |
| `start_date` | entity's earliest transaction date, or today | same |

### Protecting system entries

- `DeleteEntry`: check `scope = 'system'` → return `ErrSystemEntry` → HTTP 403
- `UpdateEntry`: same guard
- These entries never appear in the Ledger pending review queue (filtered by `scope IS DISTINCT FROM 'system'`)

---

## 5. Entity Config — Store and Handler

### Store (`services/web/store/entity_config.go`)

```go
type EntityConfig struct {
    EntityID          string    `db:"entity_id"`
    SystemWindowDays  int       `db:"system_window_days"`
    CreatedAt         time.Time `db:"created_at"`
    UpdatedAt         time.Time `db:"updated_at"`
}

func (s *Store) GetEntityConfig(ctx, entityID) (EntityConfig, error)
func (s *Store) UpdateEntityConfig(ctx, entityID string, systemWindowDays int) (EntityConfig, error)
func (s *Store) EnsureEntityConfig(ctx, entityID string) error  // upsert with defaults
```

### Handler

New endpoints under `/api/entity/config`:
- `GET /api/entity/config` — returns current config
- `PUT /api/entity/config` — updates `system_window_days`

Requires `entity:configure` permission (already exists in RBAC).

### Configuration page

Add a "System rate window" field to the existing configuration page (`/configuration`). Shows current `system_window_days` value, allows integer input, submits to `PUT /api/entity/config`. Display hint: "How many days of transaction history to use when calculating your total Income and Spend rates. Default: 90 days."

---

## 6. Stage 1 — `entry_direction` Condition for System Entries

System entries use `{"entry_direction": "income"}` or `{"entry_direction": "spend"}` as their conditions. Stage 1 must evaluate this condition type:

- `entry_direction: "income"` → matches transactions where `amount_cents > 0`
- `entry_direction: "spend"` → matches transactions where `amount_cents < 0`

This condition type may already be implemented. Verify in `stage1.rs` condition evaluator and add if missing.

---

## 7. Delivery Order

1. Migration (schema changes, status data migration)
2. Status rename throughout Rust engine and Go codebase
3. Unified rolling window in Stage 3 (replace branch logic)
4. `entity_config` store + `EnsureEntityConfig`
5. `EnsureSystemData` (labels + entries)
6. System entry protection in handlers
7. `entry_direction` condition type in Stage 1 (verify/add)
8. Entity config API endpoints
9. Configuration page UI addition
10. Compile and test both services
