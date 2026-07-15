import { useState } from 'react'
import { format, parseISO } from 'date-fns'
import { ConfidenceComponent } from './ConfidenceComponent'
import { useRateFormat } from '../../contexts/RateFormatContext'
import { useApproveReview, useRejectReview, useUpdateReview } from '../../api/generated/velociAPI'
import type { ReviewView } from '../../api/generated/velociAPI.schemas'

interface DriftCardProps {
  item: ReviewView
  onAction: () => void
}

type DriftChoice = 'correction' | 'version'

interface DriftConditions {
  old_rate_per_day?: number
  new_rate_per_day?: number
  old_timing?: string
  new_timing?: string
  transaction_evidence?: Array<{ date: string; payee: string; amount_cents: number }>
  has_manual_projection?: boolean
  manual_projection_per_day?: number
}


export function DriftCard({ item, onAction }: DriftCardProps) {
  const { formatRate } = useRateFormat()
  const [choice, setChoice] = useState<DriftChoice | null>(null)

  const approveMutation = useApproveReview()
  const rejectMutation = useRejectReview()
  const updateMutation = useUpdateReview()

  const submitting = approveMutation.isPending || rejectMutation.isPending || updateMutation.isPending

  const conditions = (item.suggested_conditions ?? {}) as DriftConditions
  const oldRate = conditions.old_rate_per_day ?? 0
  const newRate = conditions.new_rate_per_day ?? 0
  const delta = newRate - oldRate
  const deltaSign = delta >= 0 ? '+' : '−'
  const deltaColor = delta >= 0 ? 'var(--commit)' : 'var(--income)'

  async function handleAccept() {
    if (!choice) return
    try {
      await updateMutation.mutateAsync({
        id: item.id,
        data: { status: choice === 'correction' ? 'correction' : 'version' },
      })
      await approveMutation.mutateAsync({ id: item.id })
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
            color: 'var(--commit)',
            background: 'color-mix(in srgb, var(--commit) 15%, transparent)',
            padding: '2px 7px',
            borderRadius: 3,
          }}
        >
          Drift
        </span>
      </div>

      <h3 style={{ margin: '0 0 4px', fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>
        {item.suggested_name}
      </h3>

      <p style={{ margin: '0 0 12px', fontSize: 12, color: 'var(--text3)' }}>Pattern changed</p>

      {/* Was / Now comparison */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: '1fr 1fr auto',
          gap: 12,
          marginBottom: 12,
          padding: 10,
          background: 'var(--surface2)',
          borderRadius: 4,
        }}
      >
        <div>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>Was</div>
          <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text)' }}>
            {formatRate(oldRate)}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text2)' }}>
            {conditions.old_timing ?? '—'}
          </div>
        </div>
        <div>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>Now</div>
          <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text)' }}>
            {formatRate(newRate)}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text2)' }}>
            {conditions.new_timing ?? '—'}
            {conditions.old_timing === conditions.new_timing && (
              <span style={{ color: 'var(--text3)', marginLeft: 6 }}>(timing unchanged)</span>
            )}
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', justifyContent: 'center', alignItems: 'flex-end' }}>
          <span style={{ fontSize: 15, fontWeight: 700, color: deltaColor }}>
            {deltaSign}{formatRate(Math.abs(delta))}
          </span>
        </div>
      </div>

      {/* Evidence transactions */}
      {conditions.transaction_evidence && conditions.transaction_evidence.length > 0 && (
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 6 }}>
            Based on {conditions.transaction_evidence.length} new transactions
          </div>
          {conditions.transaction_evidence.map((tx, i) => (
            <div key={i} style={{ display: 'flex', gap: 8, padding: '2px 0', fontSize: 12 }}>
              <span style={{ width: 56, color: 'var(--text2)', flexShrink: 0 }}>{format(parseISO(tx.date), 'MMM d')}</span>
              <span style={{ flex: 1, color: 'var(--text2)' }}>{tx.payee}</span>
              <span style={{ color: 'var(--text2)', flexShrink: 0 }}>{(Math.abs(tx.amount_cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })}</span>
            </div>
          ))}
        </div>
      )}

      {/* Confidence component — collapsed by default for drift */}
      <ConfidenceComponent
        confidence={item.confidence ?? 0}
        merchantConfidence={item.merchant_confidence ?? 0}
        timingConfidence={item.timing_confidence ?? 0}
        amountConfidence={item.amount_confidence ?? 0}
        defaultExpanded={false}
      />

      {/* Manual override warning */}
      {conditions.has_manual_projection && conditions.manual_projection_per_day && (
        <div
          style={{
            fontSize: 12,
            color: 'var(--text3)',
            padding: '6px 10px',
            background: 'var(--surface2)',
            borderRadius: 4,
            marginBottom: 12,
            borderLeft: '2px solid var(--text3)',
          }}
        >
          You previously set a custom projection of {formatRate(conditions.manual_projection_per_day)} — accepting this change will replace it.
        </div>
      )}

      {/* Correction vs Version choice */}
      <div style={{ marginBottom: 12 }}>
        <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 8, fontWeight: 500 }}>
          How should this change be recorded?
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <label style={{ display: 'flex', gap: 8, cursor: 'pointer', alignItems: 'flex-start' }}>
            <input
              type="radio"
              name={`drift-choice-${item.id}`}
              value="correction"
              checked={choice === 'correction'}
              onChange={() => setChoice('correction')}
              style={{ marginTop: 2, accentColor: 'var(--accent)' }}
            />
            <div>
              <div style={{ fontSize: 13, color: 'var(--text)', fontWeight: 500 }}>Correction</div>
              <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                Update this entry in place and recompute history from its start date
              </div>
            </div>
          </label>
          <label style={{ display: 'flex', gap: 8, cursor: 'pointer', alignItems: 'flex-start' }}>
            <input
              type="radio"
              name={`drift-choice-${item.id}`}
              value="version"
              checked={choice === 'version'}
              onChange={() => setChoice('version')}
              style={{ marginTop: 2, accentColor: 'var(--accent)' }}
            />
            <div>
              <div style={{ fontSize: 13, color: 'var(--text)', fontWeight: 500 }}>Version</div>
              <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                Close this entry and open a new version starting today
              </div>
            </div>
          </label>
        </div>
        {!choice && (
          <p style={{ fontSize: 11, color: 'var(--text3)', margin: '8px 0 0' }}>
            Choose how to record this change before accepting.
          </p>
        )}
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
          style={{
            background: 'none',
            border: '1px solid var(--border)',
            borderRadius: 4,
            cursor: 'pointer',
            color: 'var(--text2)',
            fontSize: 13,
            padding: '6px 12px',
          }}
        >
          Edit entry
        </button>
        <button
          onClick={() => void handleAccept()}
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
          Accept change →
        </button>
      </div>
    </div>
  )
}
