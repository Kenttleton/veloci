# Veloci UI Design Spec — Job Status and Real-Time Updates

**Date:** 2026-07-13
**Status:** Draft
**Extends:** `2026-07-02-ui-design.md`, `2026-07-12-ui-design-account-review-labels.md`
**Component library:** shadcn/ui (inherited)

---

## Overview

Veloci's processing pipeline is asynchronous. After an import or a rule approval, the API publishes a job and the engine computes in the background. The UI receives SSE events as each stage completes and refreshes affected surfaces incrementally.

The design contract: current data remains readable and usable at all times. Pending treatments signal that a value is about to be replaced — they never block interaction or replace the current value with a spinner. There are no global loading overlays, no sidebar progress indicators, and no toast notifications for job completion.

---

## 1. Pending State Visual Treatment

### Principle

A pending treatment answers one implicit user question: "Is this value about to change?" It must be visible enough to prompt that recognition, and subtle enough not to suggest the current value is wrong or broken. The current value is still valid data — it reflects the last completed job. The pending treatment communicates that fresher data is on the way.

No pulsing animations. Pulsing draws attention continuously and reads as an alarm. A static treatment is sufficient — the user notices it when they look at the surface, not because it demands their attention.

### The pending treatment

A pending element receives two simultaneous CSS changes:

```css
opacity: 0.55;
border-left: 2px solid var(--accent);   /* or outline equivalent per element type */
```

Opacity 0.55 mutes the current value without making it unreadable. It is visually distinct from `--text2` (secondary text at full opacity) and from `--text3` (muted labels). The `--accent` left border is the positive signal — the same color used for the Pulse ring and active controls — indicating activity rather than error.

When the relevant stage completes and fresh data arrives, the pending treatment lifts immediately: opacity returns to 1.0 and the border is removed. No fade — the value updates and the treatment clears in the same render cycle.

### Application per surface

**Summary strip cells (Income, Commitments, Margin):**

Each cell is an independent pending unit. On `account.analyze` or `rules.reprocess`, all three cells enter pending state. On `rule.recalculate` for a single rule, only the cells whose values are affected by that rule's label hierarchy enter pending state — if the recalculated rule contributes to Commitments and Margin but not Income, only those two cells are muted. The left border applies to the cell's outer container.

```text
Income          −   Commitments     =   Margin
$8,625              [░$6,190░]          [░$2,435░]       ← pending cells
/mo · Jun 2026      /mo · Jun 2026      /mo · 28%
[actual]            [actual]            [actual]
```

The proportion bar below the strip also enters pending state (opacity 0.55) when either Commitments or Margin is pending.

**Horizon graph candles:**

Candles for the affected date range enter pending state. On `account.analyze` or `rules.reprocess`, all candles within the job's flux window are affected. On `rule.recalculate`, only candles that include the rule's epoch are affected.

Pending candles: opacity 0.55. The candle body and wick both mute together. The `--accent` left border is not applicable to individual chart candles — lightweight-charts does not support per-candle CSS borders. Instead, a 1px `--accent` dashed vertical band overlays the affected date range, drawn at the chart layer below the candles. This band is drawn at full opacity so the pending range is locatable even with muted candles above it.

Candles outside the affected range remain at full opacity and are fully interactive.

**Stack panel rows:**

Stack rows for affected rules enter pending state. The row background stays at `--surface`; the row's rate value and drift value mute to opacity 0.55; the 2px `--accent` left border applies to the row container. Unaffected rows show normally.

On `rule.recalculate`, only the specific rule's Stack row is pending. Its parent label group header also enters pending state if the label's aggregate rate will change.

**Review queue badge (sidebar nav):**

The badge count enters pending state when `stage.2.complete` has not yet fired for a running job that will affect the queue. The badge number mutes to opacity 0.55. No border treatment — the badge is too small. Instead, the badge receives a 1px `--accent` outline in place of its normal `--surface2` background border. This distinguishes "pending update" from the normal badge without changing its size or position.

### The Details link

Every surface showing a pending treatment includes a quiet "Details" link. This is a small text link, `--text3` color, 11px, appearing immediately to the right of the pending element or below it depending on available space. Arrow character precedes the word: `→ Details`.

The link does not appear until a job affecting that surface is in progress. It disappears when the pending treatment clears.

Clicking "→ Details" navigates to the Activity panel (see Section 2), with the relevant job pre-expanded.

```text
Commitments     $6,190              → Details
░░░░░░░░░░░░    /mo · Jun 2026
```

The link renders at the cell level, not per-value. One "→ Details" per pending surface unit (one per Summary strip cell, one per Stack row, one for the Horizon graph when any candles are pending).

