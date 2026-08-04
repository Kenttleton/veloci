# Entry Type Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse three entry types (`standing`, `variable`, `irregular`) into two by repurposing existing names: `standing` covers all recurring entries (strict cadence + timing, any amount — was standing + variable), `variable` covers all irregular entries (no cadence or timing pattern — was irregular).

**Architecture:** The old `variable` engine code path already handles ranges and becomes the sole path for new `standing`. The old `irregular` path becomes new `variable`. The old `standing`-specific gates are removed. `variable_method` is renamed to `rate_method` with values `median`/`max`; the engine already seeds entries with median math (stage2 uses `median_amount_cents`), and stage3 is wired up to branch on `rate_method` for the projected rate. Changing `rate_method` on a live entry dispatches `account.analyze` to recompute.

**Tech Stack:** Go 1.26, Rust (sqlx), PostgreSQL, Templ, plain JS (CodeMirror 6)

## Global Constraints

- `entry_type` CHECK must become `('standing', 'variable')` — `irregular` is no longer valid
- `variable_method` (`avg` / `max`) is UNCHANGED — it drives projected_rate_per_day for all standing entries
- No new columns; no removed columns beyond the CHECK constraint change
- No ALTER TABLE — all schema changes go directly into `migrations/app/002_financial_schema.sql`
- Rebuild the DB from schema after Task 1
- All tests must pass before each commit: `cargo test` in engine, `go test ./...` in services/web

---

## File Map

| File | Change |
|---|---|
| `migrations/app/002_financial_schema.sql` | Narrow `entry_type` CHECK to `('standing', 'variable')` |
| `services/engine/src/pipeline/types.rs` | Remove `Irregular` variant, update `from_str`, fix tests |
| `services/engine/src/pipeline/stage2.rs` | Remove standing gate constants + three-branch → two-branch classification; rename `"irregular"` → `"variable"`; update tests |
| `services/web/store/entries.go` | No struct changes; verify no `"irregular"` literals in queries |
| `services/web/handler/entries.go` | No struct changes; verify no `"irregular"` literals |
| `services/web/page/handler.go` | Remove `"irregular"` case from `entryTypeLabel`; update `TypeFilter` comment |
| `services/web/page/ledger.templ` | Remove Irregular pill; remove Irregular from both form selects |
| `services/web/js-src/conditions-editor.js` | Update `entry_type` autocomplete and lint validation |
| `services/web/store/conditions_test.go` | Update any `"irregular"` test fixtures |
| `docs/veloci-ref.md` | Update scoring table, classification thresholds, entry type table; `variable_method` → `rate_method` |
| `docs/veloci-spec.md` | Update entry type descriptions, schema table; `variable_method` → `rate_method` |
| `docs/conditions-editor.md` | Update `entry_type` enum examples and validation table |
| `docs/impl-system-entries.md` | Update `irregular` and `variable_method` references |
| `services/web/page/glossary.templ` | Update entry type description; `variable_method` → `rate_method` |

---

## Task 1: Schema — narrow entry_type CHECK

**Files:**
- Modify: `migrations/app/002_financial_schema.sql`

**Interfaces:**
- Produces: `entry_type CHECK ('standing', 'variable')` only

- [ ] **Step 1: Update the CHECK constraint**

In `migrations/app/002_financial_schema.sql`, find and update:

```sql
-- Before
  entry_type             TEXT          NOT NULL
                         CHECK (entry_type IN ('standing', 'variable', 'irregular')),
```
```sql
-- After
  entry_type             TEXT          NOT NULL
                         CHECK (entry_type IN ('standing', 'variable')),
```

- [ ] **Step 2: Rebuild the database**

```bash
# From project root — use your project's DB reset command
make db-reset
```

- [ ] **Step 3: Commit**

```bash
git add migrations/app/002_financial_schema.sql
git commit -m "feat: narrow entry_type CHECK to standing/variable"
```

---

## Task 2: Rust — EntryType enum

**Files:**
- Modify: `services/engine/src/pipeline/types.rs`

**Interfaces:**
- Produces: `EntryType` with variants `Standing`, `Variable` only; `from_str("irregular")` returns `None`

- [ ] **Step 1: Update the test first**

