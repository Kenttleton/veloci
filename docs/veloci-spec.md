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

## 2.1 The Daily Rate as Atomic Unit

Storing everything as a /day rate gives the model three properties that make it powerful:

- Universal comparability — a weekly expense and an annual expense become directly comparable

- Simple scaling — monthly = rate × 30.44, quarterly = rate × 91.31, yearly = rate × 365

- Honest arithmetic — your true position is income rate minus all committed expense rates, continuously

## 2.2 Two Lanes: Projection and Actual

Every financial event exists in two lanes simultaneously. The gap between them is Drift — the core diagnostic metric of Veloci.

| **Lane** | **Description** |
| --- | --- > *Projection** | The expected rate derived from known commitments and estimated income. This is your budget, built automatically from detected patterns rather than manual entry. |
| **Actual** | The real rate derived from imported transaction data. This is what happened. |
| **Drift** | The delta between Projection and Actual, expressed as a /day rate. Positive drift means you are ahead of projection. Negative drift means commitments are outpacing expectations. |

## 2.3 Expense Classification

Every expense entry is classified into one of three types. Classification drives how the system calculates rates and applies Smoothing.

| **Type** | **Description** |
| --- | --- |
| **Standing** | Recurring commitment with consistent amount and frequency. Rent, subscriptions, loan minimums. Rate is exact. |
| **Variable** | Fluctuating but regular expense. Groceries, utilities, fuel. User chooses average or maximum for rate calculation on a per-entry basis. |
| **One-time** | A single non-recurring event — car repair, tax refund, annual insurance, work bonus. Direction (income or expense) carries the meaning; the type signals there is no expected cadence. Also referred to as Single; the two terms are synonymous. Rate is Smoothed over a configurable window. |

## 2.4 Smoothing

Smoothing is Veloci's amortization system. Rather than letting large infrequent payments create spikes in your rate, Smoothing distributes their cost over a window of time. This means your /day rate always reflects the true ongoing cost of your financial life, including obligations that haven't billed yet.

Smoothing defaults are applied by classification and can be overridden per entry:

| **Classification** | **Default Window** |
| --- | --- |
| **One-time (short-term)** | 30 days — unexpected events and one-time purchases |
| **One-time (planned annual)** | 365 days — known annual events like insurance or registration |
| **Variable** | Rolling average or user-selected maximum, recalculated each cycle |
| **User override** | Any entry can have its Smoothing window set manually |

|  | *Smoothing is a setting, not a label. Entries are not marked as Smoothed in the interface — the Smoothing control lives in the entry editor. The rate the user sees is always the Smoothed rate unless they inspect the entry.

## 2.5 Household and Multi-Account Model

Veloci treats the budget as unified above the account level. An account is metadata on a transaction, not a structural boundary. This means a couple sharing one joint account and each maintaining a personal account operates identically to a single user managing multiple accounts for different purposes.

- All Active accounts contribute flows to the unified budget

- Transfers between accounts cancel at the budget level — one account sees a debit, the other a credit, net impact is zero

- Transfer cancellation is detected by matching amounts and approximate timing, Smoothed over 30 days

- Passive accounts are tracked and projected but do not affect Margin

## 2.6 Debt Accounts

Debt accounts — credit cards, auto loans, mortgages, personal loans — are treated as Passive by default. They are fully tracked and projected but isolated from the Active budget picture. Users opt into comparison deliberately.

Debt accounts expose three calculations not available on standard accounts:

- Minimum payment rate — the committed /day outflow from the Active budget

- True cost rate — principal plus interest over remaining term, expressed as /day

- Payoff projection — what-if modeling showing how increasing payment rate by X/day changes payoff date and total interest paid

> Instead of asking 'should I pay an extra $100/month on this debt,' Veloci asks 'what does $3.29/day do to this debt?' The per diem framing makes the tradeoff concrete and comparable to other uses of that same margin.

# 3. Data Model

The core data model is intentionally minimal. All insight, reporting, and projection is derived from these structures through calculation rather than stored state.

## 3.1 Account

| **Field** | **Description** |
| --- | --- > *id** | Unique identifier |
| **name** | User-defined display name |
| **type** | checking · savings · credit · loan · mortgage · investment |
| **status** | Active · Passive — whether this account participates in Margin calculation |
| **interest_rate** | APY for savings projection or APR for debt cost calculation |
| **balance** | Current balance snapshot from latest import |
| **credit_limit** | Applicable for credit accounts |

