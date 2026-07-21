# Conditions Refactor Design

**Date:** 2026-07-21
**Status:** Approved — ready for implementation

---

## Context

This document captures a design discussion that supersedes the canonical-merchants design
(`2026-07-18-canonical-merchants-design.md`). The earlier design introduced a
`canonical_merchants` + `canonical_merchant_aliases` two-table model for merchant matching.
That model is static, alias-list-based, and not resistant to change over time. It is being
replaced by a unified, JSON-based conditions system on `entries`.

---

## Core model

### One entity: entries

`entries` is the single composable matching primitive. There is no separate canonical merchant
entity and no classifications entity.

- **Entries have IDs, not names.** The display name for an entry is its label's name. An entry
  without a `label_id` is "Unlabeled" — never an ID prefix.
- **Labels are the named, reusable concepts.** An entry applies a label to matched transactions
  via `entries.label_id`. Multiple entries can share the same label (e.g., different pricing
  periods for the same subscription).
- **Leaf entries** match transactions directly via string conditions.
- **Composite entries** match other entries indirectly via `label_matched` conditions.
- The hierarchy is implicit in the condition tree; the table stays flat.

Labels (`labels` table, `entries.label_id`) remain as human-assigned name tags. They have no
conditions of their own.

### What is removed

| Removed | Replaced by |
| --- | --- |
| `canonical_merchants` table | not needed — merchant matching is entry conditions |
| `canonical_merchant_aliases` table | not needed — alias patterns are entry conditions |
| `canonical_merchant_id` on `transactions` | not needed — stage 1 output covers this |
| `classifications` table | entry composability via `label_matched` condition |
| Config merchants tab | not needed |
| Go `handler/canonical_merchants.go`, `store/canonical_merchants.go` | deleted |
| Go `handler/classifications.go`, `store/classifications.go` | deleted |
| Rust `CanonicalAliasMap`, `CanonicalMerchant(Uuid)` in stage 1 | removed |
| `NodeType::Classification` | renamed to `NodeType::Label` |
| `snapshots.node_type` value `'classification'` | renamed to `'label'` |

Stages 4 and 5 are otherwise unchanged — they aggregate entry rates by `label_id` and compute
drift. The internal naming changes from "classification nodes" to "label nodes."

---

## Conditions schema

Two evaluation passes, one node schema shared across all entries.

### Boolean algebra (structural — same for all entries)

```json
{ "type": "and", "children": [ ... ] }
{ "type": "or",  "children": [ ... ] }
{ "type": "not", "child": { ... } }
```

### Transaction targets (Pass 1 — evaluated against raw transaction fields)

```json
{ "type": "payee_exact",        "value": "NETFLIX.COM" }
{ "type": "payee_contains",     "value": "NETFLIX" }
{ "type": "payee_not_contains", "value": "REFUND" }
{ "type": "payee_starts_with",  "value": "NETFLIX" }
{ "type": "payee_ends_with",    "value": ".COM" }
{ "type": "payee_regex",        "value": "NETFLIX.*\\d{4}" }

{ "type": "amount_exact",       "value": 1599 }
{ "type": "amount_range",       "min": 1000, "max": 2000 }

{ "type": "date_day_of_month",  "day": 15, "tolerance_days": 3 }
{ "type": "date_range",         "start": "2024-01-01", "end": "2024-12-31" }

{ "type": "account",            "account": "Chase Checking" }
```

`account` is UUID-resolved on save, name-displayed in the editor. Clicking the name navigates
to that account.

### Entry targets (Pass 2+ — evaluated against accumulated label set)

```json
{ "type": "label_matched",   "label": "groceries" }
{ "type": "entry_direction", "value": "income" }
{ "type": "entry_type",      "value": "standing" }
```

`label_matched` is UUID-resolved on save, name-displayed in the editor. Clicking a label name
in the editor filters the ledger to entries bearing that label.

**Composability is label-to-label, not entry-to-entry.** Entries have no names — only IDs.
Composite entries reference other entries by label: `label_matched: "Subscriptions"` matches
all transactions that were assigned to any entry whose `label_id` is "Subscriptions."
There is no `entry_matched` node type.

A single `label_matched` node correctly captures all historical entries for that label
(e.g., Netflix at $13.99 and Netflix at $17.99 are two entries sharing the same label —
both are captured by `label_matched: "Netflix"`).

`entry_direction` and `entry_type` filter by the properties of entries that matched the
transaction in Pass 1.

---

## Stage 1 restructured (iterative)