Find the `from_str` tests (around line 251):

```rust
// Before
assert_eq!(EntryType::from_str("standing"),  Some(EntryType::Standing));
assert_eq!(EntryType::from_str("variable"),  Some(EntryType::Variable));
assert_eq!(EntryType::from_str("irregular"), Some(EntryType::Irregular));
```
```rust
// After
assert_eq!(EntryType::from_str("standing"),  Some(EntryType::Standing));
assert_eq!(EntryType::from_str("variable"),  Some(EntryType::Variable));
assert_eq!(EntryType::from_str("irregular"), None);
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd services/engine && cargo test types -- --nocapture 2>&1 | grep -E "FAILED|error"
```

Expected: compile error referencing `Irregular`.

- [ ] **Step 3: Remove Irregular variant and update from_str**

```rust
// Before
pub enum EntryType {
    Standing,
    Variable,
    Irregular,
}
// from_str arms:
"standing"  => Some(Self::Standing),
"variable"  => Some(Self::Variable),
"irregular" => Some(Self::Irregular),
```
```rust
// After
pub enum EntryType {
    Standing,
    Variable,
}
// from_str arms:
"standing" => Some(Self::Standing),
"variable" => Some(Self::Variable),
```

- [ ] **Step 4: Run tests**

```bash
cd services/engine && cargo test types
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add services/engine/src/pipeline/types.rs
git commit -m "feat: remove Irregular from EntryType enum"
```

---

## Task 3: Rust — stage2 classification

**Files:**
- Modify: `services/engine/src/pipeline/stage2.rs`

**Interfaces:**
- Consumes: `EntryType` from Task 2 (no `Irregular`)
- Produces: two-branch classification (`standing` / `variable`); no `"irregular"` string anywhere; old standing gate constants removed

- [ ] **Step 1: Update classification tests first**

Find all tests asserting `entry_type == "irregular"` (lines ~1118–1239). Rename test functions and update assertions:

```rust
// fn score_irregular_income_detection
// → fn score_variable_income_detection
assert_eq!(score.entry_type, "variable");

// fn score_irregular_single_transaction_low_confidence
// → fn score_variable_single_transaction_low_confidence
assert_eq!(score.entry_type, "variable");

// fn consistent_amount_irregular_timing_falls_through_to_one_time
// → fn consistent_amount_variable_timing_falls_through
assert_eq!(score.entry_type, "variable");
```

The test `score_regular_transactions_standing` (asserts `entry_type == "standing"`) stays — regular timing still classifies as `standing`.

- [ ] **Step 2: Run to verify the tests fail**

```bash
cd services/engine && cargo test stage2 -- --nocapture 2>&1 | grep -E "FAILED|error"
```

Expected: failures because `"irregular"` still appears in the implementation.

- [ ] **Step 3: Remove standing gate constants**

Find and delete (lines ~54–64):

```rust
// Delete these three lines:
const STANDING_TIMING_GATE: f64 = 0.75;
const STANDING_AMOUNT_GATE: f64 = 0.80;
const STANDING_MIN_OBSERVATIONS: usize = 3;
```

- [ ] **Step 4: Replace three-branch classification with two-branch**

Find the classification block (lines ~376–398):

```rust
// Before
let (entry_type, fitness) =
    if n >= STANDING_MIN_OBSERVATIONS
        && timing_fit >= STANDING_TIMING_GATE
        && amount_fit >= STANDING_AMOUNT_GATE
    {
        let c = (merchant_fit * 0.20
               + timing_fit  * 0.40
               + amount_fit  * 0.40).clamp(0.0, 1.0);
        ("standing", c)
    } else if n >= 2 && timing_fit >= VARIABLE_TIMING_GATE {
        let c = (merchant_fit * 0.30
               + timing_fit  * 0.55
               + amount_fit  * 0.15).clamp(0.0, 1.0);
        ("variable", c)
    } else {
        let c = (merchant_fit * 0.60
               + timing_fit  * 0.20
               + amount_fit  * 0.20).clamp(0.0, 1.0);
        ("irregular", c)
    };
```
```rust
// After
let (entry_type, fitness) =
    if n >= 2 && timing_fit >= VARIABLE_TIMING_GATE {
        let c = (merchant_fit * 0.30
               + timing_fit  * 0.55
               + amount_fit  * 0.15).clamp(0.0, 1.0);
        ("standing", c)
    } else {
        let c = (merchant_fit * 0.60
               + timing_fit  * 0.20
               + amount_fit  * 0.20).clamp(0.0, 1.0);
        ("variable", c)
    };
```

