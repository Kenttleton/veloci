# VELOCI

*Personal Financial Velocity*


**Product Specification**

Version 0.1 — June 2026

*Draft — Confidential*

# 1. Product Vision

Veloci is a local-first personal finance application built around the concept of financial velocity — the rate at which money moves into and out of your life. Rather than treating finances as static snapshots of account balances, Veloci expresses every financial commitment and income source as a daily rate, making the true cost of subscriptions, debt, and obligations immediately comparable and viscerally understandable.

We live in a subscription economy layered on top of persistent debt. Car manufacturers charge monthly fees to unlock features already installed in your vehicle. Streaming services multiply quietly. Minimum payments obscure the true cost of borrowing. Veloci exists to make these dynamics visible — not by lecturing users about their choices, but by showing the numbers in a form that connects to how people actually experience money.

> A $20/month Netflix subscription costs $0.66/day. A user earning $15/hour works 1.3 hours a month to pay for it. Neither fact is hidden — they just aren't shown. Veloci shows them.

## 1.1 Core Philosophy

- Matter of fact, not prescriptive — the app shows impact, never judges choices

- Users are intelligent adults who need better visibility, not guidance on what is optional

- Accrual-based thinking — your true daily cost includes things that haven't billed yet

- Self-healing — the system corrects itself as new data arrives, requiring minimal maintenance

- Local-first — your financial data stays on your hardware

## 1.2 Target Audience

- Primary: Self-hosting community — privacy-conscious users comfortable running their own infrastructure

- Secondary: Households managing shared finances across multiple accounts

- Broader: Anyone whose financial intuition doesn't match their financial reality

# 2. Financial Model

The financial model is the foundation of Veloci. All calculations derive from a single atomic unit: the daily rate (/day). Every income source, expense, debt payment, and one-time event is expressed as a rate per day. Display layers scale this unit to monthly, quarterly, or yearly figures, but the underlying model never changes units.

## 2.1 The Daily Rate as an Atomic Unit

Storing everything as a /day rate gives the model three properties that make it powerful:

- Universal comparability — a weekly expense and an annual expense become directly comparable

- Simple scaling — monthly = rate × 30.44, quarterly = rate × 91.31, yearly = rate × 365

- Honest arithmetic — your true position is income rate minus all committed expense rates, continuously

## 2.2 Two Lanes: Projection and Actual

Every financial event exists in two lanes simultaneously. The gap between them is Drift — the core diagnostic metric of Veloci.

| **Lane** | **Description** |
| --- | --- |
| **Projection** | The expected rate derived from known commitments and estimated income. This is your budget, built automatically from detected patterns rather than manual entry. |
| **Actual** | The real rate derived from imported transaction data. This is what happened. |
| **Drift** | The delta between Projection and Actual, expressed as a /day rate. Positive drift means you are ahead of projection. Negative drift means commitments are outpacing expectations. |

## 2.3 Entry Types

Every entry has a signal type. The type tells the engine how to detect recurrence and compute a rate — it applies equally to income and expense entries.

| **Type** | **Description** |
| --- | --- |
| **Standing** | Regular cadence, consistent amount. Rent, subscriptions, loan payments, salary. The engine detects the interval and amount; rate = amount ÷ period_days. |
| **Variable** | Regular cadence, inconsistent amount. Groceries, utilities, fuel. The engine detects the interval; rate is computed as the rolling average or maximum over the window, per the entry's setting. |
| **Irregular** | No regular cadence — timing confidence falls below the thresholds required for Standing or Variable. The engine groups matching transactions by merchant and sets `period_days` from the mean observed interval if more than one transaction is found; defaults to 30 with only one data point. Car repairs, gas stations, freelance income, annual insurance. |

## 2.4 Smoothing

Smoothing converts raw transaction amounts into a stable /day rate using a window called `period_days`. Without smoothing, a $500 car repair would appear as $500/day on the day it posts and $0 afterward. With a 30-day window, it spreads to $16.67/day — which is the honest answer to "what does maintaining a car actually cost per day."

The rate the user sees is always the smoothed rate: rolling transaction total ÷ period_days.

`period_days` is set by the engine from detected recurrence and can be overridden per entry:

- **Standing and Variable entries**: `period_days` is detected automatically from the transaction pattern — 30 for a monthly subscription, 7 for a weekly grocery run, 91 for a quarterly payment. The engine updates this as new data arrives.

- **Irregular entries**: `period_days` is set from the mean observed interval when 2 or more transactions are available; defaults to 30 when only one exists. Users can override — for example, setting 365 for a known annual expense like insurance or registration.

> Smoothing is a per-entry setting, not a category. The rate displayed is always the smoothed figure; users can inspect and override `period_days` in the entry editor.

## 2.5 Household and Multi-Account Model

Veloci treats the budget as unified above the account level. An account is metadata on a transaction, not a structural boundary. This means a couple sharing one joint account and each maintaining a personal account operates identically to a single user managing multiple accounts for different purposes.

