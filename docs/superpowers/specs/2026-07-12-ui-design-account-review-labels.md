# Veloci UI Design Spec — Account, Review, and Label Management

**Date:** 2026-07-12
**Status:** Draft
**Extends:** `2026-07-02-ui-design.md`
**Component library:** shadcn/ui + lightweight-charts (inherited)

---

## Overview

This spec covers three sections deferred from the initial UI design: the Account canvas (opened by clicking any account in the sidebar), the Review page (the rule approval queue), and Label management (where labels are created, named, and assigned to rules). All three follow the established layout contract: fixed sidebar, single-canvas view, no modal overlays for core data.

---

## 1. Account Page

### Navigation

Clicking any account row in the sidebar opens the Account canvas. The sidebar account row receives a `--surface2` background to indicate it is the active context — same treatment as the active nav item. The Budget / Reports / Review nav items remain visible and unaffected; clicking one navigates away from the account canvas.

There is no back button. The sidebar is the navigation. Clicking "Budget" in the nav returns to the Budget canvas. Clicking another account opens that account's canvas.

---

### Page Header

The topbar on the Account canvas displays the account name, account type badge, and current balance.

```text
Chase Checking                    Checking          $4,218.00
```

Layout rules:
- Account name uses `--text`, full weight, same size as the "Budget" page title.
- Account type badge (`Checking`, `Savings`, `Credit`, `Loan`, `Mortgage`, `Investment`) renders as a small pill using `--surface2` background and `--text2` text — a neutral classification tag, not a status indicator.
- Balance is right-aligned, `--text`, same size as the name. Credit accounts: if the balance is a liability (negative inflow), show in `--commit`. Otherwise `--text`.
- No rate-format toggle in the account topbar. Rate format is a budget-level concept; account pages display absolute dollar amounts throughout.

---

### Tab Strip

Below the topbar, a two-tab strip switches between the processed view and the raw audit view.

```text
[Transactions]  [Imports]
```

Default tab on open: **Transactions**.

Tab strip styling: tabs are flush left, `--text2` color when inactive, `--text` when active. The active tab has a 2px `--accent` underline. No border around the strip. The strip sits on `--bg` with a `--border` separator line below it.

---

### Transactions Tab

The Transactions tab shows this account's processed activity: every transaction that has been matched to a rule, organized by rule. Rules in `status = 'pending_review'` are shown in a visually distinct treatment so the user knows which groupings are provisional.

#### Grouping model

Transactions are grouped by rule. Each rule forms one collapsible section. Within each section, transactions are sorted by date descending. Rules with no matched transactions in the visible window are not shown.

The Transactions tab shows all-time matched transactions for this account. There is no date cutoff. The list is cursor-paginated within each rule group — the initial load shows the most recent N transactions per group; a "Load more" affordance at the bottom of each expanded group fetches older transactions within that group. A globally visible "Load more" never appears; pagination is per-group to preserve the grouping structure.

#### Rule group header

Each group header row contains:

```text
[chevron]  Rule name           Label pill    N transactions   Total amount
```

- **Chevron** — expand/collapse the group. Groups are collapsed by default when there are more than five rules visible.
- **Rule name** — `--text`, full weight.
- **Label pill** — small rounded pill showing the label this rule outputs. Background `--surface2`, text `--text2`. If the rule has no assigned label yet, the pill reads "No label" in `--text3`.
- **Transaction count** — `--text2`, e.g. "4 transactions".
- **Total amount** — right-aligned, `--text`, showing the net total of matched transactions in the visible window. Expense rules show in `--commit`; income rules show in `--income`.

**`pending_review` rule treatment:** Groups whose rule has `status = 'pending_review'` receive:
- A `--border` left border, 3px, using `--accent` color (same as the Pulse ring) — signals "this grouping is proposed, not confirmed."
- Rule name rendered in `--text2` instead of `--text` — slightly muted, not alarming.
- A `pending` badge (small pill, `--surface2` background, `--text3` text) after the rule name.
- The label pill is absent — pending rules have no approved label assignment.