- [ ] **Step 5: Update anchor guard**

Find (line ~692):

```rust
// Before
let anchor: Option<String> = if score.entry_type == "irregular" {
    None
} else {
```
```rust
// After
let anchor: Option<String> = if score.entry_type == "variable" {
    None
} else {
```

- [ ] **Step 6: Run all engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add services/engine/src/pipeline/stage2.rs
git commit -m "feat: two-branch classification; irregular→variable; remove standing gates"
```

---

## Task 4: Go — entryTypeLabel and TypeFilter

**Files:**
- Modify: `services/web/page/handler.go`

**Interfaces:**
- Produces: `entryTypeLabel` with no `"irregular"` case; `TypeFilter` comment updated

- [ ] **Step 1: Scan for any "irregular" literals in Go files**

```bash
grep -rn '"irregular"' services/web --include="*.go" | grep -v "_test.go"
```

Fix any that appear outside of test files before proceeding.

- [ ] **Step 2: Update entryTypeLabel**

In `page/handler.go` (line ~810):

```go
// Before
func entryTypeLabel(t string) string {
    switch t {
    case "standing":
        return "Standing"
    case "variable":
        return "Variable"
    case "irregular":
        return "Irregular"
    default:
        return t
    }
}
```
```go
// After
func entryTypeLabel(t string) string {
    switch t {
    case "standing":
        return "Standing"
    case "variable":
        return "Variable"
    default:
        return t
    }
}
```

- [ ] **Step 3: Update TypeFilter comment (line ~531)**

```go
// Before
TypeFilter      string // ?entry_type=standing|variable|irregular
// After
TypeFilter      string // ?entry_type=standing|variable
```

- [ ] **Step 4: Build**

```bash
cd services/web && go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add services/web/page/handler.go
git commit -m "feat: remove irregular from entryTypeLabel"
```

---

## Task 5: Ledger template — pills and form selects

**Files:**
- Modify: `services/web/page/ledger.templ`

- [ ] **Step 1: Remove Irregular filter pill (line ~24–26)**

```go
// Before
@ledgerTypePill("standing", "Standing", data)
@ledgerTypePill("variable", "Variable", data)
@ledgerTypePill("irregular", "Irregular", data)
```
```go
// After
@ledgerTypePill("standing", "Standing", data)
@ledgerTypePill("variable", "Variable", data)
```

- [ ] **Step 2: Update add-entry form type select (lines ~127–129)**

```html
<!-- Before -->
<option value="standing">Standing</option>
<option value="variable">Variable</option>
<option value="irregular">Irregular</option>
```
```html
<!-- After -->
<option value="standing">Standing</option>
<option value="variable">Variable</option>
```

- [ ] **Step 3: Update entry edit form type select (lines ~1018–1020)**

```html
<!-- Before -->
<option value="standing" selected?={ e.EntryType == "standing" }>Standing</option>
<option value="variable" selected?={ e.EntryType == "variable" }>Variable</option>
<option value="irregular" selected?={ e.EntryType == "irregular" }>Irregular</option>
```
```html
<!-- After -->
<option value="standing" selected?={ e.EntryType == "standing" }>Standing</option>
<option value="variable" selected?={ e.EntryType == "variable" }>Variable</option>
```

- [ ] **Step 4: Build templ**

```bash
cd services/web && templ generate && go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add services/web/page/ledger.templ services/web/page/ledger_templ.go
git commit -m "feat: remove Irregular entry type from ledger UI"
```

---

## Task 6: Conditions editor JS

**Files:**
- Modify: `services/web/js-src/conditions-editor.js`

- [ ] **Step 1: Update autocomplete suggestion label (line ~246)**

```js
// Before
{ label: "entry_type", detail: "standing, variable, or irregular", apply: snippet('"entry_type": "${standing}"') },
```
```js
// After
{ label: "entry_type", detail: "standing or variable", apply: snippet('"entry_type": "${standing}"') },
```

- [ ] **Step 2: Update value autocomplete options (line ~308)**

```js
// Before
options: ["standing", "variable", "irregular"].map(v => ({ label: v, type: "enum", apply: makeApply(v) })),
```
```js
// After
options: ["standing", "variable"].map(v => ({ label: v, type: "enum", apply: makeApply(v) })),
```

- [ ] **Step 3: Update lint validation (line ~413)**

```js
// Before
if (key === "entry_type" && !["standing", "variable", "irregular"].includes(val)) {
    ...
    message: `${label(key)} must be "standing", "variable", or "irregular".`,
```
```js
// After
if (key === "entry_type" && !["standing", "variable"].includes(val)) {
    ...
    message: `${label(key)} must be "standing" or "variable".`,
```

- [ ] **Step 4: Build JS bundle**

```bash
cd services/web/js-src && npm run build
```

- [ ] **Step 5: Commit**

```bash
git add services/web/js-src/conditions-editor.js services/web/static/js/conditions-editor.js
git commit -m "feat: remove irregular from entry_type conditions editor"
```

---

## Task 7: Go tests

**Files:**
- Modify: `services/web/store/conditions_test.go`

- [ ] **Step 1: Find all "irregular" fixtures**

```bash
grep -n '"irregular"' services/web/store/conditions_test.go
```

- [ ] **Step 2: Update each fixture — irregular → variable**

```go
// Before
in := mustUnmarshal(t, `{"entry_type":"irregular"}`)
```
```go
// After
in := mustUnmarshal(t, `{"entry_type":"variable"}`)
```

Update any corresponding assertions on the round-tripped value.

- [ ] **Step 3: Run Go tests**

```bash
cd services/web && go test ./...
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add services/web/store/conditions_test.go
git commit -m "test: update entry_type fixtures irregular→variable"
```

---

## Task 8: Docs and glossary

**Files:**
- Modify: `docs/veloci-ref.md`
- Modify: `docs/veloci-spec.md`
- Modify: `docs/conditions-editor.md`
- Modify: `docs/impl-system-entries.md`
- Modify: `services/web/page/glossary.templ`

- [ ] **Step 1: Update veloci-ref.md**

Scoring weights table — remove irregular row, rename variable row to standing:

```markdown
| standing | 0.30 | 0.55 | 0.15 |
| variable | 0.60 | 0.20 | 0.20 |
```

Classification thresholds — remove standing row, rename variable row, update fallthrough:

```markdown
| `standing` | `observations ≥ 2` AND `timing ≥ 0.45` |
| `variable` | fallthrough — no detectable cadence |
```

Entry type table (lines ~26–28):

```markdown
- `standing` — recurring entry with strict cadence and timing. Amount may be fixed (e.g. Netflix $15.99) or variable within an expected range (e.g. utilities $100–$200). `rate_method` controls whether the projected rate uses median or max of matched transaction amounts.
- `variable` — irregular entry with no reliable cadence, timing, or amount pattern (previously `irregular`).
```

Schema reference (lines ~63–66) — update the entry_type line and rename variable_method:

```markdown
- `entry_type` TEXT — `standing` | `variable`
- `rate_method` TEXT — `median` | `max`; standing entries only; defaults to `median`
```

- [ ] **Step 2: Update veloci-spec.md**

Entry type table (lines ~71 and ~311):

```markdown
| **Standing** | Regular cadence, strict timing. Amount may be fixed or vary within a range. `rate_method` controls projected rate (`median` = median of matched amounts ÷ period, `max` = maximum ÷ period). Covers subscriptions and recurring obligations like utilities. |
| **Variable** | No detectable cadence or timing. One-off and infrequent purchases. Subject to ended detection only. |
```

Schema table (line ~143):

```markdown
| **entry_type** | `standing` · `variable` |
```

Engine classification description (line ~230):

```markdown
The scores determine entry type: clusters with detectable timing cadence (≥2 observations, timing_fit ≥ 0.45) are classified as Standing; everything else as Variable. Each cluster that clears the minimum confidence threshold produces a live entry in the `entries` table.
```

- [ ] **Step 3: Update conditions-editor.md**

Examples (lines ~69–71):

```markdown
{"entry_type": "standing"}
{"entry_type": "variable"}
```

Enum table (line ~155):

```markdown
| `entry_type` | Inline enum: `standing`, `variable` |
```

Error table (line ~170):

```markdown
| `entry_type` not `"standing"` or `"variable"` | Error |
```

- [ ] **Step 4: Update impl-system-entries.md**

```markdown
-- Line ~8: replace
standing/variable/irregular branch logic
-- with
standing/variable branch logic

-- Line ~119: replace (in any table rows referencing irregular)
`irregular`
-- with
`variable`
```

- [ ] **Step 5: Update glossary.templ (line ~52)**

```go
// Before
"An Entry is the core signal unit in Veloci. Each entry has conditions that match transactions (by merchant, amount, timing), converts the matched transactions into a $/day rate, and is associated with a label. Entries are created during the Review process and can be edited in the entry editor. An entry's type (standing, variable, irregular) determines how its rate is computed.",
```
```go
// After
"An Entry is the core signal unit in Veloci. Each entry has conditions that match transactions (by merchant, amount, timing), converts the matched transactions into a $/day rate, and is associated with a label. Entries are created during the Review process and can be edited in the entry editor. An entry's type (standing or variable) determines how its rate is computed. Standing entries recur on a regular cadence with a consistent or range-bound amount; their projected rate uses the median or max of matched transaction amounts (controlled by rate_method). Variable entries are irregular with no predictable cadence or timing.",
```

- [ ] **Step 6: Build and run all tests**

```bash
cd services/web && templ generate && go build ./... && go test ./...
cd services/engine && cargo test
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add docs/veloci-ref.md docs/veloci-spec.md docs/conditions-editor.md docs/impl-system-entries.md
git add services/web/page/glossary.templ services/web/page/glossary_templ.go
git commit -m "docs: update entry type definitions for standing/variable rename"
```

---

## Task 9: Schema — rename variable_method to rate_method

**Files:**
- Modify: `migrations/app/002_financial_schema.sql`

**Interfaces:**
- Produces: `rate_method CHECK ('median', 'max') DEFAULT 'median'` replacing `variable_method CHECK ('avg', 'max')`

- [ ] **Step 1: Update the column definition**

In `migrations/app/002_financial_schema.sql`, find (line ~203):

```sql
-- Before
  variable_method        TEXT          CHECK (variable_method IN ('avg', 'max')),
```
```sql
-- After
  rate_method            TEXT          NOT NULL DEFAULT 'median'
                         CHECK (rate_method IN ('median', 'max')),
```

- [ ] **Step 2: Update any comment referencing variable_method (line ~291)**

```sql
-- Before
  -- via variable_method over the 3*period_days projection lookback window.
-- After
  -- via rate_method ('median' or 'max') over the 3*period_days projection lookback window.
```

- [ ] **Step 3: Rebuild the database**

```bash
make db-reset
```

- [ ] **Step 4: Commit**

```bash
git add migrations/app/002_financial_schema.sql
git commit -m "feat: rename variable_method→rate_method; avg→median; add NOT NULL DEFAULT"
```

---

## Task 10: Rust — VariableMethod enum rename

**Files:**
- Modify: `services/engine/src/pipeline/types.rs`

**Interfaces:**
- Produces: `VariableMethod::Median` and `VariableMethod::Max`; `from_str("avg")` → `None`; `from_str("median")` → `Some(Median)`

- [ ] **Step 1: Update the test first**

Find the `VariableMethod::from_str` tests:

```rust
// Before
assert_eq!(VariableMethod::from_str("avg"), Some(VariableMethod::Avg));
assert_eq!(VariableMethod::from_str("max"), Some(VariableMethod::Max));
assert_eq!(VariableMethod::from_str("avg"),  Some(VariableMethod::Avg));
```
```rust
// After
assert_eq!(VariableMethod::from_str("median"), Some(VariableMethod::Median));
assert_eq!(VariableMethod::from_str("max"),    Some(VariableMethod::Max));
assert_eq!(VariableMethod::from_str("avg"),    None);
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd services/engine && cargo test types -- --nocapture 2>&1 | grep -E "FAILED|error"
```

- [ ] **Step 3: Rename Avg → Median and update from_str**

In `types.rs` (line ~39–51):

```rust
// Before
pub enum VariableMethod {
    /// Use the average of recent observed amounts.
    Avg,
    /// Project the maximum of recent observed amounts (conservative).
    Max,
}
// from_str arms:
"avg" => Some(Self::Avg),
"max" => Some(Self::Max),
```
```rust
// After
pub enum VariableMethod {
    /// Project using the median of matched transaction amounts ÷ period_days.
    Median,
    /// Project using the maximum of matched transaction amounts ÷ period_days (conservative).
    Max,
}
// from_str arms:
"median" => Some(Self::Median),
"max"    => Some(Self::Max),
```

- [ ] **Step 4: Run tests**

```bash
cd services/engine && cargo test types
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add services/engine/src/pipeline/types.rs
git commit -m "feat: rename VariableMethod::Avg→Median; update from_str"
```

---

## Task 11: Rust — stage2 sets rate_method on INSERT; stage3 branches on it

**Files:**
- Modify: `services/engine/src/pipeline/stage2.rs`
- Modify: `services/engine/src/pipeline/stage3.rs`

**Interfaces:**
- Stage2 produces: `rate_method = 'median'` on every new entry INSERT
- Stage3 produces: `projected_rate_per_day` computed as `median(amounts)/period` or `max(amounts)/period` depending on `rate_method`; falls back to `actual_rate_per_day` for entries with no transactions in window

- [ ] **Step 1: Add rate_method to stage2 INSERT (stage2.rs)**

In the INSERT SQL (line ~761–773), add `rate_method` to the column list and `'median'` to the VALUES list:

```sql
-- Before
"INSERT INTO entries (
   entity_id, label_id, direction, entry_type, period_days, next_due_date,
   recurrence_anchor, conditions, projected_rate_per_day,
   status, source, start_date,
   alert_type, fitness, merchant_fit, timing_fit, amount_fit,
   sample_merchants, matched_transaction_count
 ) VALUES (
   $1, $2, $3, $4, $5, $6,
   $7, $8, $9,
   'pending', 'engine', $10,
   'new', $11, $12, $13, $14,
   $15, $16
 ) RETURNING id"
```
```sql
-- After
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
 ) RETURNING id"