- All Active accounts contribute flows to the unified budget

- Transfers between accounts cancel at the budget level — one account sees a debit, the other a credit, net impact is zero

- Transfer cancellation is detected by matching amounts and approximate timing, Smoothed over 30 days

- Passive accounts are tracked and projected but do not affect Margin

## 2.6 Planned Account Type Expansions

Version 1 supports checking and savings accounts. Three additional account types are planned for subsequent releases, each requiring distinct rate modeling.

**Debt accounts (v1.1)** — Credit cards, auto loans, personal loans, mortgages. Treated as Passive. Expose minimum payment rate (the committed /day outflow from the Active budget), true cost rate (principal plus interest over remaining term as /day), and payoff projection (what-if modeling for accelerated payments).

**Interest-bearing accounts (v1.2)** — High-yield savings and similar accrual accounts. Can be added as Active in v1 but interest income appears as irregular entries with no special modeling. v1.2 introduces proper interest accrual: the projected interest rate is computed continuously and contributes to the income lane as a standing rate rather than as sporadic deposits.

**Market-based accounts (v1.3)** — Money market, investment brokerage, and cryptocurrency accounts. These carry market risk and require return-rate modeling distinct from fixed-rate accrual. Cryptocurrency may ship as a separate feature track within v1.3.

# 3. Data Model

The core data model is intentionally minimal. All insight, reporting, and projection is derived from these structures through calculation rather than stored state.

## 3.1 Account

| **Field** | **Description** |
| --- | --- |
| **id** | Unique identifier |
| **name** | User-defined display name |
| **type** | checking · savings · credit · loan · mortgage · investment |
| **status** | Active · Passive — whether this account participates in Margin calculation |
| **interest_rate** | APY for savings projection or APR for debt cost calculation |
| **balance** | Current balance snapshot from latest import |
| **credit_limit** | Applicable for credit accounts |

## 3.2 Entry

An Entry is the atomic unit of the financial model. Every income source and expense is an Entry. Multiple entries may share one label — a label names a financial signal across its entire lifecycle, while each entry represents one continuous rate instance of that signal.

| **Field** | **Description** |
| --- | --- |
| **id** | Unique identifier |
| **entity_id** | Owning entity (household or individual) |
| **label_id** | Reference to the global label naming this signal — nullable for user-created entries without auto-matching |
| **direction** | `income` · `expense` |
| **entry_type** | `standing` · `variable` · `irregular` |
| **period_days** | Amortization window in days. Default 30. Drives rate and smoothing calculations |
| **variable_method** | `avg` · `max` — applicable to Variable entries only |
| **projected_rate_per_day** | Expected /day rate — set by the engine from pattern detection or by the user |
| **conditions** | JSONB matching rules for transaction auto-assignment — nullable for manual entries |
| **priority** | Lower value = matched first when multiple entries compete for a transaction |
| **status** | `pending_review` · `active` · `inactive` |
| **source** | `user` · `engine` — indicates whether this entry was created manually or detected |
| **recurrence_anchor** | Expected day-of-month or cron-style anchor for recurring entries |
| **next_due_date** | Engine-computed date of the next expected transaction |
| **project_tentatively** | When TRUE, Stage 7 includes this pending_review entry in projections |
| **pending_amount_cents** | Forward-versioned amount — applied when `computed_as_of` reaches `pending_effective_date` |
| **pending_effective_date** | Date on which `pending_amount_cents` becomes the active rate |
| **start_date** | First date this entry instance was active — set to the earliest matching transaction date |
| **end_date** | Date this instance was closed — NULL means currently active |

## 3.3 Transaction

Transactions are immutable once inserted. Financial columns (date, amount_cents, imported_payee) never change. Positive `amount_cents` = inflow (income, credit); negative = outflow (expense, debit).

| **Field** | **Description** |
| --- | --- |
| **id** | Unique identifier |
| **entity_id** | Owning entity |
| **account_id** | Source account |
| **import_batch_id** | Import run that inserted this row |
| **date** | Transaction date |
| **amount_cents** | Raw transaction amount in cents (positive = inflow) |
| **imported_payee** | Raw merchant string from bank export — immutable |
| **merchant_normalized** | Normalized merchant name produced by Stage 0 |
| **imported_id** | Bank-supplied dedup ID from the CSV — nullable |
| **settlement_status** | `flux` · `settled` — set at insert time, never changed |

## 3.4 Derived Values

Some values are computed by the engine and stored in `snapshots` or `projections`; others are computed by the API at query time and never written to the database.

**Stored in `snapshots`** — one row per node (entry or classification) per calendar day:

| **Value** | **Field** | **Description** |
| --- | --- | --- |
| **Drift** | `drift_per_day` | Actual rate minus projected rate. Positive = actual exceeds projected |
| **Slope** | `slope_per_day` | Linear regression slope of actual rate over recent snapshot history — indicates whether a signal is trending up or down |

