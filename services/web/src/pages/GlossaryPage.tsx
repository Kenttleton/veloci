
interface GlossaryTerm {
  term: string
  shortDef: string
  fullDef?: string
  examples?: string
}

const GLOSSARY_TERMS: GlossaryTerm[] = [
  // From 2026-07-02-ui-design.md
  {
    term: 'Margin',
    shortDef: 'Income minus all commitments at the selected rate.',
    fullDef: 'Margin is the money remaining after all your known commitments are subtracted from income. Positive margin means your income exceeds your commitments. Negative margin means you are spending more than you are earning at the current rate.',
    examples: 'If your income is $8,625/mo and your commitments total $6,190/mo, your margin is $2,435/mo (28%).',
  },
  {
    term: 'Projection',
    shortDef: 'Expected margin, calculated from known commitments amortized over time.',
    fullDef: 'The projection is a smooth forward-looking estimate of your margin, calculated by amortizing all known commitments over time. It absorbs known future events (such as a loan payoff) gradually rather than showing discrete jumps. The projection curve extends past today into the future based on your current budget.',
  },
  {
    term: 'Pulse',
    shortDef: 'The selected period on the Horizon graph. Stack and Summary reflect this period.',
    fullDef: 'The Pulse is the active time period you are examining. Click any candle on the Horizon graph to set the Pulse. The Summary strip and Stack panel update to show rates for the Pulse period. By default the Pulse is the most recently closed period.',
  },
  {
    term: 'Horizon',
    shortDef: 'The candlestick graph showing margin over time, past and future.',
    fullDef: 'The Horizon is the candlestick chart that shows your margin rate across time. Each candle represents one period (day, month, or year depending on your rate format). Green candles indicate periods where actual margin exceeded projection; red candles indicate the reverse. Drag horizontally to pan through history or into the projected future.',
  },
  {
    term: 'Drift',
    shortDef: 'Difference between actual and projected rate. +$X = ahead, −$X = behind.',
    fullDef: 'Drift measures how much your actual spending deviated from the projection in a given period. Positive drift means you came in ahead of expectation (margin improved). Negative drift means you spent more than projected (margin was hurt). Each candle body height represents the drift magnitude for that period.',
  },
  {
    term: 'Hit',
    shortDef: 'An unexpected cost that pushed a commitment above its projected rate.',
    fullDef: 'A Hit occurs when a commitment\'s actual spending exceeded the projected rate for the period. Hits are tagged with a red pill in the Stack panel and correspond to red candles on the Horizon. The API determines Hit classification — the UI renders the tag without applying threshold logic.',
    examples: 'A higher-than-usual electric bill in summer, or a one-time fee from a normally consistent subscription.',
  },
  {
    term: 'Boost',
    shortDef: 'An unexpected positive — a commitment cost less, or income exceeded expectation.',
    fullDef: 'A Boost occurs when a commitment\'s actual spending came in below projection, or when income exceeded expectations. Boosts are tagged with a green pill in the Stack panel and correspond to green candles on the Horizon.',
    examples: 'A lower-than-expected utility bill, a refund, or a month where a variable expense was unusually low.',
  },
  {
    term: 'Stack',
    shortDef: 'Waterfall breakdown of the Pulse period showing each commitment\'s rate and drift.',
    fullDef: 'The Stack is the expandable panel below the Horizon graph. It shows each commitment\'s actual rate and drift for the selected Pulse period, organized by label group. The top row is the income anchor; the bottom row shows the remaining margin after all commitments.',
  },
  {
    term: 'Amortization',
    shortDef: 'How Veloci spreads known future events (e.g. annual insurance) evenly over time as a stable rate.',
    fullDef: 'Amortization is the process of distributing a known future cost evenly over time to produce a stable daily rate. For example, a $1,200/year insurance payment is amortized to $3.29/day rather than shown as a spike on payment day. This allows the projection to be smooth and comparable regardless of payment timing.',
    examples: 'Annual subscriptions, quarterly taxes, semiannual insurance payments.',
  },
  // From 2026-07-12-ui-design-account-review-labels.md
  {
    term: 'Rule',
    shortDef: 'A named pattern that matches transactions and converts them to a $/day rate.',
    fullDef: 'A Rule is the core configuration unit in Veloci. Each rule has conditions that match transactions (by merchant, amount, timing), converts the matched transactions into a $/day rate, and outputs to exactly one label. Rules are created during the Review process and can be edited in the rule editor.',
  },
  {
    term: 'Label',
    shortDef: 'A named group that one or more rules output to. Labels are the categories you see in the Stack.',
    fullDef: 'Labels are named groupings that rules output to. Each rule outputs exactly one label. Labels are the category headers in the Stack panel. You can create new labels during Review or in Settings > Labels. Labels cannot be deleted — they are permanent identifiers. The name can be changed at any time.',
  },
  {
    term: 'Confidence',
    shortDef: 'How certain the engine is that a detected pattern is real. Score from 0 to 1.',
    fullDef: 'Confidence is a composite score from 0 to 1 that indicates how reliably the engine detected a pattern. It is composed of three sub-scores: merchant (consistency of the business name), timing (regularity of the cadence), and amount (consistency of the charge amount). Scores above 0.7 indicate high confidence.',
  },
  {
    term: 'Epoch',
    shortDef: 'The active lifespan of a rule\'s signal. A new epoch begins when a rule is approved; it ends when the commitment stops.',
    fullDef: 'An epoch is a bounded period during which a rule was actively generating transactions. When you approve a new rule, the epoch begins. When you confirm a rule has ended, the epoch closes. A rule can have multiple epochs if it was temporarily paused and resumed. History is preserved per-epoch.',
  },
  {
    term: 'Pending',
    shortDef: 'A rule detected by the engine but not yet reviewed. Pending rules are excluded from your budget until approved.',
    fullDef: 'A Pending rule is one the engine has proposed but you have not yet reviewed. Pending rules are displayed in the Transactions tab with a muted treatment and an accent border, indicating the grouping is provisional. Pending rules do not contribute to your budget rates until you approve them in Review.',
  },
  {
    term: 'Drift (rule)',
    shortDef: 'A detected change in an active rule\'s amount or timing pattern. Requires review to accept or dismiss.',
    fullDef: 'Rule drift occurs when the engine detects that an active rule\'s pattern has changed — for example, a subscription price increase. A drift card appears in Review showing the old and new patterns. You choose whether to record the change as a Correction (in-place update) or a Version (new epoch with the updated amount).',
  },
  {
    term: 'Ended',
    shortDef: 'An active rule whose expected transaction has not arrived. May indicate a cancelled subscription or temporary gap.',
    fullDef: 'An Ended alert appears when an active rule\'s expected transaction is overdue by multiple cycles (the 3-strike mechanism). You decide whether it is a temporary gap (keep the rule active) or a true end (close the epoch on a specific date). Ended rules retain their historical data in the Horizon chart.',
  },
  // From 2026-07-13-ui-design-job-status.md
  {
    term: 'Recalculating',
    shortDef: 'A background job is updating rates, snapshots, or projections. Current values are valid; fresh values will appear when the job completes.',
    fullDef: 'When a recalculation is in progress, affected surfaces show a muted treatment (opacity 0.55) with an accent left border. This indicates that the current value is about to be replaced with fresher data. The current value is still valid — it reflects the last completed job. No data is lost during recalculation.',
  },
  {
    term: 'Activity',
    shortDef: 'The log of all processing jobs — imports, reprocesses, and recalculations — for your account.',
    fullDef: 'The Activity panel shows the full history of all processing jobs for your entity. Jobs are listed in reverse-chronological order with no time cutoff. You can expand any job to see its stage-by-stage timeline. Failed jobs show the error detail and a Retry option if applicable.',
  },
  {
    term: 'Job',
    shortDef: 'A unit of background work triggered by an import or a rule change. Jobs run through multiple stages before completing.',
    fullDef: 'A Job is a unit of asynchronous processing work. Three job types exist: Import (processes a CSV file through all stages), Rules reprocess (reprocesses all transactions against the current rule set), and Recalculate (recalculates snapshots and projections for an account). Jobs run through up to 8 stages and update the UI via SSE events as each stage completes.',
  },
]

