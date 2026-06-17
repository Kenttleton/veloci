# Veloci

*Personal Financial Velocity*

Veloci is a local-first personal finance app built around a single idea: every financial commitment and income source expressed as a daily rate. Not a monthly budget snapshot — a continuous rate, like a speedometer for money.

> A $20/month Netflix subscription costs $0.66/day. You work 1.3 hours a month to pay for it. Neither fact is hidden — they just aren't shown. Veloci shows them.

---

## How it works

Every income source and expense lives as a **/day rate**. Monthly, quarterly, and yearly figures are just that rate scaled — the model never changes units. Your true daily position is income rate minus all committed expense rates, continuously.

Two lanes run in parallel:

- **Projection** — the expected rate from known commitments and estimated income
- **Actual** — the real rate from imported transaction data
- **Drift** — the delta between them, the primary diagnostic signal

---

## Import cycle

Veloci is local-first and import-driven. No bank sync, no credentials to third-party services. You export CSV from your bank, import it, and Veloci processes it:

1. Normalizes merchant names and clusters transactions by pattern
2. Matches transactions to known entries automatically
3. Surfaces new patterns as candidates for your review
4. After approval, future matching is automatic

Most people have fewer than 50 true recurring items. After two or three import cycles, the review queue shrinks to only genuinely new activity.

---

## Views

**Pulse** — where you are right now  
Rate snapshot dashboard. Income at the top, all expenses cascading below, Margin at the bottom. Every figure leads with the /day rate.

**Stack** — why your Margin is what it is  
Waterfall cascade showing how income is consumed by each expense in sequence. Thin bars at the bottom are the gut-punch insight moment.

**Horizon** — where you are going  
Line graph of Projection vs. Actual over time, with Drift shaded between them. Supports passive account overlays (debt payoff curves, savings projections) without affecting the core budget picture.

---

## Expense types

| Type | Description |
|------|-------------|
| **Standing** | Recurring commitment — rent, subscriptions, loan minimums. Rate is exact. |
| **Single** | One-time expected expense. Smoothed over a window. |
| **Hit** | Unexpected negative event. Smoothed short (30 days default). |
| **Boost** | Unexpected positive event — refund, gift, bonus. Smoothed short. |
| **Variable** | Regular expense with fluctuating amount — groceries, utilities. Rate by average or maximum. |

**Smoothing** amortizes large infrequent payments over a time window so your /day rate always reflects your true ongoing cost, including obligations that haven't billed yet.

---

## Debt accounts

Debt accounts are Passive by default — tracked and projected, but isolated from your active budget picture. Each debt account exposes:

- **Minimum payment rate** — the committed /day outflow from your active budget
- **True cost rate** — principal plus interest over remaining term, as /day
- **Payoff projection** — what adding $X/day does to payoff date and total interest paid

> Instead of "should I pay an extra $100/month on this debt," Veloci asks "what does $3.29/day do to this debt?"

---

## Design principles

- **Matter of fact, not prescriptive.** The app shows numbers. Users draw conclusions.
- **The /day rate is always primary.** Monthly translations are secondary context, never the lead figure.
- **Local-first.** Your financial data stays on your hardware.
- **Self-healing.** Cancelled subscriptions disappear from Actual as transactions stop arriving. Errors and gaps correct themselves over time without manual intervention.
- **Friction decreases over time.** The first import cycle is the hardest. Each subsequent cycle requires less effort.

---

## Out of scope — v1

- Bank sync / automatic transaction fetching
- Cloud hosting or managed sync
- Investment portfolio tracking
- Tax preparation or reporting
- Bill payment or transaction initiation
- Multi-currency support
- Mobile native apps (web-first, mobile-responsive)
- AI-generated financial advice