This treatment communicates "the engine grouped these transactions tentatively; they are not yet part of your budget" without any alarm language.

#### Transaction rows (within an expanded group)

| Column | Width | Content |
| --- | --- | --- |
| Date | 96px | `MMM D` format, `--text2` |
| Merchant name | flex | `merchant_normalized` value, `--text` |
| Label pills | auto | Labels applied to this transaction. One pill per rule assignment. Small rounded pills, `--surface2` background, `--text2` text. |
| Amount | 88px | Right-aligned. Expense: `--commit`. Income: `--income`. |

Transaction rows have a `--surface` background. On hover, background shifts to `--surface2`. Clicking a row expands an inline detail panel below it, anchored within the group. The expand is a smooth height transition; only one row per group is expanded at a time.

**Inline detail panel contents:**

- **Rules matched:** A column list of every rule that matched this transaction, with its confidence score (e.g. "Netflix · 0.94"). Each rule name is a link that navigates to the rule editor. Confidence is shown as a small `--text3` value to the right.
- **Raw import:** The original `imported_payee` string, amount, and import batch date ("Chase Checking · Jul 8, 2026"). No normalization detail — the normalized form is already visible in the row above.
- **Import batch link:** A text link "View in Imports tab" that switches to the Imports tab and scrolls to the matching batch group.

The panel closes when the row is clicked again or when another row is expanded.

#### Empty state — Transactions tab

When the account has no matched transactions (fresh account after import, or import pending):

```text
No matched transactions yet.
Transactions appear here once the engine has processed this account's imports
and patterns have been matched to rules.

[Go to Review →]
```

`Go to Review` is a text link navigating to the Review page.

---

### Imports Tab

The Imports tab is the raw audit layer. It shows every row from `raw_transactions` for this account, annotated with deduplication metadata. There is no rule context here — purely what was imported and how deduplication handled it.

#### Import batch grouping

Rows are grouped by `import_batch_id`. Each import batch forms one collapsible section.

Batch group header:

```text
[chevron]  Imported Jul 8, 2026    Chase Checking CSV    N rows   N duplicates skipped
```

- Import date is the `import_batches.processed_at` date.
- File label is the source account name and format type. `--text2`.
- Row count: total `transactions_imported` from the batch record.
- Duplicates skipped: `transactions_skipped_duplicate` from the batch record. If zero, this field is omitted.

Batch groups are expanded by default on initial load, most recent batch at top. Older batches collapse automatically if there are more than three batches.

#### Raw transaction rows

| Column | Width | Content |
| --- | --- | --- |
| Date | 96px | `MMM D` format, `--text2` |
| Raw merchant | flex | `imported_payee` (exact bank string), `--text` |
| Normalized | flex | `merchant_normalized`, `--text3` — shows what the engine did |
| Amount | 88px | Right-aligned, `--text` |
| Import batch | auto | Batch date as a small `--text3` chip |
| Status | auto | See below |

**Duplicate row treatment:** Rows that were skipped as duplicates are shown with:
- All text in `--text3` (full mute).
- A strikethrough on the raw merchant name.
- A `duplicate` badge: small pill, `--surface2` background, `--text3` text.
- Row background stays at `--surface` — no color change. The muted text and badge are sufficient.

Duplicates are shown rather than hidden because this tab is an audit surface. A user troubleshooting a missing transaction needs to confirm it was intentionally skipped, not silently dropped.

**Normal row status:** Rows with no dedup flag have no status badge. Their presence in the list confirms they were accepted.

#### Filtering

A filter bar sits above the batch groups:

```text
[All batches ▾]   [Show duplicates ●]   [Search merchant...]
```