**Stored in `projections`** — one row per entity per projected date:

| **Value** | **Field** | **Description** |
| --- | --- | --- |
| **Committed rate** | `commitment_rate_per_day` | Sum of all active expense projected rates at a given date |
| **Margin** | `margin_rate_per_day` | Income rate minus committed rate — the /day surplus or deficit |
| **Pinch point** | `is_pinch_point` | TRUE on any projected date where margin drops below zero |

**Computed at display layer** — derived from stored rates by the API, never persisted:

| **Value** | **Derivation** | **Status** |
| --- | --- | --- |
| **Discretionary rate** | `margin_rate_per_day / income_rate_per_day × 100` | v1 — both inputs already exist in `projections`; computed at query time, no new storage needed |

# 4. Import and Detection Cycle

Veloci is local-first and import-driven. Users export CSV transaction data from their financial institutions and import it manually — import events create a natural reconciliation rhythm and the app never requires credentials to third-party financial services. Bank sync is a later version consideration.

## 4.1 Institution Normalization

Every bank exports CSV in a slightly different format. On first import from a new institution, Veloci presents a column mapping interface: which column is date, which is amount, which is merchant. This mapping is saved and applied automatically on all future imports from the same institution. The mapping also stores the settlement window — how many days after import a transaction is considered authoritative — and the sign convention for amounts.

## 4.2 Transaction Import

Once the column mapping is applied, imported rows are normalized and written to `transactions`. Merchant names are standardized (punctuation stripped, whitespace collapsed, common abbreviations resolved). Each row is assigned a settlement status at insert time: `settled` if the transaction date is older than the institution's settlement window, `flux` otherwise. Flux rows near the import boundary may be superseded by a later overlapping import; settled rows are immutable.

Deduplication runs against existing transactions using a configurable date window and amount tolerance to handle the same transaction appearing in overlapping CSV exports from the same institution.

## 4.3 Entry Matching

After import, each new transaction is evaluated against all active entries in priority order. An entry matches when its conditions — merchant pattern, amount range, and any other configured criteria — align with the transaction. Matched transactions are written to `transaction_entry_assignments`. The engine updates `next_due_date` on any entry whose match advances the expected recurrence.

Transactions that match no existing entry are passed to pattern detection.

## 4.4 Pattern Detection

Unmatched transactions are clustered by normalized merchant name and scored across three components:

- **Merchant** — how consistently the merchant name resolves to a single entity
- **Timing** — how regular the intervals between occurrences are
- **Amount** — how consistent the transaction amounts are

The scores determine entry type: clusters with tight timing and tight amounts are classified as Standing; tight timing with variable amounts as Variable; everything else as Irregular. Each cluster that clears a minimum confidence threshold produces a candidate entry in the review queue.

## 4.5 User Review

After processing, users see a review queue of candidate entries. Each candidate shows the detected pattern, entry type, calculated /day rate, confidence breakdown, and the matched transactions. Users can approve as-is, edit the name or entry type, or dismiss.

Once approved, the entry joins the model and future matching against it is automatic. Review burden decreases over time — most people have fewer than 50 true recurring items, and after two or three import cycles the queue shrinks to only genuinely new activity.

## 4.6 The Healing Property

The import cycle is self-correcting. Entries that end — a subscription cancelled, a loan paid off — naturally disappear from the Actual lane as transactions stop arriving. The engine detects a missed expected transaction and surfaces it in the review queue as an `ended` alert. The user confirms the change; the Projection lane entry is then closed. No manual cleanup is required.

# 5. Budget Views

The budget page surfaces three views of the same underlying snapshot data. Each view answers a distinct question about the user's financial position. All three read from `snapshots` (the engine's computed daily rate series) and `projections` (the forward-looking signal superposition). Switching between views does not reload data — they are lenses on the same state.

## 5.1 Pulse

Answers: Where am I right now?

The Pulse view is the primary budget dashboard. It reads the latest snapshot rates — the most recent `actual_rate_per_day` and `projected_rate_per_day` per entry — and presents them as a ranked list from Income at the top to Margin at the bottom. Every figure is shown as a /day rate with a display scale toggle (monthly, quarterly, yearly).

- Income entries at the top with rate and scale translation

- Expense entries below, each showing name, label, /day rate, and percentage of Income

- Entries grouped by label with group subtotals; classifications apply additional grouping

- Margin at the bottom with /day rate and discretionary percentage

- Drift indicator per entry when Actual diverges from Projection

## 5.2 Stack

Answers: Why is my Margin what it is?

The Stack view is a waterfall cascade showing how Income rate is consumed by each Expense entry in sequence, arriving at Margin. Each row removes a proportional slice from the remaining bar until only Margin remains. The visual makes the weight of each commitment immediately apparent without requiring the user to do any arithmetic.

