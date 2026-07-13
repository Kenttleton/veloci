import { useState } from 'react'
import { TermTooltip } from '../shared/TermTooltip'

interface ConfidenceComponentProps {
  confidence: number
  merchantConfidence: number
  timingConfidence: number
  amountConfidence: number
  defaultExpanded?: boolean
}

function scoreColor(score: number): string {
  if (score >= 0.7) return 'var(--margin-pos)'
  if (score >= 0.3) return 'var(--text2)'
  return 'var(--text3)'
}

function ScoreBar({ score }: { score: number }) {
  return (
    <div
      style={{
        flex: 1,
        height: 4,
        borderRadius: 2,
        background: 'var(--surface2)',
        overflow: 'hidden',
        margin: '0 8px',
      }}
    >
      <div
        style={{
          width: `${score * 100}%`,
          height: '100%',
          background: 'var(--accent)',
          borderRadius: 2,
        }}
      />
    </div>
  )
}

export function ConfidenceComponent({
  confidence,
  merchantConfidence,
  timingConfidence,
  amountConfidence,
  defaultExpanded = true,
}: ConfidenceComponentProps) {
  const [showDetail, setShowDetail] = useState(defaultExpanded)

  return (
    <div
      style={{
        padding: '10px 0',
        borderTop: '1px solid var(--border)',
        borderBottom: '1px solid var(--border)',
        marginBottom: 12,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: showDetail ? 10 : 0 }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)' }}>
            <TermTooltip term="Confidence" definition="How certain the engine is that a detected pattern is real. Score from 0 to 1.">
              Confidence
            </TermTooltip>
          </span>
          <span
            style={{
              fontSize: 20,
              fontWeight: 700,
              color: scoreColor(confidence),
              lineHeight: 1,
            }}
          >
            {confidence.toFixed(2)}
          </span>
        </div>
        <button
          onClick={() => setShowDetail(!showDetail)}
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            color: 'var(--text3)',
            fontSize: 11,
            padding: 0,
          }}
        >
          {showDetail ? 'Hide detail' : 'Show detail'}
        </button>
      </div>

      {showDetail && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {[
            {
              label: 'Merchant',
              score: merchantConfidence,
              definition: 'Are all these transactions from the same business?',
            },
            {
              label: 'Timing',
              score: timingConfidence,
              definition: 'Is there a consistent cadence?',
            },
            {
              label: 'Amount',
              score: amountConfidence,
              definition: 'Are the amounts consistent?',
            },
          ].map(({ label, score, definition }) => (
            <div key={label} style={{ display: 'flex', alignItems: 'center' }}>
              <span style={{ width: 64, fontSize: 12, color: 'var(--text2)' }}>
                <TermTooltip term={label} definition={definition}>
                  {label}
                </TermTooltip>
              </span>
              <ScoreBar score={score} />
              <span style={{ width: 32, fontSize: 11, color: 'var(--text3)', textAlign: 'right' }}>
                {score.toFixed(2)}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