- **All batches** — dropdown to filter to a single import batch.
- **Show duplicates** — toggle. On by default. When off, duplicate rows are hidden entirely.
- **Search merchant** — filters by `imported_payee` or `merchant_normalized`, substring match, case-insensitive.

#### Empty state — Imports tab

When the account has no imports yet:

```text
No imports for this account.
Use the import button in the sidebar to upload a CSV.
```

---

## 2. Review Page

The Review page is the queue for all engine-detected events requiring a human decision. It is reached via the "Review" nav item in the sidebar. The sidebar nav badge count on "Review" reflects `COUNT(*) WHERE review_queue.status = 'pending'`.

---

### Page-Level Layout

```text
Review                         [new: 3]  [drift: 1]  [ended: 2]
──────────────────────────────────────────────────────────────────
[Card]
[Card]
[Card]
...
```

The topbar shows the page title "Review" and three filter badges. Each badge is a pill button showing the alert type and its count of pending items. The active filter is filled (`--accent` background, white text); inactive filters are outlined (`--border` border, `--text2` text).

By default all types are shown — all three badges are active. Clicking a badge toggles that type off/on. The queue below filters in real time.

When a filter produces zero matching items within its type, the badge still appears but shows `0` and is visually disabled (lowered opacity, no click action).

Cards are arranged in a single scrollable column, full canvas width minus standard padding. No multi-column layout — each card warrants full reading attention.

---

### Review Card Anatomy (shared elements)

Every card, regardless of alert type, has:

- **Card container:** `--surface` background, `--border` border, 4px rounded corners, standard inner padding. No shadow — consistent with the rest of the UI.
- **Alert type badge:** top-left. `new` in `--accent`, `drift` in `--commit`, `ended` in `--text3`. Text is always uppercase in the badge.
- **Rule name (or suggested name):** prominent, `--text`, full weight, below the badge.
- **Body:** type-specific content (see each section below).
- **Action row:** bottom of card. Right-aligned action buttons. See per-type sections.
- **Confidence component:** present on `new` and `drift` cards; absent on `ended` cards. See Confidence Component section below.

Cards are ordered in a fixed sequence: all `new` cards first, then all `drift` cards, then all `ended` cards. Within each type, cards are sorted by confidence descending — higher-confidence items surface first. This ordering is intentional: `new` patterns drive rate accuracy and need homeostasis approval most urgently; `drift` adjustments follow; `ended` decisions are informational. There is no user-configurable sort.

---

### Card: `new` Alert Type

A new pattern was detected. The engine proposes creating a rule.

```text
┌──────────────────────────────────────────────────────────┐
│ NEW                                                       │
│ Netflix                                                   │
│                                                           │
│ 4 transactions  ·  $15.99 / mo  ·  monthly on the 7th    │
│                                                           │
│ Sample transactions:                                      │
│   Jul 7       NETFLIX.COM          $15.99                 │
│   Jun 7       NETFLIX.COM          $15.99                 │
│   May 7       NETFLIX.COM          $15.99                 │
│                                                           │
│ [Confidence component — see below]                        │
│                                                           │
│ Label:  [No label ▾]    Type:  Standing ▾                 │
│                                                           │
│ [Dismiss]    [Edit rule]    [Approve →]                   │
└──────────────────────────────────────────────────────────┘
```

Type dropdown options: **Standing**, **Variable**, **One-time**. There are no Hit or Boost types — direction (income/expense) on the rule carries that semantic. Pre-populated from `suggested_entry_type`.

```text
┌──────────────────────────────────────────────────────────┐
└──────────────────────────────────────────────────────────┘
```

**Body fields:**
- Transaction count, proposed rate in the active rate format, and timing summary (e.g. "monthly on the 7th", "weekly on Fridays", "every 14 days"). These are derived from the `review_queue` record's `suggested_entry_type`, `suggested_rate_per_day`, and the rule's `recurrence_anchor`.
- Up to three sample transactions from `sample_merchants`, shown as a compact list. Date (`MMM D`), raw merchant string, amount. If there are more than three, a "show N more" link expands the list inline.