- Income bar shown at full width as the starting point

- Each Expense entry removes its proportional slice from the right

- Label groupings collapse multiple entries into a single band with expand option

- Margin bar at the bottom — thin bars here are the gut-punch insight moment

## 5.3 Horizon

Answers: Where am I going?

The Horizon view displays the rate history as a candlestick chart and extends it forward as a projection line. Each candle represents the range of `actual_rate_per_day` values from `snapshots` within the candle's time bucket — open, high, low, close are computed by the API from the daily series at query time, not stored. The projection line extends from the last snapshot date forward using `projections` data.

- Candlestick history — OHLC derived from daily snapshot rates per time bucket

- Projection line — forward rate from `projections`, extending beyond the last import date

- Drift shading between actual and projected — green when ahead, red when behind

- Slope indicator per entry showing whether the rate is trending up or down

- Time axis: 3 months · 6 months · 1 year · 3 years

# 6. Product Vocabulary

Veloci uses a consistent naming system across all surfaces. Terms are plain nouns chosen to be immediately inferrable without explanation. The vocabulary avoids financial jargon and lay-person approximations equally, landing in a register that respects the user's intelligence.

## 6.1 Core Terms

| **Term** | **Definition** |
| --- | --- |
| **Income** | The rate at which money enters your budget from all Active sources combined |
| **Expense** | The rate at which money leaves your budget across all committed Active outflows |
| **Margin** | Your surplus rate — Income minus Expense. The /day capacity you have for new commitments or wealth generation |
| **Drift** | The delta between your Projection and your Actual rate. The primary diagnostic signal in Veloci |
| **/day** | The universal rate unit. Never named — always displayed as a suffix. All figures derive from this unit |

## 6.2 Entry Types

| **Term** | **Definition** |
| --- | --- |
| **Standing** | Regular cadence, consistent amount. Rate = amount ÷ period_days |
| **Variable** | Regular cadence, inconsistent amount. Rate = rolling window total ÷ period_days, using average or maximum |
| **Irregular** | No detectable cadence, inconsistent amount. Grouped by merchant; rate = most recent amount ÷ period_days |

## 6.3 Account Status

| **Term** | **Definition** |
| --- | --- |
| **Active** | This account participates in the budget. Its flows contribute to Income, Expense, and Margin calculations |
| **Passive** | This account is tracked and projected but does not affect Margin. Used for debt accounts, investment accounts, or savings viewed as separate from the operating budget |

## 6.4 Budget Lanes

| **Term** | **Definition** |
| --- | --- |
| **Projection** | The expected lane — what your budget anticipates based on known commitments and estimated income |
| **Actual** | The real lane — what transaction data shows actually happened |

## 6.5 Controls and Settings

| **Term** | **Definition** |
| --- | --- |
| **Smoothing** | The amortization window (`period_days`) on an entry. Engine-detected for Standing and Variable; user-configured for Irregular. The rate displayed is always the smoothed figure |

## 6.6 Views

| **Term** | **Definition** |
| --- | --- |
| **Pulse** | The rate snapshot dashboard. Where you are right now |
| **Stack** | The waterfall cascade. Why your Margin is what it is |
| **Horizon** | The projection graph. Where you are going |

# 7. Design Principles

## 7.1 The /day Rate is Always Primary

Every financial figure in Veloci leads with the /day rate. Monthly, quarterly, and yearly translations are available as secondary context but never lead. This is the central UX decision that makes the product work — users build intuition for /day rates through repetition, and once that intuition is calibrated, every new financial decision becomes immediately legible.

## 7.2 Show Impact at the Moment of Approval

When a user approves a new Entry from the review queue, that is the highest-value insight moment in the product. The Margin change should be shown immediately and in context — not as a notification, but as a live update to the Pulse view. The user should feel the app respond to their confirmation with genuine, personalized insight.

## 7.3 Never Editorialize

Veloci does not label entries as necessities or luxuries. It does not suggest what the user should cut. It does not rank spending by importance. The app shows numbers. Users draw conclusions. Labels, classifications, and groupings are entirely user-defined. The product's job is visibility, not advice.

## 7.4 Friction at Insight, Not at Setup

Configuration burden should decrease over time. The first import cycle requires the most effort — institution mapping, entry review, label assignment. Each subsequent cycle requires less. By cycle three, most users should be approving a handful of new items and reviewing drift, not rebuilding their financial picture.

## 7.5 Degrade Gracefully

The model should never break or produce wrong answers when data is sparse or ambiguous. Unmatched transactions are passed to pattern detection — if confidence is too low to create a candidate entry, they simply remain unmatched until more data accumulates. A single transaction is never enough to confidently establish a pattern; more imports improve the signal without requiring user intervention.

When an active entry stops receiving expected transactions, the engine raises an `ended` alert rather than silently closing the entry or letting it distort rates. The user confirms or dismisses the change. Uncertainty is always surfaced explicitly — the system asks rather than assumes.