```

No new `.bind()` call needed — `'median'` is a literal in the SQL.

- [ ] **Step 2: Add rate_method to ActiveEntry in stage3.rs**

Find the `ActiveEntry` struct (line ~38–46):

```rust
// Before
pub(crate) struct ActiveEntry {
    pub id:              Uuid,
    pub label_id:        Uuid,
    pub direction:       String,
    pub entry_type:      String,
    pub period_days:     i32,
    pub variable_method: Option<String>,
    pub projected_rate_per_day: Option<f64>,
}
```
```rust
// After
pub(crate) struct ActiveEntry {
    pub id:              Uuid,
    pub label_id:        Uuid,
    pub direction:       String,
    pub entry_type:      String,
    pub period_days:     i32,
    pub rate_method:     String,
    pub projected_rate_per_day: Option<f64>,
}
```

- [ ] **Step 3: Update the SQL SELECT for active entries (line ~207–215)**

```sql
-- Before
SELECT e.id, e.label_id, e.direction, e.entry_type, e.period_days,
       e.variable_method, e.projected_rate_per_day, e.start_date
FROM entries e
WHERE ...
```
```sql
-- After
SELECT e.id, e.label_id, e.direction, e.entry_type, e.period_days,
       e.rate_method, e.projected_rate_per_day, e.start_date
