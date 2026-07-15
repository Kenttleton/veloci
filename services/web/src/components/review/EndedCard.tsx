import { useRateFormat } from '../../contexts/RateFormatContext'
import { useApproveReview, useRejectReview, useUpdateReview } from '../../api/generated/velociAPI'
import type { ReviewView } from '../../api/generated/velociAPI.schemas'
import { useState } from 'react'

interface EndedCardProps {
  item: ReviewView
  onAction: () => void
}

type EndedChoice = 'gap' | 'ended'

interface EndedConditions {
  last_seen_date?: string
  next_due_date?: string
  days_overdue?: number
  current_rate_per_day?: number
}

function formatFullDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' })
}

export function EndedCard({ item, onAction }: EndedCardProps) {
  const { formatRate } = useRateFormat()
  const conditions = (item.suggested_conditions ?? {}) as EndedConditions

  const [choice, setChoice] = useState<EndedChoice | null>(null)

  const approveMutation = useApproveReview()
  const rejectMutation = useRejectReview()
  const updateMutation = useUpdateReview()

  const submitting = approveMutation.isPending || rejectMutation.isPending || updateMutation.isPending

  async function handleConfirm() {
    if (!choice) return
    try {
      if (choice === 'gap') {
        await rejectMutation.mutateAsync({ id: item.id })
      } else {
        await updateMutation.mutateAsync({ id: item.id, data: { status: 'ended' } })
        await approveMutation.mutateAsync({ id: item.id })
      }
      onAction()
    } catch {
      //
    }
  }

  async function handleDismiss() {
    try {
      await rejectMutation.mutateAsync({ id: item.id })
      onAction()
    } catch {
      //
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
            {conditions.last_seen_date ? formatFullDate(conditions.last_seen_date) : '—'}
          </span>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)', width: 100, flexShrink: 0 }}>Expected next:</span>
          <span style={{ fontSize: 12, color: 'var(--text)' }}>
            {conditions.next_due_date ? formatFullDate(conditions.next_due_date) : '—'}
            {conditions.days_overdue !== undefined && conditions.days_overdue > 0 && (
              <span style={{ color: 'var(--commit)', marginLeft: 8 }}>
                ({conditions.days_overdue} days overdue)
              </span>
            )}
          </span>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)', width: 100, flexShrink: 0 }}>Rate:</span>
          <span style={{ fontSize: 12, color: 'var(--text)' }}>
            {conditions.current_rate_per_day != null ? formatRate(conditions.current_rate_per_day) : '—'}
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

          <label style={{ display: 'flex', gap: 8, cursor: 'pointer', alignItems: 'center' }}>
            <input
              type="radio"
              name={`ended-choice-${item.id}`}
              value="ended"
              checked={choice === 'ended'}
              onChange={() => setChoice('ended')}
              style={{ accentColor: 'var(--accent)' }}
            />
            <span style={{ fontSize: 13, color: 'var(--text)' }}>This entry has ended</span>
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