# 8. Out of Scope — Version 1

- Bank sync / automatic transaction fetching

- Cloud hosting or managed sync service

- Investment portfolio tracking or performance calculation

- Multi-currency support

- Mobile native applications (web-first, mobile-responsive)

# 9. Database Schema

Veloci uses two Postgres databases: `veloci_auth` for authentication and token management, and `veloci_app` for all financial and user data. Schema files live in `migrations/` organized by database:

| File | Database | Contents |
| --- | --- | --- |
| `migrations/auth/001_auth_schema.sql` | veloci_auth | Credentials, tokens, invite tokens |
| `migrations/app/001_app_schema.sql` | veloci_app | Entities, users, RBAC |
| `migrations/app/002_financial_schema.sql` | veloci_app | Financial data model |
| `migrations/app/002_rbac_seed.sql` | veloci_app | RBAC seed data |

The number prefix within each folder is FK dependency order — files with the same prefix have no dependency on each other and can be applied in either order; lower numbers must be applied before higher numbers that reference their tables. All files are edited in place.

## 9.1 Auth Database

`migrations/auth/001_auth_schema.sql`

### auth_credentials

One row per registered user. The auth database holds no financial data — it exists solely to authenticate requests and issue tokens.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK — referenced as `user_id` in the auth token chain |
| email | TEXT | UNIQUE |
| password_hash | TEXT | bcrypt hash |
| system_role | TEXT | `server_admin` · `user` |

### tokens

JWT access and refresh tokens. Access tokens link back to the refresh token that issued them via `parent_id` — revoking a refresh token cascade-deletes all its access tokens. `rotated_at` tracks refresh token rotation, allowing a short grace window for concurrent requests.

| Column | Type | Notes |
| --- | --- | --- |
| jti | TEXT | UNIQUE — JWT ID claim, used for lookup and revocation |
| token_type | TEXT | `access` · `refresh` |
| parent_id | UUID | FK → tokens(id) — access tokens only; cascade delete |
| claims | JSONB | Full JWT claims payload |
| expires_at | TIMESTAMPTZ | |
| rotated_at | TIMESTAMPTZ | Set when this refresh token is rotated; nullable |

### invite_tokens

One-time invite links. `token_hash` is the hashed form of the link token. `accepted_at` is set on first use; subsequent use is rejected.

## 9.2 Core App Database

`migrations/app/001_app_schema.sql`

### entities

The top-level unit of the data model. A household, individual, or organization. All financial tables scope to `entity_id`.

### users

Individual user profiles in the app database. `auth_credential_id` is the FK bridge to the auth database — the two databases share no schema but are joined through this ID at the application layer.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| auth_credential_id | UUID | Bridge FK to `auth_credentials.id` in veloci_auth |
| email | TEXT | Denormalized from auth for display — kept in sync by veloci-auth service |
| name | TEXT | Display name |

### RBAC

Four tables implement role-based access control:

- **roles** — named roles (`entity_admin`, `entity_user`, etc.)
- **permissions** — named permission strings (`entries:write`, `classifications:write`, `accounts:write`, etc.)
- **role_permissions** — join table assigning permissions to roles
- **entity_users** — assigns a user to an entity with a specific `entity_role` (`entity_admin` · `entity_user`)

## 9.3 Financial Reference / Taxonomy

### labels

Global name registry. No `entity_id` — labels are shared across all entities. Renaming a label requires no recalculation.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| name | TEXT | UNIQUE — one canonical name per signal |
| created_at | TIMESTAMPTZ | |

## 9.4 Accounts and Institutions

### institution_mappings

CSV column configuration per bank. Drives Stage 0 normalization. Also stores the `settlement_window_days` used to bound the engine's flux window.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| entity_id | UUID | FK → entities |
| institution_name | TEXT | UNIQUE per entity |
| source_type | TEXT | `csv` · `integration` |
| settlement_window_days | INTEGER | Days before import timestamp considered settled. Default 14 |
| dedup_window_days | INTEGER | Date tolerance for matching transactions across overlapping imports |
| amount_tolerance_pct | FLOAT8 | Fuzzy amount match tolerance. Default 0.005 (0.5%) |
| date_col, amount_col, merchant_col | TEXT | CSV column mapping |
| amount_sign_convention | TEXT | `positive_is_credit` · `positive_is_debit` |
| created_at | TIMESTAMPTZ | |

### accounts

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| entity_id | UUID | FK → entities |
| institution_id | UUID | FK → institution_mappings (nullable) |
| name | TEXT | UNIQUE per entity |
| account_type | TEXT | `checking` · `savings` · `credit` · `loan` · `mortgage` · `investment` |
| status | TEXT | `active` · `passive` |
| interest_rate | NUMERIC(8,4) | APY for savings / APR for debt |
| balance_cents | BIGINT | Latest known balance snapshot |
| credit_limit_cents | BIGINT | Credit accounts only |