FROM entries e
WHERE ...
```

And update the struct mapping (line ~234):

```rust
// Before
variable_method:        r.variable_method,
// After
rate_method:            r.rate_method,
```

- [ ] **Step 4: Write the two helper functions (add near compute_actual_rate, line ~185)**

```rust
fn median_rate(txns: &[&AssignedTxn], period_days: i32) -> f64 {
    if txns.is_empty() || period_days <= 0 { return 0.0; }
    let mut amounts: Vec<i64> = txns.iter().map(|t| t.amount_cents.abs()).collect();
    amounts.sort_unstable();
    let n = amounts.len();
    let median = if n % 2 == 0 {
        (amounts[n / 2 - 1] + amounts[n / 2]) / 2
    } else {
        amounts[n / 2]
    };
    median as f64 / period_days as f64
}

fn max_rate(txns: &[&AssignedTxn], period_days: i32) -> f64 {
    if txns.is_empty() || period_days <= 0 { return 0.0; }
    let max = txns.iter().map(|t| t.amount_cents.abs()).max().unwrap_or(0);
    max as f64 / period_days as f64
}
```

- [ ] **Step 5: Replace the projected_rate_per_day computation (line ~158–162)**

```rust
// Before
let projected_rate_per_day = if let Some(user_rate) = entry.projected_rate_per_day {
    user_rate
} else {
    prior_projected_rate.unwrap_or(actual_rate_per_day)
};
```
```rust
// After
let projected_rate_per_day = if let Some(user_rate) = entry.projected_rate_per_day {
    user_rate
} else if active_txns.is_empty() {
    prior_projected_rate.unwrap_or(actual_rate_per_day)
} else {
    match entry.rate_method.as_str() {
        "max" => max_rate(&active_txns, entry.period_days),
        _     => median_rate(&active_txns, entry.period_days), // "median" is the default
    }
};
```

- [ ] **Step 6: Write tests for the two helper functions (in stage3's test module)**

```rust
#[cfg(test)]
mod tests {
    use super::*;

