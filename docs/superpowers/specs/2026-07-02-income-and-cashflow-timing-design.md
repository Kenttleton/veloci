# Veloci — Income & Cash Flow Timing Design

**Date:** 2026-07-02
**Status:** Draft
**Scope:** Income rule modeling, pay schedule phase encoding, signal superposition in Stage 7, second derivative

---

## 1. Mental Model: Overlaid Rate Signals

Veloci models financial life as a system of overlaid rate signals. Each rule contributes a signal with four properties:

| Property | Meaning | Source |
| --- | --- | --- |
| **Amplitude** | Magnitude of the rate in $/day (`amount_cents / period_days`) | Rule amount + period_days |
| **Period** | Recurrence cycle in days | `period_days` |
| **Phase** | When in the cycle the signal fires | `recurrence_anchor` |
| **Duration** | How long the signal remains active | `period_days` (amortization window) |

The margin rate shown in the Horizon graph is the instantaneous superposition of all active signals. A user might have an income signal of +$71/day (biweekly $1000 paycheck, 14-day period) and a rent signal of -$50/day (monthly $1500, 30-day period) — net +$21/day margin. But if those signals are 15 days out of phase, there is a window each month where the commitment signal fires before enough income signal has accumulated.

"Will I make rent?" is not a balance check. It is a phase alignment check: at the phase offset where the rent signal fires, has the income signal accumulated sufficient margin to cover it?

Stage 7 is the signal superposition engine that makes this visible.

### Rates are primary; balance is derived

Veloci's computation is entirely rate-based. However, users' bank accounts show balances — not rates. Stage 7 therefore outputs both:

1. **Rate columns** — the instantaneous margin rate on each projected day (the Veloci-native view)
2. **Projected balance** — the running integral of the margin rate signal from the current account balance (the bank account comparison view)

The projected balance is a convenience output derived from the rate signals. It does not drive the computation.

---

## 2. Income Rule Types

Income rules use the same entry type taxonomy as expense rules. Income direction is valid for:

| Entry type | Income meaning | Rate computation |
| --- | --- | --- |
| `standing` | Regular paycheck, direct deposit, salary | `amount_cents / period_days` per day |
| `variable` | Freelance, tips, commission | Rolling median or max over `period_days` |
| `boost` | Tax refund, bonus, one-time windfall | `amount_cents / period_days` amortized over declared window |

`hit` is **expense direction only** — it models short-term debt (unplanned expense amortized as a temporary rate drag). It is never an income type.

All income rules carry `direction = 'income'`. They flow through Stages 1–6 identically to expense rules. Sign convention: `positive amount_cents = inflow`. The Margin label sees the net: `income_rate - commitments_rate`.

---

## 3. Schedule Fields

The rate tells you amplitude and period. The schedule tells you phase. Both are required to answer "will I make it?"

### New columns on `rules`

```sql
ALTER TABLE rules ADD COLUMN next_due_date     DATE;
ALTER TABLE rules ADD COLUMN recurrence_anchor TEXT;
```

### `recurrence_anchor` — phase encoding (engine input)

`recurrence_anchor` encodes the phase of the signal. It is used by Stage 7 to place events on the future timeline, and by Stage 2 to advance `next_due_date` after each matched transaction. It is **not** a UI display hint — it is the authoritative phase descriptor for the scheduling engine.

Encoding format:

| Pattern | Meaning | Example |
| --- | --- | --- |
| `dom:N` | Day of month N | `dom:1` — fires 1st of each month |
| `dom:N,M` | Multi-anchor: days N and M each month | `dom:1,15` — semi-monthly (single rule, two phase points) |
| `dow:N` | Day of week N (0=Monday … 6=Sunday) | `dow:4` — every Friday |
| `interval:N` | Every N days from last occurrence | `interval:14` — biweekly from last fire |

Multi-anchor rules (e.g., `dom:1,15`) allow a single income rule to represent semi-monthly pay without splitting into two separate rules. Stage 7 expands the anchors into individual scheduled dates within the projection window.

### `next_due_date` — next scheduled phase occurrence

The concrete date this rule's signal next fires. Stage 7 uses this as the starting point for advancing the timeline.

**How it is set:**

1. **User-set** — entered in the UI when creating or editing a rule. Highest priority.
2. **Engine-inferred** — Stage 2 writes `next_due_date = last_transaction_date + median_interval` after each matched transaction. Only overwrites if user has not set it.
3. **NULL** — rule has no schedule. Contributes to the rate baseline but is excluded from phase analysis.

### Index

```sql
CREATE INDEX ON rules (entity_id, next_due_date);
```

---

## 4. Stage 7: Signal Superposition Engine

Stage 7 projects all rate signals forward 90 days. For each day in the projection window, it sums the active rate contributions from all rules based on their current phase positions.

### Inputs

- All `rules` for the entity with `status = 'active'`
- `recurrence_anchor` and `next_due_date` for phase placement
- `amount_cents` and `period_days` for amplitude computation
- `accounts.balance_cents` — the current account balance, frozen at last import, used only as the starting point for the derived projected balance column

### Algorithm