## 3.2 Entry

An Entry is the atomic unit of the financial model. Every income source and expense is an Entry.

| **Field** | **Description*
| **id** | Unique identifier > *name** | User-defined display name (e.g. Netflix, Rent, Salary) |
| **account_id** | Originating account — metadata only, does not affect calculation |
| **direction** | Income · Expense |
| **entry_type** | Standing · Single · Hit · Boost · Variable |
| **projected_rate** | Expected /day rate — derived from pattern detection or user input |
| **actual_rate** | Real /day rate — derived from transaction history |
| **smoothing_window** | Days over which to amortize. System default or user override |
| **variable_method** | Average · Maximum — applicable to Variable entries only |
| **confidence** | Automation confidence score for user review prioritization |
| **status** | Active · Inactive · Projected — Projected entries exist in the Projection lane only |
| **tags** | User-defined grouping tags |

## 3.3 Transaction

| **Field** | **Description*
| **id** | Unique identifier > *account_id** | Source account |
| **entry_id** | Matched Entry — null if unmatched |
| **date** | Transaction date |
| **amount** | Raw transaction amount |
| **merchant_raw** | Raw merchant string from import |
| **merchant_clean** | Normalized merchant name after processing |
| **import_batch** | Which CSV import this came from |

## 3.4 Derived Values

These values are never stored — they are calculated on demand from Entries and Transactions:

| **Value** | **Derivation*
| **Margin** | Total Income rate minus total Expense rate — your /day surplus > *Drift** | Projected rate minus Actual rate for any Entry, group, or total |
| **Committed rate** | Sum of all Active Expense projected rates |
| **Discretionary rate** | Margin expressed as a percentage of total Income |
| **Payoff projection** | Calculated from debt balance, APR, and payment rate |

# 4. Import and Detection Cycle

Veloci is local-first and import-driven. There is no bank sync. Users export CSV transaction data from their financial institutions and import it manually. This is a deliberate design choice: CSV is more reliable than bank sync APIs, import events create a natural reconciliation rhythm, and the app never requires credentials to third-party financial services.

## 4.1 Institution Normalization

Every bank exports CSV in a slightly different format. On first import from a new institution, Veloci presents a column mapping interface: which column is date, which is amount, which is merchant. This mapping is saved and applied automatically on all future imports from the same institution.

## 4.2 Detection Pipeline

On each import, transactions are processed through a detection pipeline:

- Cluster by merchant name normalization, amount consistency, and timing regularity

- Score each cluster for confidence — high confidence entries are auto-approved, low confidence entries are surfaced for review

- Match transactions to existing Entries where patterns are already known

- Flag new patterns as candidate Entries for user review

- Detect transfers by matching debit/credit pairs within a timing window across accounts

## 4.3 User Review

After processing, users see a review queue of candidate Entries requiring approval. Each candidate shows the detected pattern, the calculated /day rate, the suggested classification, and the matched transactions. Users can approve as-is, edit name or classification, merge with an existing Entry, split into multiple Entries, or dismiss.

Once approved, the Entry joins the model and future matching is automatic. Review burden decreases over time as the model learns the user's financial patterns.

|  | *Most people have fewer than 50 true recurring items. After two or three import cycles, the review queue shrinks to only genuinely new activity.

## 4.4 The Healing Property

The import cycle is self-correcting. Entries that end — a loan paid off, a subscription cancelled — naturally disappear from the Actual lane as transactions stop arriving. The user sees their Margin increase without any manual action. Projection lane entries persist until dismissed, creating a visible gap that prompts the user to confirm the change.

# 5. Views

Veloci surfaces its financial model through three named views, each answering a distinct question about the user's financial life. Views are not tabs in a traditional sense — they are lenses on the same underlying data.

## 5.1 Pulse

Answers: Where am I right now?

The Pulse view is the primary dashboard. It displays the current rate snapshot — Income at the top, all Expense entries cascading below, Margin at the bottom. Every figure is shown as a /day rate with the current display scale (monthly, quarterly, yearly) available as a toggle.

- Income rate displayed prominently with scale translation

- Each Expense entry shown with name, tag, /day rate, and percentage of Income

- Entries grouped by user-defined tags with group subtotals

- Margin displayed at bottom with /day rate and discretionary percentage

- Drift indicator per entry and in total when Actual diverges from Projection

- New entry impact preview — adding a hypothetical expense shows Margin change in real time

## 5.2 Stack

Answers: Why is my Margin what it is?