**Pass 1:** Evaluate all entries whose conditions contain only transaction targets.
For each transaction: collect matching entry IDs → write to `transaction_entry_assignments`
→ collect the set of labels earned by the transaction.

**Pass 2+:** Iterative label expansion per transaction.

After Pass 1, each transaction has a set of earned labels. Stage 1 then repeatedly evaluates
entries whose conditions reference entry targets (`label_matched`, `entry_direction`,
`entry_type`) using the current label set. Each iteration may add new labels, which unlocks
further entries in the next iteration.

**Termination per transaction:** stop when either:

- No new labels were added in the last iteration (stable), or
- A label is encountered that is already in the transaction's label set (cycle detected).

Cycles are allowed and intentional. When a cycle is detected, the current transaction's
expansion terminates and the cycle is logged (not an error — expected for mutual or reflexive
label references). Different transactions may traverse the same cycle and each terminates
independently on their first repeat. A transaction can accumulate many labels across many
passes; label graph topology is unconstrained.

**Merchant normalization (stage 0):** Basic sanitization only — `merchant_normalized` is a
lightly-cleaned version of `imported_payee`, not a canonical form.

Transformations applied (in order):

1. Unicode NFC normalization
2. Strip control characters and non-printable characters (U+0000–U+001F, U+007F–U+009F,
   zero-width spaces, etc.)
3. Trim leading and trailing whitespace
4. Collapse internal whitespace runs to a single space

**No casing changes.** Original case is preserved. Casing is the user's and engine's
responsibility at match time, not the importer's.

**Case sensitivity in payee matching:**

- `payee_exact`, `payee_contains`, `payee_not_contains`, `payee_starts_with`, `payee_ends_with`
  — case-insensitive always. Users should not need to know or care about bank casing.
- `payee_regex` — user-controlled via inline flags. Default is case-sensitive; users who need
  case-insensitive write `(?i)NETFLIX` or `(?i:netflix)`. Documentation covers this.

---

## Engine entry proposals (stage 2)

Stage 2 groups unmatched transactions by exact `merchant_normalized` value and proposes an
entry with a `payee_exact` condition. This is the default. The user refines to
`payee_contains`, `payee_regex`, etc. in the ledger editor.

Stage 2 may additionally cluster similar payee strings and propose `payee_contains` conditions
when it detects a consistent prefix — kept as engine heuristics, not a stored entity.
The "canonical merchant" concept is a transient grouping heuristic in stage 2 only.

---

## Confidence model

Confidence is a measure of **entry quality against its matched transactions** — not a measure
of how certain the engine is in its guess. It applies equally to engine-proposed and
user-created entries.

The question confidence answers: *given the transactions your conditions pull in, how well
does your entry's declared metadata describe the actual pattern?*

- A `standing` entry with consistent monthly Netflix charges → high confidence
- A `standing` entry whose matched transactions arrive irregularly at varying amounts → low
  confidence, signaling the `entry_type` or `period` may be misconfigured

**Confidence dimensions (same for all entries regardless of source):**

| Dimension | What it measures |
| --- | --- |
| Overall | Weighted composite of the three below |
| Merchant / payee | How tightly the matched transactions cluster around the declared payee pattern |
| Timing | How consistently matched transactions arrive relative to the declared period and anchor |
| Amount | How consistently matched transaction amounts match the declared projected rate |

**Recomputed on every reprocess.** Confidence is not written once at creation — it is
updated after each engine run for all active entries. This means a user who edits an entry's
conditions or metadata and reprocesses will immediately see updated confidence reflecting
whether the new configuration fits their actual transaction history.

**Engine-proposed entries** start with an initial confidence score from stage 2 based on the
transactions that triggered the proposal. This score updates on every subsequent reprocess.

**User-created entries** start with no confidence score (NULL) since they have no matched
transactions yet. After the first reprocess, confidence is computed from the newly matched
transactions exactly as for engine entries.

Stage 3 is the appropriate place to compute and write updated confidence scores for all
active entries after each run.

---

## Budget breakdown model

The Budget graph shows spending at every label level simultaneously. Overlap is intentional.

A Netflix transaction contributes to:

- The **Netflix** label (leaf entry, `payee_contains: "NETFLIX"`)
- The **Streaming** label (composite entry, `label_matched: "Netflix"`)
- Any higher-level label that references Streaming

All three appear in the Budget with independent rates and proportions:

- Netflix: $17.99/mo = **2% of overall budget**, 34% of Streaming
- Streaming: $52/mo = **5% of overall budget**, X% of Entertainment
- Entertainment: ...