    fn make_txn(amount_cents: i64) -> AssignedTxn {
        AssignedTxn {
            entry_id:     uuid::Uuid::nil(),
            txn_date:     chrono::NaiveDate::from_ymd_opt(2026, 1, 1).unwrap(),
            amount_cents,
        }
    }

    #[test]
    fn median_rate_odd() {
        let txns = vec![make_txn(-1000), make_txn(-3000), make_txn(-2000)];
        let refs: Vec<&AssignedTxn> = txns.iter().collect();
        // median of [1000, 2000, 3000] = 2000; rate = 2000 / 30 ≈ 66.67
        assert!((median_rate(&refs, 30) - 2000.0 / 30.0).abs() < 0.01);
    }

    #[test]
    fn median_rate_even() {
        let txns = vec![make_txn(-1000), make_txn(-2000), make_txn(-3000), make_txn(-4000)];
        let refs: Vec<&AssignedTxn> = txns.iter().collect();
        // median of [1000, 2000, 3000, 4000] = (2000+3000)/2 = 2500; rate = 2500 / 30
        assert!((median_rate(&refs, 30) - 2500.0 / 30.0).abs() < 0.01);
    }

    #[test]
    fn max_rate_picks_largest() {
        let txns = vec![make_txn(-1000), make_txn(-5000), make_txn(-2000)];
        let refs: Vec<&AssignedTxn> = txns.iter().collect();
        // max = 5000; rate = 5000 / 30
        assert!((max_rate(&refs, 30) - 5000.0 / 30.0).abs() < 0.01);
    }