**Inline assignment fields:**
- **Label** — a compact select (`--surface2` background, `--border` border) showing the currently assigned label or "No label." Opening it shows existing labels with a "New label..." option at the bottom. Creating a label from here opens an inline input immediately below — no navigation required.
- **Type** — entry type select pre-populated from `suggested_entry_type`. Options: Standing, Variable, One-time. Changes here update what the rule will be created as.

These fields sit between the confidence component and the action row. They allow the user to make the two most common adjustments (label assignment and type correction) before approving without opening a full editor.

**Actions:**
- **Dismiss** — sets `review_queue.status = 'rejected'`, sets `rule.status = 'inactive'`. Removes the card from the queue. Text button, `--text3`, leftmost.
- **Edit rule** — opens the rule editor inline (see DECISION NEEDED below). Outlined button, `--text2`.
- **Approve** — sets `review_queue.status = 'approved'`, sets `rule.status = 'active'`. Triggers `account.analyze` job. Filled button, `--accent` background.

**"Edit rule" behavior:** Clicking "Edit rule" expands an inline panel directly below the card body. The inline panel covers common adjustments: name, type, label, period, and projected rate. Condition editing (the JSONB condition tree) opens the full dedicated rule editor page. The inline panel and the full rule editor are not mutually exclusive — the full editor contains all fields, including the simple ones available inline. The inline path is for quick corrections during review; the full editor is for anything requiring condition logic.

**Post-approve behavior:** The card collapses and slides out of the list. The queue count badge decrements. No toast, no banner. If the approval triggers a job that updates the Margin, the Summary strip on the Budget canvas updates silently the next time the user navigates there — the data refreshes but no notification is pushed. This is consistent with the principle that budget health is communicated through data, not alerts.

---

### Card: `drift` Alert Type

An existing active rule's observed pattern has changed. The old and new patterns are shown side by side.

```text
┌──────────────────────────────────────────────────────────┐
│ DRIFT                                                     │
│ Rent — 123 Main St                                        │
│                                                           │
│ Pattern changed                                           │
│                                                           │
│   Was               Now                                   │
│   $1,500.00 / mo    $1,550.00 / mo    +$50.00 / mo       │
│   monthly on 1st    monthly on 1st    (timing unchanged)  │
│                                                           │
│ Based on 2 new transactions                               │
│   Jul 1   LANDLORD LLC   $1,550.00                        │
│   Jun 1   LANDLORD LLC   $1,550.00                        │
│                                                           │
│ [Confidence component — see below]                        │
│                                                           │
│ How should this change be recorded?                       │
│                                                           │
│ ○ Correction — update this rule in place and recompute    │
│               history from the start of the current epoch │
│                                                           │
│ ○ Version    — close the current rule version and open    │
│               a new one starting today                    │
│                                                           │
│ [Dismiss]    [Edit rule]    [Accept change →]             │
└──────────────────────────────────────────────────────────┘
```

**Body fields:**
- "Pattern changed" subheading.
- Two-column comparison: Was (old projected rate, old timing) vs Now (new observed rate, new timing). The delta between Was and Now is shown as a signed value to the right: `+$50.00/mo` in `--commit` for an increase, `-$30.00/mo` in `--income` for a decrease. If timing changed, timing shows the delta too; if timing is unchanged, "timing unchanged" appears in `--text3`.
- Brief "Based on N new transactions" line with the triggering transactions listed compactly.

**Correction vs. Version choice (required before Accept):**

The user must select how this drift should be recorded before "Accept change" is enabled. The two options correspond to different engine outcomes:

