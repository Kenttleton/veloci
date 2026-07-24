# Conditions Editor

Feature reference for the ledger entry conditions editor. This document covers Schema B (the human-authored format), the translation contract with Schema A, and the CM6 editor behavior. For Schema A internals (engine evaluation, two-pass model, fitness gates) see `docs/veloci-ref.md`.

---

## Schema B — the editor format

Schema B is the JSON format users write in the conditions editor and the format returned by all entry API endpoints. It uses the condition type as the object key and the match target as the value. The translation layer in `store/conditions.go` converts between Schema B and Schema A on every read/write.

### Logical operators

```json
{"and": [...]}        // all children must match
{"or":  [...]}        // any child must match
{"not": {...}}        // child must NOT match
```

Operators nest freely. XOR is not a direct Schema B operator — compose with `and`/`or`/`not` if needed.

### Transaction-target leaves (Pass 1)

Evaluated against `merchant_normalized`, `amount_cents`, `date`, `account_id`, and `institution_id`.

All `payee_*` comparisons are case-insensitive except `payee_regex` (case controlled by inline flags, e.g. `(?i)NETFLIX`).

```json
{"payee_contains":     "NETFLIX"}
{"payee_exact":        "NETFLIX.COM"}
{"payee_starts_with":  "AMZ"}
{"payee_ends_with":    ".COM"}
{"payee_not_contains": "REFUND"}
{"payee_regex":        "^NETFLIX"}
{"payee_one_of":       ["Netflix", "Hulu", "Disney"]}

{"amount_range": {"min": -50, "max": -10}}
```

`amount_range` values are in **dollars** (positive = inflow/credit, negative = outflow/debit). Both bounds are optional; omitting a bound leaves it open. The translation layer converts dollars ↔ cents (`× 100`) when writing to / reading from Schema A.

```json
{"date_day_of_month": {"day": 15, "tolerance_days": 2}}
{"date_range": {"start": "2026-01-01", "end": "2026-12-31"}}
```

`date_day_of_month.day` accepts positive integers only (1–28). Negative month-end indexing (like `dom:-1` in `recurrence_anchor`) is not supported for this condition — use `entry_recurrence_anchor` if you need to match entries by their anchor. `tolerance_days` is optional (default 0).

```json
{"account":     "Chase Checking"}
{"institution": "Chase"}
```

Account and institution values are plain-string names. `ConditionsForStorage` resolves them to UUIDs; `ConditionsForDisplay` resolves UUIDs back to names. An unresolvable name is a save error.

### Entry-target leaves (Pass 2+)

Evaluated against the set of entries that matched this transaction in Pass 1 (and prior Pass 2+ iterations). A condition matches if **any single** accumulated entry satisfies all gates in the node.

```json
{"label_matched": "Netflix"}
```

Name resolved to/from UUID at the API boundary. In the summary view, label names are clickable links that filter the ledger to entries bearing that label.

```json
{"entry_direction": "income"}
{"entry_direction": "spend"}

{"entry_type": "standing"}
{"entry_type": "variable"}
{"entry_type": "irregular"}

{"entry_period": {"min_days": 25, "max_days": 35}}

{"entry_projected_rate": {"min": 1.5, "max": 5.0}}
```

`entry_period` and `entry_projected_rate` both bounds are optional.

```json
{"entry_fitness": {"overall": {"min": 0.8}, "merchant": {"min": 0.7, "max": 1.0}}}
```

`entry_fitness` score object keys: `overall`, `merchant`, `timing`, `amount`. Each is an object with optional `min`/`max` bounds. All specified gates must be satisfied by the **same single** accumulated entry.

```json
{"entry_recurrence_anchor": "dom:15"}
{"entry_recurrence_anchor": "dom:-1"}
{"entry_recurrence_anchor": "dow:0"}
```

Matches when any accumulated matched entry has the specified recurrence anchor. Uses the same anchor format as `entries.recurrence_anchor`: `dom:N` (day of month; negative = from month end), `dow:N` (day of week, 0=Mon), `dom:N,M` (semi-monthly), `interval:N` (every N days).

**Excluded from Schema B:** `entry_source` (`user`/`engine`/`system`) is system metadata, not a user-facing match criterion.

---

## Plain-language display names

The editor and summary use these display names. JSON type keys are never shown.

| Schema B key | Display name |
| --- | --- |
| `and` | All of these |
| `or` | Any of these |
| `not` | None of these |
| `payee_contains` | Description contains |
| `payee_exact` | Description is exactly |
| `payee_starts_with` | Description starts with |
| `payee_ends_with` | Description ends with |
| `payee_not_contains` | Description does not contain |
| `payee_regex` | Description matches pattern |
| `payee_one_of` | Description is one of |
| `amount_range` | Amount is between |
| `date_day_of_month` | Day of month is |
| `date_range` | Date is between |
| `account` | From account |
| `institution` | From institution |
| `label_matched` | Tagged as |
| `entry_direction` | Direction is |
| `entry_type` | Type is |
| `entry_period` | Period is between |
| `entry_projected_rate` | Projected rate is between |
| `entry_fitness` | Fitness is |
| `entry_recurrence_anchor` | Anchor is |