    #[test]
    fn median_rate_empty_returns_zero() {
        assert_eq!(median_rate(&[], 30), 0.0);
    }
}
```

- [ ] **Step 7: Run engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add services/engine/src/pipeline/stage2.rs services/engine/src/pipeline/stage3.rs
git commit -m "feat: wire rate_method in stage2/stage3; median and max projection paths"
```

---

## Task 12: Go — rename VariableMethod → RateMethod; trigger on change

**Files:**
- Modify: `services/web/store/entries.go`
- Modify: `services/web/handler/entries.go`
- Modify: `services/web/page/handler.go`

**Interfaces:**
- Produces: all Go structs use `RateMethod *string` / `rate_method` (db tag); `UpdateEntry` dispatches `account.analyze` when `rate_method` changes; `entryPutBody` serializes `rate_method`

- [ ] **Step 1: Rename in store/entries.go**

```go
// EntryRow struct (line ~25):
// Before
VariableMethod      *string         `db:"variable_method"`
// After
RateMethod          *string         `db:"rate_method"`

// SELECT columns (line ~55) — rename the column reference:
// Before
e.direction, e.entry_type, e.period_days, e.variable_method,
// After
e.direction, e.entry_type, e.period_days, e.rate_method,

// CreateEntryInput struct (line ~219):
// Before
VariableMethod       *string
// After
RateMethod           *string

// INSERT column list (line ~233):
// Before
variable_method, projected_rate_per_day, conditions, priority,
// After
rate_method, projected_rate_per_day, conditions, priority,

// INSERT bind (line ~255):
// Before
in.VariableMethod, in.ProjectedRatePerDay, ...
// After
in.RateMethod, in.ProjectedRatePerDay, ...

// UpdateEntryInput struct (line ~270):
// Before
VariableMethod      *string
// After
RateMethod          *string

// UPDATE SET clause (line ~299):
// Before
variable_method = $7,
// After
rate_method = $7,

// UPDATE bind (line ~325):
// Before
in.VariableMethod, in.ProjectedRatePerDay, ...
// After
in.RateMethod, in.ProjectedRatePerDay, ...
```