export function GlossaryPage() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div
        style={{
          padding: '14px 20px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em' }}>
          Glossary
        </h1>
        <p style={{ margin: '4px 0 0', fontSize: 13, color: 'var(--text3)' }}>
          Domain vocabulary for Veloci. Hover any dashed-underline term in the app for a short definition.
        </p>
      </div>

      {/* Term table */}
      <div style={{ flex: 1, overflow: 'auto', padding: '20px' }}>
        <div style={{ maxWidth: 720 }}>
          {GLOSSARY_TERMS.map((entry, idx) => (
            <div
              key={entry.term}
              style={{
                padding: '16px 0',
                borderBottom: idx < GLOSSARY_TERMS.length - 1 ? '1px solid var(--border)' : 'none',
              }}
            >
              <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
                <div style={{ width: 160, flexShrink: 0 }}>
                  <span
                    style={{
                      fontSize: 14,
                      fontWeight: 700,
                      color: 'var(--text)',
                    }}
                  >
                    {entry.term}
                  </span>
                </div>
                <div style={{ flex: 1 }}>
                  <p style={{ margin: '0 0 6px', fontSize: 13, color: 'var(--text2)' }}>
                    {entry.fullDef ?? entry.shortDef}
                  </p>
                  {entry.examples && (
                    <p style={{ margin: 0, fontSize: 12, color: 'var(--text3)', fontStyle: 'italic' }}>
                      Example: {entry.examples}
                    </p>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