---

## Translation layer (`store/conditions.go`)

`ConditionsForDisplay(ctx, entityID, raw)` — Schema A → Schema B. Called on every read. Resolves UUIDs to names. Handles legacy Schema A nodes nested inside Schema B pass-through.

`ConditionsForStorage(ctx, entityID, raw)` — Schema B → Schema A. Called on every write. Resolves names to UUIDs. Creates a label if `label_matched` references an unknown name. Returns an error if `account` or `institution` cannot be resolved. Multiplies `amount_range.min`/`.max` by 100 to convert dollars to cents.

Both functions are recursive and handle arbitrary nesting depth. The round-trip `Display → Storage → Display` must produce identical Schema B output for all supported condition types.

---

## Editor (`js-src/conditions-editor.js`)

Built on CodeMirror 6. Initialized lazily on `<details>` `toggle` open. One instance per entry row.

### Autocomplete

Three completers run in parallel via `autocompletion({ override: [...] })`:

**`contextKeyCompleter`** — fires inside a JSON property key string. Walks the CM6 syntax tree via `getJsonContext` to determine cursor depth and nearest parent combinator. At depth 1 (root), logic operators (`and`/`or`/`not`) appear first. At depth 2+, condition type keys appear first. Both groups are always present and filter by typed prefix. Applies a snippet that inserts the full key–value structure with the cursor placed at the value.

**`valueCompleter`** (async) — fires inside a JSON string value, dispatched by the property key:

| Key | Completions |
| --- | --- |
| `payee_*` | Live search: `QUERY /api/transactions/merchant` with typed prefix; returns `merchant_normalized` strings by frequency |
| `entry_direction` | Inline enum: `income`, `spend` |
| `entry_type` | Inline enum: `standing`, `variable`, `irregular` |
| `label_matched` | Cached fetch `/api/labels?limit=500`, filtered by name |
| `account` | Cached fetch `/api/accounts?limit=500`, filtered by name |
| `institution` | Cached fetch `/api/institutions`, filtered by `institution_name` |

**`structureCompleter`** — fires in non-string positions after `"and":`, `"or":`, `"not":`. Offers the expected container shape so users don't have to remember which combinators take arrays vs objects.

### Linting

Runs 600ms after last keystroke. Reports diagnostics on the relevant key or value span.

| Check | Severity |
| --- | --- |
| Unknown key | Warning |
| `entry_direction` not `"income"` or `"spend"` | Error |
| `entry_type` not `"standing"`, `"variable"`, or `"irregular"` | Error |
| `and`/`or` value is not an array | Error |
| `payee_*` value is an empty string | Warning |
| `amount_range` value is not an object with at least one of `min`/`max` | Error |
| `date_day_of_month` missing `day` field | Error |
| `date_range` missing `start` or `end` field | Error |
| `payee_one_of` value is not a non-empty array | Error |

Linter messages use display names ("Description contains requires a non-empty string") rather than JSON key names.

### Plain-language summary

Updates 300ms after document change. `summaryHTML` renders the Schema B tree as a readable English phrase using display names. `label_matched` and `account` values render as clickable links. The summary reads from the in-editor document (not the saved DB value) so it reflects unsaved edits immediately.

### Save indicator and auto-save

The save indicator (`.js-conditions-status`) communicates state at all times:

| State | Indicator text |
| --- | --- |
| No edits since last save | _(empty)_ |
| JSON is invalid or incomplete | `invalid JSON` |
| JSON valid, waiting to auto-save (1.5s debounce) | `unsaved` |
| Save in flight | `saving…` |
| Save succeeded | `saved` (clears after 2s) |
| Save failed | `error` |

Auto-save fires 1.5s after the last keystroke **only if** the document parses as valid JSON. When JSON is not yet valid the indicator shows `invalid JSON` immediately rather than staying blank.

### Theme and contrast

The editor uses a dark-safe custom theme (`velociTheme`) with CSS variable integration. Lint range markers use colored underlines; lint tooltips use `var(--surface)` background with `var(--text)` foreground to maintain contrast in both light and dark modes.

---

## Data flow

```text
DB (Schema A, UUIDs)
  ↓ ConditionsForDisplay   (A → B, UUIDs → names, cents → dollars)
JSON response (Schema B, names, dollars)
  ↓ CM6 editor renders; user edits
  ↓ PATCH /api/entries/:id/conditions  (Schema B body)
  ↓ ConditionsForStorage   (B → A, names → UUIDs, dollars → cents)
DB (Schema A, UUIDs)
```

---

## API endpoints

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/entries` | Returns `conditions` in Schema B |
| `GET` | `/api/entries/:id` | Returns `conditions` in Schema B |
| `PUT` | `/api/entries/:id` | Accepts Schema B; translates to Schema A on write |
| `PATCH` | `/api/entries/:id/conditions` | Editor auto-save endpoint |
| `QUERY` | `/api/transactions/merchant` | Payee autocomplete; body `{"payee":"<query>"}` |