- [ ] **Step 2: Rename in handler/entries.go**

```go
// CreateEntry body struct (line ~208):
// Before
VariableMethod      *string         `json:"variable_method"`
// After
RateMethod          *string         `json:"rate_method"`

// CreateEntry store input (line ~243):
// Before
VariableMethod:      body.VariableMethod,
// After
RateMethod:          body.RateMethod,

// UpdateEntry body struct (line ~267):
// Before
VariableMethod      *string         `json:"variable_method"`
// After
RateMethod          *string         `json:"rate_method"`

// UpdateEntry store input (line ~316):
// Before
VariableMethod:      body.VariableMethod,
// After
RateMethod:          body.RateMethod,
```

- [ ] **Step 3: Add rate_method change trigger to UpdateEntry**

After the `h.s.UpdateEntry(...)` call succeeds (around line ~326), add the change detection and dispatch. The handler needs the existing entry first — fetch it before the update:

```go
func (h *EntriesHandler) UpdateEntry(c echo.Context) error {
    ctx := c.Request().Context()
    entityID := middleware.EntityID(ctx)
    id := c.Param("id")

    // ... existing body binding and validation ...

    // Fetch current rate_method before update to detect changes.
    existing, err := h.s.GetEntry(ctx, entityID, id)
    if errors.Is(err, pgx.ErrNoRows) {
        return echo.NewHTTPError(http.StatusNotFound, "not found")
    }
    if err != nil {
        return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
    }

    item, err := h.s.UpdateEntry(ctx, entityID, id, store.UpdateEntryInput{ /* ... */ })
    // ... existing error handling ...

    // Trigger engine recompute when rate_method changes.
    oldMethod := ""
    if existing.RateMethod != nil { oldMethod = *existing.RateMethod }
    newMethod := ""
    if body.RateMethod != nil { newMethod = *body.RateMethod }
    if newMethod != "" && newMethod != oldMethod {
        meta, _ := json.Marshal(map[string]string{})
        if job, err := h.s.CreateJob(ctx, entityID, "account.analyze", middleware.UserID(ctx), meta); err == nil {
            h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
                JobID:    job.ID,
                Type:     "account.analyze",
                EntityID: entityID,
                Metadata: meta,
            })
        }
    }

    item.Conditions = h.s.ConditionsForDisplay(ctx, entityID, item.Conditions)
    return c.JSON(http.StatusOK, response.Single(toEntryView(item)))
}
```

- [ ] **Step 4: Rename in page/handler.go**

```go
// entryPutBody struct (line ~996):
// Before
VariableMethod      *string         `json:"variable_method"`
// After
RateMethod          *string         `json:"rate_method"`

// entryDataJSON (line ~1029):
// Before
VariableMethod:      e.VariableMethod,
// After
RateMethod:          e.RateMethod,
```

- [ ] **Step 5: Build**

```bash
cd services/web && go build ./...
```

Expected: clean compile.

- [ ] **Step 6: Run Go tests**

```bash
cd services/web && go test ./...
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add services/web/store/entries.go services/web/handler/entries.go services/web/page/handler.go
git commit -m "feat: rename variable_method→rate_method in Go; trigger analyze on rate_method change"
```