The Stack view is a waterfall cascade showing how Income rate is consumed by each Expense entry in sequence, arriving at Margin. Each row removes a slice from the remaining bar, left to right, until only the Margin remains. The visual makes the proportional weight of each commitment immediately apparent without requiring the user to do any arithmetic.

- Income bar shown at full width as the starting point

- Each Expense entry removes its proportional slice from the right side of the remaining bar

- Tag groupings collapse multiple entries into a single band with expand option

- Margin bar at the bottom — thin bars here are the gut-punch insight moment

- Tapping any band highlights that entry across all views

## 5.3 Horizon

Answers: Where am I going?

The Horizon view is a line graph showing Projection and Actual rates over time. The gap between the two lines is Drift, shaded to make it visually prominent. The time axis is adjustable. Passive accounts can be toggled onto the Horizon view as additional overlaid lines without affecting the core budget picture.

- Projection line — expected rate over time based on known commitments

- Actual line — real rate from transaction history

- Drift shading between the lines — green when ahead, red when behind

- Passive account overlays — debt payoff curves, savings growth projections, on/off toggle per account

- Future events visible as projection changes — loan payoff date shows as Margin step-up, annual expense shows as planned Smoothing contribution

- Time axis: 3 months · 6 months · 1 year · 3 years

# 6. Product Vocabulary

Veloci uses a consistent naming system across all surfaces. Terms are plain nouns chosen to be immediately inferrable without explanation. The vocabulary avoids financial jargon and lay-person approximations equally, landing in a register that respects the user's intelligence.

## 6.1 Core Terms

| **Term** | **Definition** |
| --- | --- > *Income** | The rate at which money enters your budget from all Active sources combined |
| **Expense** | The rate at which money leaves your budget across all committed Active outflows |
| **Margin** | Your surplus rate — Income minus Expense. The /day capacity you have for new commitments or wealth generation |
| **Drift** | The delta between your Projection and your Actual rate. The primary diagnostic signal in Veloci |
| **/day** | The universal rate unit. Never named — always displayed as a suffix. All figures derive from this unit |

## 6.2 Entry Types

| **Term** | **Definition*
| **Standing** | A recurring commitment with consistent amount and frequency > *Single** | A one-time expected expense or income event, Smoothed over an appropriate window |
| **Hit** | An unexpected negative event — unplanned expense or financial setback. Smoothed short |
| **Boost** | An unexpected positive event — windfall income, refund, or gift. Smoothed short |
| **Variable** | A regular expense with a fluctuating amount. Rate calculated by average or maximum per user preference |

## 6.3 Account Status

| **Term** | **Definition*
| **Active** | This account participates in the budget. Its flows contribute to Income, Expense, and Margin calculations > *Passive** | This account is tracked and projected but does not affect Margin. Used for debt accounts, investment accounts, or savings viewed as separate from the operating budget |

## 6.4 Budget Lanes

| **Term** | **Definition*
| **Projection** | The expected lane — what your budget anticipates based on known commitments and estimated income > *Actual** | The real lane — what transaction data shows actually happened |

## 6.5 Controls and Settings

| **Term** | **Definition*
| **Smoothing** | The amortization control on an entry. Sets the window over which a Single, Hit, Boost, or Variable entry's cost is distributed. System defaults apply automatically; users override per entry |

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

Veloci does not label entries as necessities or luxuries. It does not suggest what the user should cut. It does not rank spending by importance. The app shows numbers. Users draw conclusions. Tags, colors, and groupings are entirely user-defined. The product's job is visibility, not advice.

## 7.4 Friction at Insight, Not at Setup

Configuration burden should decrease over time. The first import cycle requires the most effort — institution mapping, entry review, tag assignment. Each subsequent cycle requires less. By cycle three, most users should be approving a handful of new items and reviewing drift, not rebuilding their financial picture.

## 7.5 Degrade Gracefully

The model should never break or require manual correction when data is missing or ambiguous. Unmatched transactions sit in a review queue. Entries with no recent transactions drift toward Projected-only status rather than disappearing. The healing property ensures that errors and gaps self-correct over time without user intervention.

# 8. Out of Scope — Version 1

- Bank sync / automatic transaction fetching

- Cloud hosting or managed sync service

- Investment portfolio tracking or performance calculation

- Tax preparation or reporting

- Bill payment or financial transaction initiation

- Multi-currency support

- Mobile native applications (web-first, mobile-responsive)

- AI-generated financial advice or recommendations