## 9.5 Import Pipeline

### processing_jobs

Audit log for every engine job. A partial unique index prevents duplicate active jobs: only one `queued` or `processing` job per `(entity_id, job_type)` at a time.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| entity_id | UUID | FK → entities |
| job_type | TEXT | `import.process` · `entries.reprocess` · `account.analyze` · `balance.project` |
| status | TEXT | `queued` · `processing` · `complete` · `failed` |
| triggered_by | UUID | FK → users |

### pending_imports

Staging area for uploaded CSVs. Retained after processing for audit.

### import_batches

One record per completed `import.process` run. Records deduplication counts: rows imported, skipped as duplicate, and superseded.

### transactions

Source of truth for all financial calculations. Financial columns are immutable after insert.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| entity_id | UUID | FK → entities |
| account_id | UUID | FK → accounts |
| import_batch_id | UUID | FK → import_batches |
| date | DATE | Transaction date |
| amount_cents | BIGINT | Positive = inflow; negative = outflow |
| imported_payee | TEXT | Raw bank string — immutable |
| merchant_normalized | TEXT | Stage 0 normalized name |
| imported_id | TEXT | Bank dedup ID from CSV — nullable |
| settlement_status | TEXT | `flux` · `settled` — set at insert, never changed |
| imported_at | TIMESTAMPTZ | Wall-clock insert time for effective settlement derivation |

## 9.6 Financial Model

### entries

One row per continuous rate signal instance. Absorbs the prior `rules` and `rule_epochs` tables into a single unified structure.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| entity_id | UUID | FK → entities |
| label_id | UUID | FK → labels — nullable |
| direction | TEXT | `income` · `expense` |
| entry_type | TEXT | `standing` · `variable` · `irregular` |
| period_days | INTEGER | Amortization window. Default 30 |
| variable_method | TEXT | `avg` · `max` — variable entries only |
| projected_rate_per_day | NUMERIC(12,4) | Engine or user-set expected /day rate |
| conditions | JSONB | Auto-match rules — nullable for manual entries |
| priority | INTEGER | Lower = matched first. Default 100 |
| status | TEXT | `pending_review` · `active` · `inactive` |
| source | TEXT | `user` · `engine` |
| recurrence_anchor | TEXT | Expected recurrence day/pattern |
| next_due_date | DATE | Engine-computed next expected transaction date |
| project_tentatively | BOOLEAN | If TRUE, Stage 7 projects this entry before approval |
| pending_amount_cents | BIGINT | Forward-versioned amount — applied at `pending_effective_date` |
| pending_effective_date | DATE | Date when pending amount becomes active |
| start_date | DATE | First transaction date for this instance |
| end_date | DATE | Closure date — NULL = currently active |

### classifications

User-defined post-stage rules that apply labels to groups of entries. Do not affect rate calculations — display and grouping only. Conditions reference label UUIDs, enabling aggregate labels built from leaf labels without a separate membership table. The API enforces cycle detection at write time.

| Column | Type | Notes |
| --- | --- | --- |
| id | UUID | PK |
| entity_id | UUID | FK → entities |
| label_id | UUID | FK → labels — label this classification assigns |
| conditions | JSONB | Matching conditions referencing entry attributes and label UUIDs |
| priority | INTEGER | Evaluation order. Default 100 |
| status | TEXT | `active` · `inactive` |

### transaction_entry_assignments

Many-to-many join between transactions and entries. A transaction may match multiple entries. `confidence` is 1.0 for user-created entries and 0.0–1.0 for engine-generated matches.

### review_queue

Engine-detected candidate entries awaiting user approval. Each row carries a full suggestion — name, entry_type, conditions, rate, matched transaction count, and three-component confidence breakdown (merchant / timing / amount).

`alert_type`: `new` = first detection, `drift` = rate changed significantly, `ended` = signal no longer seen.

## 9.7 Engine Output

### snapshots

Rebuildable engine output. One row per calendar day per node. Safe to truncate and recompute at any time.

`node_type = 'entry'` → Stage 3 entry-level rate signal
`node_type = 'classification'` → Stage 4 classification-level aggregate

`snapshot_date` is the calendar day this row represents. `computed_as_of` is `MAX(transactions.date)` from the import run that wrote this row — separate from `snapshot_date` so an import covering a historical window correctly records its horizon.

OHLC candlestick high/low are not stored — the API computes `MAX/MIN(actual_rate_per_day)` over the daily series at query time.