The "→ Details" link on the Horizon graph is anchored to the **right edge of the pending band**, not fixed to the container corner. The band's right-edge x-coordinate is computed via `timeToCoordinate(bandEndDate)` from the lightweight-charts time scale. The link sits immediately to the right of that edge at a fixed vertical offset (e.g., 12px from the top of the chart area). When the pending band is narrow, this keeps the link visually connected to the affected range rather than floating at a disconnected corner. The link is clamped to stay within the container bounds so it never overflows. When the band scrolls entirely out of the visible viewport (panned past), the link is hidden via an `IntersectionObserver` on the chart container.

---

## 2. Activity Panel

The Activity panel is a full canvas view showing all processing jobs for the entity, reverse-chronological. It is accessible from:

- The "→ Details" link on any pending surface.
- A dedicated **Activity** nav item added to the sidebar navigation between "Review" and the accounts section.

**Activity** appears in the sidebar nav permanently — always accessible, positioned below Review and above the accounts list. This is the lowest-priority nav item in the main group. Permanent placement is predictable: users debugging a failed import from days ago should not have to wonder why the nav item disappeared.

---

### Page Layout

```text
Activity                                          [entity_admin: All users ▾]
──────────────────────────────────────────────────────────────────────────────
[Job card — running]
[Job card — finished]
[Job card — finished]
[Job card — failed]
...
```

The topbar shows "Activity" and, for entity_admin users only, a user filter dropdown. The filter defaults to "All users" and allows the admin to filter to a specific user's jobs. Members (non-admin) see only their own jobs; the filter is not shown to them.

Jobs are displayed as a list of cards. Each card is collapsible. The list is cursor-paginated — the initial load shows the most recent N jobs; a "Load older" affordance at the bottom of the list fetches the next page. There is no artificial time cutoff. The `processing_jobs` table retains all records permanently, and the Activity panel is the historical view of that table: most recent at top, full history accessible via pagination.

---

### Job Card — Anatomy

Each job card contains:

```text
┌──────────────────────────────────────────────────────────┐
│ [status dot]  Import — Chase Checking     Jul 8 · 2:34pm │
│               Triggered by Kent                          │
│               3 stages complete · 1 running              │
│                                                [chevron] │
└──────────────────────────────────────────────────────────┘
```

**Status dot:** A 6px filled circle, left-aligned before the job type label. Color by state:

- Running: `--accent`, static (no pulse)
- Finished: `--margin-pos`
- Failed: `--commit`
- Queued (not yet started): `--text3`

**Job type label:** Human-readable, not the internal job_type key.

| `job_type` | Display label |
| --- | --- |
| `import.process` | Import |
| `rules.reprocess` | Rules reprocess |
| `account.analyze` | Recalculate |

The account name follows the job type label after an em dash when the job is scoped to a specific account: "Import — Chase Checking", "Recalculate — Chase Checking". Entity-wide jobs show the job type only: "Rules reprocess."

**Timestamp:** Right-aligned. Date and time of `queued_at`. If today, show time only ("2:34pm"). If earlier, show short date and time ("Jul 8 · 2:34pm").

**Triggered by:** `--text3`, the name of the user who triggered the job. Entity admins see this on all cards; members see it only on their own cards (it is redundant there but shown for consistency).

**Summary line:** One line below "Triggered by." Describes current state in plain language. See per-state rendering below.

**Chevron:** Expands the card to show stage sub-entries.

---

### Job States

#### Queued

```text
○  Import — Chase Checking                     2:34pm
   Triggered by Kent
   Waiting to start
```

Status dot: `--text3`. Summary: "Waiting to start." No chevron — there are no stage entries yet.

#### Running (Started / Processing)

```text
●  Import — Chase Checking                     2:34pm
   Triggered by Kent
   Stage 3 of 7 — Rate computation                   [chevron ∨]
```

Status dot: `--accent`. Summary shows the current stage number, total stages for this job type, and the current stage name. The stage name uses the human-readable form (see stage names below). No spinner — the dot color and the summary text communicate activity without animation.

When expanded, the stage sub-entries show:

```text
  ✓  Stage 0 — CSV import           0.4s
  ✓  Stage 1 — Rule matching        1.2s
  ✓  Stage 2 — Pattern detection    0.8s
  ●  Stage 3 — Rate computation     running...
     Stage 4 — Label mapping        —
     Stage 5 — Trend analysis       —
     Stage 6 — Snapshot write       —
     Stage 7 — Projection           —
```

Completed stages show a checkmark (Lucide `check`, `--margin-pos`) and elapsed time in `--text3`. The current stage shows the status dot. Future stages show a dash in `--text3`.

**Stage display names:**

| Internal stage | Display name |
| --- | --- |
| Stage 0 | CSV import |
| Stage 1 | Rule matching |
| Stage 2 | Pattern detection |
| Stage 3 | Rate computation |
| Stage 4 | Label mapping |
| Stage 5 | Trend analysis |
| Stage 6 | Snapshot write |
| Stage 7 | Projection |