- **Correction** — the prior epoch was incomplete data; the rule should be updated in place. `projected_rate_per_day` is updated, and the engine reprocesses from the current epoch's `epoch_start` using the corrected rule. Example: a gas bill appeared low in summer but that was the only data; the fall/winter amount is the true baseline.
- **Version** — this is a genuine commitment change, not a data correction. The current epoch closes (`epoch_end = today`), and a new epoch opens (`epoch_start = today`) with the updated amount. Rate history before today reflects the old commitment; history after reflects the new. Example: Netflix raises their price — that is a new phase of the commitment.

The distinction is **user-determined**, not detected automatically. Both cases look identical in the data (an amount increase with a single occurrence). The user knows the context.

"Accept change" is disabled until one option is selected. A `--text3` note reads: "Choose how to record this change before accepting."

**Manual override warning:** If the rule has a user-set `projected_rate_per_day` (a manual projection override), a one-line note appears below the comparison before the choice: "You previously set a custom projection of $X/mo — accepting this change will replace it." This is informational, not a blocking confirmation.

**Actions:**

- **Dismiss** — ignores the detected drift. The rule retains its prior projected rate. Sets `review_queue.status = 'rejected'`. The drift will likely re-appear on the next import if the pattern holds.
- **Edit rule** — same as `new` card.
- **Accept change** — enabled only after Correction or Version is selected. Executes the chosen outcome. Triggers `account.analyze` (Correction) or a full `rules.reprocess` from epoch_start (Version). Filled `--accent` button.

---

### Card: `ended` Alert Type

An active rule's expected transaction has stopped arriving. The engine detected signal expiry (the 3-strike mechanism from Stage 3).

```text
┌──────────────────────────────────────────────────────────┐
│ ENDED                                                     │
│ Disney+                                                   │
│                                                           │
│ Last seen: Jun 12, 2026                                   │
│ Expected next: Jul 12, 2026   (45 days overdue)          │
│ Rate: $10.99 / mo                                         │
│                                                           │
│ What happened?                                            │
│                                                           │
│ ○ Temporary gap — keep rule active                        │
│                                                           │
│ ○ Ended on:  [Jun 12, 2026  ▾]                            │
│                                                           │
│ [Dismiss]    [Confirm →]                                  │
└──────────────────────────────────────────────────────────┘
```