| Column | Type | Notes |
| --- | --- | --- |
| node_id | UUID | FK → entries.id or labels.id |
| node_type | TEXT | `entry` · `classification` |
| snapshot_date | DATE | Calendar day this row represents |
| computed_as_of | DATE | MAX transaction date from the import run |
| actual_rate_per_day | NUMERIC(12,4) | Observed rate for this day |
| projected_rate_per_day | NUMERIC(12,4) | Expected rate for this day |
| drift_per_day | NUMERIC(12,4) | Actual minus projected |
| slope_per_day | NUMERIC(14,6) | Linear regression slope over snapshot history |
| r_squared | NUMERIC(4,3) | Regression fit quality |
| rolling_window_total_cents | BIGINT | Sum of matched amounts in the rate window |

### projections

Forward-looking signal superposition timeline produced by Stage 7. One row per (account, projected_date) per job. Safe to truncate and recompute.

`account_id NULL` = entity-level aggregate across all active accounts. `is_pinch_point = TRUE` when `margin_rate_per_day < 0`.

| Column | Type | Notes |
| --- | --- | --- |
| entity_id | UUID | FK → entities |
| account_id | UUID | FK → accounts — nullable for entity aggregate |
| job_id | UUID | FK → processing_jobs |
| projected_date | DATE | Forward date |
| income_rate_per_day | NUMERIC(12,4) | |
| commitment_rate_per_day | NUMERIC(12,4) | |
| margin_rate_per_day | NUMERIC(12,4) | Income minus commitment |
| projected_balance_cents | BIGINT | Running integral of margin — for bank comparison only |
| is_pinch_point | BOOLEAN | TRUE when margin < 0 at this date |

# 10. Engine Pipeline

The processing engine is a Rust service (`services/engine`) using `sqlx` for async Postgres access and `rayon` for CPU-parallel computation within stages. All pipeline computation is entity-scoped — no row from one entity is ever visible to another entity's pipeline run.

## 10.1 Job Types

| **Job type** | **Stages** | **Trigger** |
| --- | --- | --- |
| `import.process` | 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7 | CSV upload |
| `entries.reprocess` | 1 → 2 → 3 → 4 → 5 → 6 → 7 | Entry edited or approved from review queue |
| `account.analyze` | 3 → 4 → 5 → 6 → 7 | Account balance updated |
| `balance.project` | 7 | Balance-only refresh |

A partial unique index on `processing_jobs` prevents duplicate active jobs: only one `queued` or `processing` job per `(entity_id, job_type)` is allowed at a time.

## 10.2 The Flux Window and Day-Crawl

Stages 3–6 do not run once — they run once per calendar day within the **flux window**:

```text
flux_start = computed_as_of − settlement_window_days
flux_end   = computed_as_of
```

`computed_as_of` is `MAX(transactions.date)` from the current import. `settlement_window_days` is the maximum value across all institution mappings for the entity (default 7 days if none configured).

The day-crawl crawls `[flux_start .. computed_as_of]` inclusive, computing and upserting one snapshot per day. Days outside the flux window contain only settled transactions and are not recomputed. Stage 7 runs once after the crawl completes.

## 10.3 Stage Reference

### Stage 0 — CSV Normalization and Deduplication

**Input:** Pending import CSV bytes, institution mapping config
**Output:** Rows in `transactions`; `Stage0Output.computed_as_of`

Reads the institution mapping to resolve column positions and sign convention. Parses CSV rows, normalizes merchant names, and computes settlement status for each row (`settled` if `date < uploaded_at − settlement_window_days`, `flux` otherwise). Deduplicates against existing transactions using the `dedup_window_days` window and `amount_tolerance_pct` fuzzy matching. Flux rows from prior overlapping imports are superseded (deleted and replaced). Settled rows are never deleted. Emits `computed_as_of = MAX(date)` from all inserted rows.

---

### Stage 1 — Entry Matching

**Input:** Entity ID, `transactions`
**Output:** Rows in `transaction_entry_assignments`; `Stage1Output.unmatched_tx_ids`

Loads all active entries (`status = 'active'` AND `end_date IS NULL`) that have conditions. Evaluates each entry's JSONB conditions against every transaction using merchant normalization, amount bands, and timing rules. Entries are evaluated in ascending `priority` order — lower priority value matches first. Writes matched pairs to `transaction_entry_assignments`. Updates `next_due_date` on entries where new matches advance the expected recurrence date. Returns the IDs of transactions that matched no entry — these become Stage 2's input.

---

### Stage 2 — Pattern Detection

**Input:** Unmatched transaction IDs from Stage 1
**Output:** New rows in `entries` (status `pending_review`) and `review_queue`

Clusters unmatched transactions by merchant name similarity, amount consistency, and timing regularity. Scores each cluster across three components: merchant confidence (brand extraction and normalization quality), timing confidence (regularity of intervals), and amount confidence (variance in transaction amounts). The composite confidence score determines whether a cluster is surfaced as `new` or silently discarded below a threshold.

For each accepted cluster: upserts a label (by name, global UNIQUE), creates an entry with `source = 'engine'` and `start_date` set to the earliest transaction in the cluster, and enqueues a `review_queue` row with the full suggestion and confidence breakdown. Sets `next_due_date` and `project_tentatively = TRUE` when the recurrence pattern is clear enough to project before user approval.