```text
projection_window = 90 days
balance = accounts.balance_cents  // starting point for derived balance only

for each day D in [today .. today + 90]:

    // Sum instantaneous rate contributions for day D
    income_rate_D = Σ { r.amount_cents / r.period_days
                        for income rules r whose signal is active on D }

    commitment_rate_D = Σ { r.amount_cents / r.period_days
                            for commitment rules r whose signal is active on D }

    margin_rate_D = income_rate_D - commitment_rate_D

    // Derived balance: running integral of margin rate (bank account comparison)
    balance = balance + margin_rate_D

    is_pinch_point = (margin_rate_D < 0)

    write rate_projections row:
      projected_date          = D
      income_rate_per_day     = income_rate_D
      commitment_rate_per_day = commitment_rate_D
      margin_rate_per_day     = margin_rate_D
      projected_balance_cents = balance     // derived, for user reference only
      is_pinch_point          = is_pinch_point

// Advance next_due_date for rules that fired during projection.
// Written in a staged pass AFTER the full projection is complete — not in-loop —
// to keep the projection output deterministic and reproducible from the same inputs.
for each rule r with recurrence_anchor:
    r.next_due_date = last_projected_fire_date(r) + r.period_days
```

**"A rule's signal is active on day D"** means D falls within the current activation window: `[next_fire_date, next_fire_date + period_days)`. For standing/variable rules, the engine expands `recurrence_anchor` into a list of fire dates within the 90-day window and checks each.

### Determinism

Stage 7 is fully deterministic. `next_due_date` is a stored field — not derived from `NOW()`. The starting balance is frozen at import time. Two runs on the same inputs produce identical output.

### `balance.project` job type

The `'balance.project'` job type is already in the `processing_jobs.job_type` CHECK constraint (added in M7). It triggers Stage 7 in isolation — for example when a user updates a `next_due_date` or `recurrence_anchor` without re-importing transactions.

---

## 5. The Second Derivative: Margin Rate Over Time

The **first derivative** is the margin rate: `income_rate - commitments_rate` in $/day. It answers "am I ahead or behind right now?"

The **second derivative** is the rate of change of the margin rate over time. It answers "is my position improving or worsening?"

This is already captured. The Margin label's `slope_per_day` in `computed_snapshots` (written by Stage 5) is exactly this value — the slope of the margin rate across the last N snapshots. No new field or computation is needed.

The API surfaces it as:
- **Margin rate** — current $/day gap. Primary number on the Margin label.
- **Margin slope** (`slope_per_day` on the Margin snapshot) — trend direction and magnitude. "Improving at +$0.15/day" or "tightening at -$0.40/day."
- **Margin on due date** — the `margin_rate_per_day` value from `rate_projections` on the due date. The signal-native answer to "will I make rent?"
- **Projected balance on due date** — the `projected_balance_cents` value on the due date, for users who want to compare against their bank app.

### `drift_per_day`

`drift_per_day` in `computed_snapshots` is `projected_rate - actual_rate` — the historical accuracy signal showing how far the actual rate deviated from what was projected at the prior snapshot. This is already defined and computed in Stage 5/6. It is not a new concept in this spec.

---

## 6. Pinch Points

A pinch point is a day where `margin_rate_per_day < 0` — commitment signals are firing faster than income signals are arriving at that phase offset. It is expressed entirely in rate terms.

Stage 7 sets `is_pinch_point = TRUE` on any row where `margin_rate_per_day < 0`. The API can characterize runs of consecutive pinch-point days into a contiguous gap event (start date, duration, deepest negative margin rate) for display.

There are no balance-threshold tiers. Severity is the magnitude of the negative margin rate and the duration of the gap — not a comparison to a dollar buffer.

---

## 7. Schema Changes

### rules table

```sql
ALTER TABLE rules ADD COLUMN next_due_date     DATE;
ALTER TABLE rules ADD COLUMN recurrence_anchor TEXT;

CREATE INDEX ON rules (entity_id, next_due_date);
```

### rate_projections table (replaces balance_projections from M7)

The `balance_projections` table defined in M7 is superseded by `rate_projections`. The migration should drop `balance_projections` and create `rate_projections` in its place.

```sql
DROP TABLE IF EXISTS balance_projections;

CREATE TABLE rate_projections (
  id                       UUID           PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id                UUID           NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  account_id               UUID           REFERENCES accounts(id),
  job_id                   UUID           NOT NULL REFERENCES processing_jobs(id),
  projected_date           DATE           NOT NULL,
  income_rate_per_day      NUMERIC(12,4)  NOT NULL DEFAULT 0,
  commitment_rate_per_day  NUMERIC(12,4)  NOT NULL DEFAULT 0,
  margin_rate_per_day      NUMERIC(12,4)  NOT NULL,
  projected_balance_cents  BIGINT         NOT NULL,
  is_pinch_point           BOOLEAN        NOT NULL DEFAULT FALSE,
  UNIQUE (entity_id, account_id, job_id, projected_date)
);

CREATE INDEX ON rate_projections (entity_id, account_id, projected_date);
```

`account_id IS NULL` rows are the entity-aggregate projection. Per-account rows (for passive accounts) project each independent mini-budget separately.

`NUMERIC(12,4)` for rate columns matches the precision convention in `computed_snapshots`.

---

## 8. Out of Scope

- Interest modeling (HYSA yield, CC interest, investment returns) — deferred to v1.1
- Variable income range estimation (confidence intervals on freelance income) — v1.1
- Multi-account coverage strategies (which account covers which bill) — v1.1
- Automatic `next_due_date` inference for rules with no transaction history — v1.1