**Body fields:**
- Last seen date (from the rule's last matched transaction date).
- Expected next date (from `next_due_date`) and how many days overdue.
- The rule's current rate.

No confidence component on `ended` cards. The absence of an expected transaction is already confirmed by the engine. The question is what the user wants to do about it.

**Choice model:**

The user selects one of two radio options before "Confirm" is enabled:

- **Temporary gap — keep rule active** — the commitment is paused, not ended. The rule stays active. `review_queue.status = 'rejected'`. The rule continues to project forward and will re-appear in the queue if the signal stays absent.
- **Ended on [date]** — the commitment is over. Sets `rule_epochs.epoch_end` to the selected date and `terminated_by_user_id` to the acting user. Sets `review_queue.status = 'approved'`. Triggers `account.analyze`. The rule is removed from active projections; its history is preserved for the Horizon chart.

The date picker in the "Ended on" option defaults to the last seen date (last matched transaction date). The user can adjust it — for example, they cancelled a subscription on a specific date that differs from the last charge. The date is used as `epoch_end`; transactions before that date remain in the rule's history.

"Confirm" is disabled until a radio is selected. The date picker acts as both the configuration mechanism and the confirmation intent — selecting "Ended on" and picking a date is the confirmation. There is no secondary confirmation step.

---

### Confidence Score Component

Shown on `new` and `drift` cards. Displays the composite confidence score prominently, with the three sub-scores as secondary detail.

```text
Confidence  0.87

  Merchant   ████████████████░  0.94
  Timing     █████████████░░░░  0.79
  Amount     ████████████████░  0.95
```

Layout:
- "Confidence" label in `--text3`, score value large in `--text`. Score color: ≥0.7 in `--margin-pos`, 0.3–0.69 in `--text2`, <0.3 not shown (filtered by engine).
- The three sub-scores follow below, each on one line.
- Sub-score label (`--text2`), a thin horizontal bar (4px height, `--surface2` track, `--accent` fill proportional to score), and the raw score value (`--text3`).
- The breakdown section is collapsed by default on `drift` cards (where the composite score is already known to be reliable — the rule was previously approved). On `new` cards the breakdown is expanded by default.
- A toggle link "Show detail" / "Hide detail" in `--text3` toggles the sub-score rows.

Sub-score definitions follow the tooltip convention (dashed underline on each label):
- `merchant_confidence` — "Are all these transactions from the same business?"
- `timing_confidence` — "Is there a consistent cadence?"
- `amount_confidence` — "Are the amounts consistent?"

These short definitions appear on hover. Full definitions belong in the Glossary.

---

### Empty State — Review Queue

When all cards have been actioned and the queue is empty:

```text
Queue clear.

No patterns are waiting for review. New patterns will appear here
after your next import.
```

`--text2` body text, centered in the canvas. No illustration, no color treatment. The absence of items is itself the data.

If filters are active and the filtered view is empty but the full queue is not:

```text
No drift alerts pending.

[Show all types]
```

"Show all types" resets the filter to show all cards.

---

## 3. Label Management

Labels are named groupings that rules output. Each rule has exactly one `label_id`. The label hierarchy (leaf rules outputting leaf labels; post-stage rules aggregating leaf labels into aggregate labels) is expressed through rule conditions.

Label management does not require a dedicated page. It lives in two places: inline on the Review page (creating and assigning during rule approval) and in a Settings panel (bulk management).

---

### Label Assignment During Review

When approving a `new` card on the Review page, the Label select (described above in the `new` card anatomy) is the primary assignment surface. Creating a new label from the select dropdown:

1. User opens the label select and chooses "New label...".
2. An inline text input appears immediately below the select, pre-focused.
3. User types the label name and presses Enter or clicks "Create."
4. The new label is created and immediately selected in the card's label field.
5. On Approve, the rule is created with this `label_id`.

No navigation, no page change. The full flow takes one extra step in the review card.

Label rename from the Review page is not supported — renaming belongs to the Settings panel. If the user wants to rename before approving, they should do it in Settings first, then return to Review.

---

### Labels in Settings

The Settings page (accessed from the sidebar footer) gains a **Labels** section. This is the canonical CRUD surface for labels.

```text
Settings
  Account
  Labels        ← new section
  Import
  ...
```

The Labels section within Settings:

```text
Labels

[+ New label]

  Name                   Used by    Actions
  ─────────────────────────────────────────
  Housing                3 rules    [Rename]
  Streaming              2 rules    [Rename]
  Food & Dining          5 rules    [Rename]
  ...
```

Layout:

- Table with two columns: Name (`--text`), "Used by N rules" (`--text2`), and a Rename action.
- **Rename** — inline: click the label name (or the Rename button) to make it editable in place. Press Enter to save, Escape to cancel. Duplicate names show a validation message inline: "A label with this name already exists."
- **No delete action.** Labels are permanent — there is no concept of deleting a label. A label without a name (empty string) is valid and means no pill is shown for rules that output it. Label identity is always the UUID; the name is only for display.
- **New label** — text button top-left. Adds an empty inline row at the top of the table with a focused text input.

The Settings Labels table is a name management surface only. Rule-to-label assignment is done on the Review page (during approval) or in the rule editor.

**Label reassignment:** There is no bulk reassign flow. When the user wants to move rules from one label to another, they do so in the rule editor for each rule. The UI refreshes the "Used by N rules" counts automatically as changes are saved. In the edge case where multiple rules share the same label and the user approves a pattern that conflicts, the rules-level editor is the resolution surface.

---

### Label Rule Assignment Outside Review

After approval, a rule's label can be changed through the rule editor. The rule editor is not fully specced here (deferred to the rule editor spec), but the label field within it follows the same select pattern as the Review card: existing labels in a dropdown, "New label..." at the bottom.

---

### How Labels Appear in the Transactions Tab

In the Transactions tab on the Account canvas, labels appear as pills on each rule group header (the label this rule outputs) and as individual transaction-level pills (the full set of labels applied via all matching rules).

**Rule group header label pill:** Shows the single output label for that rule. `--surface2` background, `--text2` text, 3px border-radius. Clicking the label pill in the header navigates to nothing in v1 — it is informational only.

Clicking a label pill in the Transactions tab adds it as an active filter chip in the filter bar, narrowing the view to only rule groups whose output label matches. This covers the "what is my Streaming spend?" workflow. The filter applies to output labels only — not to the input labels in a rule's condition tree. When "Streaming" is filtered, the transactions from the Netflix, Hulu, and HBO rules appear because those rules output the Streaming label; their individual transaction-level pills (Netflix, Hulu, etc.) are visible naturally. A "Not Labeled" chip appears in the filter bar for rules with an empty label name. Filter chips can be cleared individually or all at once.

**Transaction-level label pills:** When a transaction row is expanded (or if rows show pills inline), each label pill corresponds to one rule assignment. Multiple pills per transaction reflect the pre/post rule hierarchy — a Netflix charge legitimately carries both a "Netflix" label (leaf) and "Streaming" label (aggregate). Pills are shown in a wrapping flex row.

---

### How Labels Appear in the Stack Panel

In the Stack panel on the Budget canvas, label names are the group headers. Each label with a non-zero rate in the selected Pulse period appears as a category header row, with its member rules indented below it.

The Stack panel is driven by label hierarchy — this is unchanged from the existing spec's "Category headers" behavior. Label renaming in Settings propagates immediately to the Stack because all references use `label_id` and the name is fetched from the `labels` table at render time.

Labels with no active rules contributing to the Pulse period are hidden from the Stack. They do not appear as empty rows.

---

## Glossary Additions

The following terms should be added to the Glossary page (extends the table in `2026-07-02-ui-design.md`):

| Term | Short definition (tooltip) |
| --- | --- |
| Rule | A named pattern that matches transactions and converts them to a $/day rate. |
| Label | A named group that one or more rules output to. Labels are the categories you see in the Stack. |
| Confidence | How certain the engine is that a detected pattern is real. Score from 0 to 1. |
| Epoch | The active lifespan of a rule's signal. A new epoch begins when a rule is approved; it ends when the commitment stops. |
| Pending | A rule detected by the engine but not yet reviewed. Pending rules are excluded from your budget until approved. |
| Drift (rule) | A detected change in an active rule's amount or timing pattern. Requires review to accept or dismiss. |
| Ended | An active rule whose expected transaction has not arrived. May indicate a cancelled subscription or temporary gap. |

The tooltip convention (dashed underline + hover definition) applies to all new terms wherever they appear in the Review page, Account page, and Stack panel.

---

## Decisions Log

All decisions originally flagged in this spec have been resolved as of 2026-07-13.

| # | Location | Resolution |
| --- | --- | --- |
| 1 | Transactions tab | All-time, cursor-paginated per rule group |
| 2 | Transaction rows | Inline expanding row with rule matches, raw import, and batch link |
| 3 | `new` card | Inline panel for simple fields (name, type, label, period); full rule editor for conditions |
| 4 | `drift` card | Show one-line warning when a manual projection will be overwritten |
| 5 | `ended` card | Radio + date picker; date selection acts as confirmation, no separate step |
| 6 | Card sort | Fixed order: new → drift → ended; confidence desc within each type; no user sort |
| 7 | Labels in Settings | No bulk reassign; no delete concept; rename-only management |
| 8 | Label pills | Clicking adds output label as filter chip; "Not Labeled" chip for empty-name rules |