Entry type assignment: `standing` for clusters with regular timing and consistent amounts (≥3 observations); `variable` for clusters with regular timing and variable amounts; `irregular` as the fallthrough when timing confidence is below the gates. For irregular clusters, `period_days` is set to the mean observed interval when 2+ transactions are present, or 30 for a single-transaction cluster.

---

### Stage 3 — Rate Computation (Day-Crawl)

**Input:** Entity ID, snapshot date, `entries`, `transaction_entry_assignments`, `transactions`
**Output:** `Stage3Output.entry_rates` — one `EntryRate` per active entry

Pure calculation — no writes. Runs once per day in the flux window.

Loads all active entries (`status = 'active'` AND `end_date IS NULL`). For each entry, collects all assigned transactions where `t.date >= e.start_date`. Computes `actual_rate_per_day` using the entry's `period_days` window:

- **Standing**: `amount / period_days` for the most recent matching transaction
- **Variable**: rolling window total over `period_days` divided by `period_days`, using `avg` or `max` per entry's `variable_method`
- **Irregular**: amortizes the most recent transaction amount over `period_days`

Loads the prior snapshot for each entry and uses it to set `projected_rate_per_day`. Emits one `EntryRate` per entry containing actual rate, projected rate, transaction count, window days used, and rolling window total.

---

### Stage 4 — Label Rate Aggregation

**Input:** `entry_rates` from Stage 3
**Output:** `Stage4Output.label_rates` — one `LabelRate` per label

Groups entry rates by `label_id`. For each label, sums actual and projected rates across all entries referencing that label. Records `contributing_entry_count`. Entries without a `label_id` are skipped. The result drives classification-level snapshot rows in Stage 6.

---

### Stage 5 — Trend Regression

**Input:** Stage 3 and Stage 4 outputs; snapshot history from `snapshots`
**Output:** `Stage5Output.entry_trends` and `Stage5Output.classification_trends`

Bulk-loads recent snapshot history for all nodes (entries and classifications). Runs a linear regression on each node's `actual_rate_per_day` time series to compute:

- `slope_per_day` — the rate of change of the actual rate over the history window
- `r_squared` — regression fit quality (how regular the trend is)
- `drift_per_day` — `actual_rate_per_day − projected_rate_per_day` for the current snapshot

Regression runs in parallel across nodes using `rayon`. Nodes with insufficient history receive zero slope and zero r-squared.

---

### Stage 6 — Snapshot Upsert

**Input:** Stages 3, 4, 5 outputs; entity ID, job ID, snapshot date
**Output:** Rows upserted into `snapshots`

Builds all snapshot rows in parallel using `rayon`, then writes them in a single atomic Postgres transaction. The UPSERT pattern (`ON CONFLICT (entity_id, node_id, snapshot_date) DO UPDATE`) makes Stage 6 fully idempotent — re-running the same job produces identical rows. Partial writes are impossible: either all snapshots for this day commit or none do.

Produces two snapshot types per day:

- **Entry snapshots** (`node_type = 'entry'`): one per `EntryRate` from Stage 3
- **Classification snapshots** (`node_type = 'classification'`): one per `LabelRate` from Stage 4

---

### Stage 7 — Cash Flow Projection

**Input:** Entity ID, `computed_as_of`, active entries + their recurrence schedules, latest snapshots
**Output:** Rows in `projections`; new `review_queue` alert rows for missed expected transactions

Runs once after the day-crawl completes. Loads eligible entries: `status = 'active'` entries and `status = 'pending_review'` entries where `project_tentatively = TRUE`. Loads the latest snapshot rate for each eligible entry to use as the projected rate. Deletes all existing projections for this entity/job and rebuilds them for 90 days forward.

For each projected day, superposes all eligible entry rates using their `recurrence_anchor` and `next_due_date` to phase each signal correctly across the timeline. Produces one row per `(entity_id, account_id, projected_date)` containing income rate, commitment rate, margin rate, running projected balance, and whether the day is a pinch point (margin < 0).

Also raises `alert_type = 'ended'` review queue entries for active entries whose `next_due_date` has passed without a matching transaction — signaling that a known recurring commitment may have ended.

## 10.4 Pipeline Invariants

- **Idempotent writes**: All stage writes use UPSERT or DELETE+INSERT. Re-running any job produces identical output.
- **Read pool / write pool**: Stages 0–5 use the read pool for queries. Stages 6 and 7 use the write pool for their final commits.
- **Snapshot safety**: The `snapshots` and `projections` tables are rebuildable from `transactions` + `entries` at any time. Truncating them and rerunning a full pipeline produces bit-identical results.
- **No cross-entity reads**: Every query is scoped with `entity_id = $1`. No stage can read or affect another entity's data.
