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
| `dom:N` | Day of month, 1-indexed from start. N = 1–31. | `dom:1` — 1st; `dom:15` — 15th |
| `dom:-N` | Day of month, 1-indexed from end. `-1` = last day. | `dom:-1` — last day of month |
| `dom:N,M,...` | Multi-anchor: multiple phase points per month. Mix positive and negative. | `dom:15,-1` — 15th and last day (semi-monthly) |
| `dow:N` | Day of week. 0=Monday … 6=Sunday. | `dow:4` — every Friday |
| `interval:N` | Every N days from last fire date. | `interval:14` — biweekly |

**Index convention:** Both positive and negative indices are 1-based. `dom:1` is the 1st, `dom:-1` is the last day. There is no `dom:0`.

**Negative index resolution:** For a given year/month with `D = days_in_month(year, month)`:

```text
resolved_day(N) = N > 0 ? N : D + N + 1

dom:-1  → D + (-1) + 1 = D        // last day
dom:-2  → D + (-2) + 1 = D - 1    // second-to-last
```

**UI translation:** The engine stores and reads the raw index. The UI translates for display:

- Positive: `1` → "1st", `15` → "15th", `28` → "28th"
- Negative: `-1` → "last" (v1 only; `-2`, `-3`, etc. displayed as ordinal from end in future)

**Quarterly and annual rules:** Use `interval:91` and `interval:365`. Do not use `dom:N` with large `period_days` — `dom:` is for within-month phase anchors only.

**Invoice-triggered income (net-30, net-15, etc.):** Model as `variable` with no `recurrence_anchor`. Invoice payments are event-driven, not calendar-phase-driven. They contribute to the rolling rate baseline but are excluded from Stage 7 projection.

Multi-anchor rules (e.g., `dom:15,-1`) allow a single income rule to represent semi-monthly pay without splitting into two separate rules. Stage 7 expands each anchor into individual scheduled dates within the projection window and computes the effective window duration between consecutive firings.

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

// Pre-expand all dom: anchors into concrete fire dates before the day loop.
// For each dom: rule, build a sorted list of all fire dates in the projection window.
// For interval: rules, fire dates are computed on-the-fly from next_due_date + N.
for each rule r with recurrence_anchor:
    if r.recurrence_anchor starts with "dom:":
        r.fire_dates = expand_dom_anchors(r.recurrence_anchor, computed_as_of, computed_as_of + 90)
        // effective_period_days(fire) = next fire date after fire - fire (variable per firing)
        // this eliminates false pinch points caused by fixed-width windows across unequal anchor gaps

for each day D in [computed_as_of .. computed_as_of + 90]:

    // Sum instantaneous rate contributions for day D
    income_rate_D = Σ { rate_on_day(r, D)
                        for income rules r whose signal is active on D }

    commitment_rate_D = Σ { rate_on_day(r, D)
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
    if r.recurrence_anchor starts with "dom:":
        // Advance to the next calendar anchor after the last fire date.
        // Do NOT add period_days — dom: rules have variable inter-firing gaps.
        r.next_due_date = next_anchor_date_after(last_fire_date(r), r.recurrence_anchor)
    else:  // interval:N
        r.next_due_date = last_projected_fire_date(r) + N
```

**`rate_on_day(r, D)`** — how a rule contributes its rate on day D:

- `dom:` rules: find the fire date `F` in `r.fire_dates` such that `F <= D < next_fire_date_after(F)`. If found, rate = `r.amount_cents / effective_period_days` where `effective_period_days = next_fire_date_after(F) - F`. This uses the actual inter-firing gap rather than the stored `period_days`, eliminating false pinch points from unequal anchor spacing (e.g., the 15th→last gap varies by month).
- `interval:N` rules: signal is active on D if `next_due_date <= D < next_due_date + r.period_days`. Rate = `r.amount_cents / r.period_days`.
- `dow:N` rules: signal is active on D if `day_of_week(D) == N`. Rate = `r.amount_cents / 7`.

**"A rule's signal is active on day D"** means D falls within one of the rule's activation windows as defined above. For standing/variable rules, the engine expands `recurrence_anchor` into a list of fire dates within the 90-day window and checks each.

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

`drift_per_day` in `computed_snapshots` uses an all-or-nothing direction check: ALL-expense nodes use `projected_rate - actual_rate`; any node containing income uses `actual_rate - projected_rate`. Positive drift always means financially ahead of projection. Defined and computed in Stage 5; see the processing engine spec for the full formula.

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