These are not mutually exclusive buckets. They are overlapping perspectives on the same
transactions at different levels of abstraction. The user is meant to see both the leaf detail
and the aggregate simultaneously and compare them against the overall picture.

Consequences for implementation:

- Stage 4 label rate aggregation is correct as-is — it sums entry rates per label without
  deduplication across labels. A transaction matched by both Netflix and Streaming entries
  contributes to both label rates.
- The Budget graph does not attempt to reconcile overlapping rates into a 100% pie. It is a
  proportional view at each level independently.
- The DAG implied by `label_matched` chains is the structure the Budget uses for its
  breakdown hierarchy — leaf → mid → top — but every node in the DAG is shown, not just
  leaves or just roots.

---

## `pending_review` processing boundary

The engine is the **only** component that applies condition logic. The web service never
evaluates conditions — it only reads rows the engine has written. This means:

- There is no on-demand preview endpoint in the web service. The `POST /api/entries/preview`
  endpoint is removed.
- The ledger transaction panel — for both `pending_review` and `active` entries — reads from
  `transaction_entry_assignments`. "Will be" vs "are" is display language derived from
  `entries.status`, not from a separate data path.

**Stage 1** processes entries with `status IN ('active', 'pending_review')`. It writes
`transaction_entry_assignments` rows for both. This is how `pending_review` entries get
their transactions — via a reprocess run.

**Stage 3+** stays filtered to `status = 'active'`. The existing `load_active_entries`
query (`AND status = 'active' AND end_date IS NULL`) is correct and unchanged. Snapshots,
rates, and confidence calculations never touch `pending_review` entries.

**Consequence for UX:** when a user creates or edits a `pending_review` entry, they hit the
reprocess button to see what transactions the conditions match. The transaction panel shows
the results from the most recent engine run — it does not update in real time until a reprocess
completes. This is the intended behavior.

Approving a `pending_review` entry flips `status → 'active'` and marks the page dirty. The
next reprocess computes the entry's first confidence score and treats its assignments as real.

---

## Reprocessing scope

Conditions changes have two scopes:

| Scope | Trigger | Reprocess |
| --- | --- | --- |
| **Entry scope** (ledger page) | User edits one or more entry conditions | Manual — reprocess button on ledger page; warn on navigation if dirty |
| **Leaf scope** (future) | Change isolated enough to not affect other entries | Auto-reprocess on save |

Ledger dirty state is page-level: any unsaved/unprocessed condition edit on the page marks it
dirty. Navigating away while dirty shows a warning. The reprocess button is visible and
prominent when the page is dirty.

---

## Ledger page additions

### Sort and filter

Filter pills (existing): `all`, `active`, `pending_review`, `inactive`

Additional filter capability:

- Filter by label (deeplink target — clicking a `label_matched` name in conditions applies this)
- Filter by direction (`income` / `expense`)
- Filter by entry type (`standing` / `variable` / `irregular`)
- Sort: by `start_date` (default DESC), by `actual_rate_per_day`, by `confidence`, by label name

Filters are URL query parameters — shareable and survive page reload.
Label filter deeplink: `?label=<uuid>`

### Add Entry button

A button in the ledger page header opens an "Add Entry" modal with the same form as the
inline edit/review panel:

- Label name (creates or links an existing label on save)
- Direction (`income` / `expense`)
- Entry type (`standing` / `variable` / `irregular`)
- Projected rate (optional)
- Conditions (CM6 JSON editor)
- Start date (defaults to today)

On save: entry is created with `status = 'pending_review'`, `source = 'user'`. It appears in
the ledger under the Pending Review filter. The page marks dirty immediately. The user hits
the reprocess button — the engine runs, matches transactions to the new entry's conditions,
and writes rows to `transaction_entry_assignments`. The transaction panel then shows "will be"
results from those rows. The user approves when satisfied — same path as an engine proposal.
Approving moves the entry to `status = 'active'` and marks the page dirty for another reprocess,
after which confidence is computed from the matched transactions (see Confidence model).

---

## Conditions editor UX

Veloci is a product for real users — not software professionals. The conditions editor must
be approachable without sacrificing power. The JSON schema is an implementation detail; users
interact with display names and guided tooling.

### Display name layer

The editor and all user-facing messages use plain-language display names. JSON type keys are
never shown to users.

