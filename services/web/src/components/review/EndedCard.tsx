import { useState } from 'react'
import { useRateFormat } from '../../contexts/RateFormatContext'
import { approveReview, rejectReview, updateReview } from '../../api/resources'
import type { ReviewItem } from '../../api/resources'

interface EndedCardProps {
  item: ReviewItem
  onAction: () => void
}

type EndedChoice = 'gap' | 'ended'

function formatFullDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' })
}

export function EndedCard({ item, onAction }: EndedCardProps) {
  const { formatRate } = useRateFormat()
  const [choice, setChoice] = useState<EndedChoice | null>(null)
  const [endedDate, setEndedDate] = useState<string>(
    item.last_seen_date ? item.last_seen_date.split('T')[0] : new Date().toISOString().split('T')[0],
  )
  const [submitting, setSubmitting] = useState(false)

  async function handleConfirm() {
    if (!choice) return
    setSubmitting(true)
    try {
      if (choice === 'gap') {
        await rejectReview(item.id)
      } else {
        await updateReview(item.id, { end_date: endedDate })
        await approveReview(item.id, { end_date: endedDate })
      }
      onAction()
    } catch {
      setSubmitting(false)
    }
  }

  async function handleDismiss() {
    setSubmitting(true)
    try {
      await rejectReview(item.id)
      onAction()
    } catch {
      setSubmitting(false)
    }
  }

  return (
    <div
      style={{
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: 4,
        padding: 16,
        marginBottom: 12,
      }}
    >
      {/* Alert badge */}
      <div style={{ marginBottom: 6 }}>
        <span
          style={{
            fontSize: 10,
            fontWeight: 700,
            textTransform: 'uppercase',
            letterSpacing: '0.08em',
            color: 'var(--text3)',
            background: 'var(--surface2)',
            padding: '2px 7px',
            borderRadius: 3,
          }}
        >
          Ended
        </span>
      </div>

      <h3 style={{ margin: '0 0 12px', fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>
        {item.suggested_name}
      </h3>

      {/* Info */}
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          gap: 4,
          marginBottom: 16,
          padding: 10,
          background: 'var(--surface2)',
          borderRadius: 4,
        }}
      >
        <div style={{ display: 'flex', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)', width: 100, flexShrink: 0 }}>Last seen:</span>
          <span style={{ fontSize: 12, color: 'var(--text)' }}>
            {item.last_seen_date ? formatFullDate(item.last_seen_date) : '—'}
          </span>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)', width: 100, flexShrink: 0 }}>Expected next:</span>
          <span style={{ fontSize: 12, color: 'var(--text)' }}>
            {item.next_due_date ? formatFullDate(item.next_due_date) : '—'}
            {item.days_overdue !== undefined && item.days_overdue > 0 && (
              <span style={{ color: 'var(--commit)', marginLeft: 8 }}>
                ({item.days_overdue} days overdue)
              </span>
            )}
          </span>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)', width: 100, flexShrink: 0 }}>Rate:</span>
          <span style={{ fontSize: 12, color: 'var(--text)' }}>
            {item.current_rate_per_day != null ? formatRate(item.current_rate_per_day) : '—'}
          </span>
        </div>
      </div>

      {/* Choice */}
      <div style={{ marginBottom: 16 }}>
        <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 8, fontWeight: 500 }}>
          What happened?
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <label style={{ display: 'flex', gap: 8, cursor: 'pointer', alignItems: 'center' }}>
            <input
              type="radio"
              name={`ended-choice-${item.id}`}
              value="gap"
              checked={choice === 'gap'}
              onChange={() => setChoice('gap')}
              style={{ accentColor: 'var(--accent)' }}
            />
            <span style={{ fontSize: 13, color: 'var(--text)' }}>
              Temporary gap — keep entry active
            </span>
          </label>

          <label style={{ display: 'flex', gap: 8, cursor: 'pointer', alignItems: 'flex-start' }}>
            <input
              type="radio"
              name={`ended-choice-${item.id}`}
              value="ended"
              checked={choice === 'ended'}
              onChange={() => setChoice('ended')}
              style={{ accentColor: 'var(--accent)', marginTop: 2 }}
            />
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <span style={{ fontSize: 13, color: 'var(--text)' }}>Ended on:</span>
              <input
                type="date"
                value={endedDate}
                onChange={(e) => { setEndedDate(e.target.value); setChoice('ended') }}
                style={{
                  background: 'var(--surface2)',
                  border: '1px solid var(--border)',
                  borderRadius: 4,
                  padding: '3px 7px',
                  color: 'var(--text)',
                  fontSize: 12,
                  cursor: 'pointer',
                }}
              />
            </div>
          </label>
        </div>
      </div>

      {/* Action row */}
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', alignItems: 'center' }}>
        <button
          onClick={() => void handleDismiss()}
          disabled={submitting}
          style={{
            background: 'none',
            border: 'none',
            cursor: submitting ? 'default' : 'pointer',
            color: 'var(--text3)',
            fontSize: 13,
            padding: '6px 10px',
          }}
        >
          Dismiss
        </button>
        <button
          onClick={() => void handleConfirm()}
          disabled={!choice || submitting}
          style={{
            background: 'var(--accent)',
            border: 'none',
            borderRadius: 4,
            cursor: (!choice || submitting) ? 'not-allowed' : 'pointer',
            color: '#fff',
            fontSize: 13,
            fontWeight: 600,
            padding: '6px 14px',
            opacity: (!choice || submitting) ? 0.4 : 1,
          }}
        >
          Confirm →
        </button>
      </div>
    </div>
  )
}