For job types that begin at a stage other than 0, earlier stages are not shown. A `rules.reprocess` job starts at Stage 1 — the card shows stages 1 through 7 only.

#### Finished

```text
✓  Import — Chase Checking                     2:34pm
   Triggered by Kent
   Completed in 4.8s · 312 transactions imported     [chevron ∨]
```

Status dot: `--margin-pos` checkmark (Lucide `check`, same size as dot). Summary: "Completed in Xs" plus relevant metadata. Metadata varies by job type:

| Job type | Metadata shown |
| --- | --- |
| `import.process` | "N transactions imported · N duplicates skipped" |
| `rules.reprocess` | "N rules processed" |
| `account.analyze` | "N snapshots written" |

Finished cards are collapsed by default if the job completed without issue. They can be expanded to show the full stage timeline.

#### Failed

```text
⚠  Import — Chase Checking                     2:34pm
   Triggered by Kent
   Failed at Stage 0 — CSV import              [chevron ∨]
```

Status dot: replaced by a Lucide `alert-triangle` icon in `--commit`. Summary: "Failed at Stage N — [stage name]." The card background receives a very subtle red tint: `background: color-mix(in srgb, var(--surface) 94%, var(--commit) 6%)`. This distinguishes failed cards from finished cards at a glance without making the panel alarming.

When expanded, the failed stage shows the error detail:

```text
  ✓  Stage 0 — CSV import           —
  ✗  Stage 1 — Rule matching        failed

     Error: malformed condition tree on rule "Netflix"
     Rule ID: 3f2a1b...
     [Retry]  [Copy error]
```

The failed stage shows a Lucide `x` icon in `--commit` instead of the checkmark. Error message is shown in a `--surface2` inset block, `--text2`, monospace font. The rule ID or relevant identifier is shown when available.

**Retry affordance:** Appears only for recoverable failures. A failure is considered recoverable when `processing_jobs.error` contains a known-retriable error code (defined by the API). For unrecoverable failures (e.g. malformed CSV that will fail again), no Retry button is shown. The determination of recoverability is made by the API and surfaced as a boolean field on the job response — the UI renders or hides Retry based on that field, without interpreting the error text.

Retry: a small outlined button using `--border` border and `--text2` text. Clicking it re-publishes the job. The card transitions back to Queued state.

**Copy error** copies the raw error string and job ID to the clipboard. All error messages are already scoped through RBAC — entity admins see full errors for all entity jobs, members see full errors for their own. There is no privacy concern in a self-hosted context, and the raw error is genuinely useful for debugging.

---

### Admin vs Member Visibility

| Role | Jobs visible | User filter | Error detail |
| --- | --- | --- | --- |
| `entity_admin` | All entity jobs | Visible (filters by user) | Full, including error message and job ID |
| `entity_user` | Own jobs only | Not shown | Full for own jobs |

The API enforces this scoping. The UI renders based on what the API returns — it does not apply its own role-based filtering.

---

### Empty State

When there are no jobs at all for this entity (or for this user, for members):

```text
No activity yet.

Jobs appear here after you import transactions or make changes that
trigger a recalculation.
```

`--text2` body text, centered in the canvas. No illustration.

If the user is a member viewing their own jobs and no jobs exist, the same message appears — it does not imply admin scope.

---

## 3. Glossary Additions

The following terms should be added to the Glossary page (extends the tables in `2026-07-02-ui-design.md` and `2026-07-12-ui-design-account-review-labels.md`):

| Term | Short definition (tooltip) |
| --- | --- |
| Recalculating | A background job is updating rates, snapshots, or projections. Current values are valid; fresh values will appear when the job completes. |
| Activity | The log of all processing jobs — imports, reprocesses, and recalculations — for your account. |
| Job | A unit of background work triggered by an import or a rule change. Jobs run through multiple stages before completing. |

The tooltip convention (dashed underline + hover definition) applies to "Recalculating" wherever it appears — it does not appear as a label in the current design, but if a future surface uses the term it should carry the tooltip.

"Activity" as a nav label does not receive a tooltip (nav items are not domain vocabulary, they are navigation).

---

## Decisions Log

All decisions originally flagged in this spec have been resolved as of 2026-07-13.

| # | Location | Resolution |
| --- | --- | --- |
| 1 | Horizon graph | "→ Details" anchored to right edge of pending band via `timeToCoordinate()`; clamped to container; hidden when band scrolls out of viewport |
| 2 | Activity nav item | Permanent placement, below Review and above accounts list |
| 3 | Activity panel | No time cutoff; cursor-paginated full history, most recent at top |
| 4 | Failed job detail | "Copy error" included; error strings are RBAC-scoped, no privacy concern |