| JSON type | Display name |
| --- | --- |
| `payee_exact` | Description is exactly |
| `payee_contains` | Description contains |
| `payee_not_contains` | Description does not contain |
| `payee_starts_with` | Description starts with |
| `payee_ends_with` | Description ends with |
| `payee_regex` | Description matches pattern |
| `amount_exact` | Amount is exactly |
| `amount_range` | Amount is between |
| `date_day_of_month` | Day of month is |
| `date_range` | Date is between |
| `account` | From account |
| `label_matched` | Tagged as |
| `entry_direction` | Direction is |
| `entry_type` | Type is |
| `and` | All of these |
| `or` | Any of these |
| `not` | None of these |

### IntelliSense / schema-aware autocomplete

The CM6 editor provides guided authoring — closer to a smart form than a raw text editor:

- **Configurable trigger character** (default `@`, user-settable in preferences, stored in
  `localStorage` for v1). Typing the trigger character opens a palette of condition types by
  display name. Selecting one inserts the correct JSON structure with the cursor placed at
  the value field.
- **Hover / focus tooltips** on any key show a one-line explanation of what the field does
  and what values are valid.
- **Enum fields** (`entry_direction`, `entry_type`, `value` in boolean nodes) autocomplete
  valid options — no guessing or documentation lookup required.
- **`payee_*` value fields** autocomplete from the user's actual `merchant_normalized`
  transaction history.
- **`label` field** in `label_matched` autocompletes from existing label names.
- **Linter messages** use display names: "Description contains requires a non-empty string",
  not "`payee_contains.value` must be non-empty".

### Expanded entry row layout

```text
[Summary row]
[Review panel — if pending_review]
[CM6 conditions editor (left) | Confidence bars (right)]   ← unchanged from today
[Plain-language summary — full width]                      ← new
[Transactions — row count + table]
```

The confidence panel stays in its current position to the right of the editor. The
plain-language summary is a new full-width band between the editor/confidence block and the
transaction table. This creates a natural reading flow: edit → interpret → verify.

```text
Any of these:
  Description contains "NETFLIX"
  Description contains "NFLX"
```

The transaction table shows a **row count** prominently — at-a-glance confirmation of scope.
For `pending_review` entries, it shows the transactions matched in the most recent engine run
labeled "will be". For `active` entries, it shows confirmed assignments labeled "are". Both
read from `transaction_entry_assignments` — there is no separate preview endpoint or runtime
condition evaluation in the web service. Reprocessing is how both states get updated.

### Deeplinks and navigation

- `label_matched` label names render as clickable links in the read-only (non-editing) view.
  Clicking filters the ledger to entries bearing that label.
- `account` account names link to that account's page.

### Dirty indicator

Per-entry save indicator already exists. Page-level dirty state is shown in the ledger header
next to the reprocess button.

### Documentation

For advanced node types (`payee_regex`, boolean composition), inline links open the
Documentation page at the relevant section. See Documentation page below.

---

## "Where used" relationships (configuration page)

Configuration surfaces inverse relationships:

| In config | Shows |
| --- | --- |
| Label detail | Entries with this `label_id`; entries with `label_matched` this label in conditions |
| Account detail | Entries with `account` condition referencing this account |
| Institution detail | Accounts using this institution |

Account creation: the "link institution" step shows existing accounts attached to that
institution as a read-only field.

---

## Implementation order

1. **Schema** — drop `canonical_merchants`, `canonical_merchant_aliases`, `classifications`
   tables; drop `canonical_merchant_id` from `transactions`; rename `node_type = 'label'` in
   `snapshots`
2. **Delete Go files** — `handler/canonical_merchants.go`, `store/canonical_merchants.go`,
   `handler/classifications.go`, `store/classifications.go`
3. **Update `main.go`** — remove route registrations and permissions for deleted handlers
4. **Update `store/conditions.go`** — remove canonical_merchant enrichment branch; keep label
   resolution
5. **Update Rust engine** — stage 1: remove `CanonicalAliasMap` + `CanonicalMerchant` node,
   add `PayeeStartsWith`, `PayeeEndsWith`, `PayeeNotContains`, implement iterative label
   expansion with per-transaction cycle detection; expand `load_entries` to include
   `status IN ('active', 'pending_review')`; `types.rs`: `NodeType::Classification →
   NodeType::Label`; stage 5: rename classification fields to label fields
6. **Update `configuration.templ`** — remove merchants tab; add "where used" panels for labels
   and accounts
7. **Ledger page** — sort/filter controls, Add Entry modal, dirty-state tracking, reprocess
   button
8. **Conditions editor** — `@` shortcut, payee autocomplete from transaction history, deeplink
   rendering for label names
