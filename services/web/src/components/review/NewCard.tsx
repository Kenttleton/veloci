import { useState } from 'react'
import { ConfidenceComponent } from './ConfidenceComponent'
import { useRateFormat } from '../../contexts/RateFormatContext'
import {
  useApproveReview,
  useRejectReview,
  useUpdateReview,
} from '../../api/generated/velociAPI'
import type { ReviewView } from '../../api/generated/velociAPI.schemas'

interface NewCardProps {
  item: ReviewView
  onAction: () => void
}

interface NewConditions {
  recurrence_anchor?: string
}

type EntryType = 'standing' | 'variable' | 'irregular'

const ENTRY_TYPE_LABELS: Record<EntryType, string> = {
  standing: 'Standing',
  variable: 'Variable',
  irregular: 'Irregular',
}


export function NewCard({ item, onAction }: NewCardProps) {
  const { formatRate } = useRateFormat()
  const conditions = (item.suggested_conditions ?? {}) as NewConditions

  const [selectedType, setSelectedType] = useState<EntryType>((item.suggested_entry_type as EntryType) || 'standing')
  const [showAllSamples, setShowAllSamples] = useState(false)
  const [showInlineEditor, setShowInlineEditor] = useState(false)

  const approveMutation = useApproveReview()
  const rejectMutation = useRejectReview()
  const updateMutation = useUpdateReview()

  const submitting = approveMutation.isPending || rejectMutation.isPending || updateMutation.isPending

  async function handleApprove() {
    try {
      await updateMutation.mutateAsync({
        id: item.id,
        data: { status: selectedType },
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

  const sampleMerchants = item.sample_merchants ?? []
  const displayedSamples = showAllSamples ? sampleMerchants : sampleMerchants.slice(0, 3)

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
      {/* Alert type badge */}
      <div style={{ marginBottom: 6 }}>
        <span
          style={{
            fontSize: 10,
            fontWeight: 700,
            textTransform: 'uppercase',
            letterSpacing: '0.08em',
            color: 'var(--accent)',
            background: 'color-mix(in srgb, var(--accent) 15%, transparent)',
            padding: '2px 7px',
            borderRadius: 3,
          }}
        >
          New
        </span>
      </div>

      <h3 style={{ margin: '0 0 8px', fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>
        {item.suggested_name}
      </h3>

      {/* Summary line */}
      <p style={{ margin: '0 0 12px', fontSize: 13, color: 'var(--text2)' }}>
        {item.matched_transaction_count} transactions · {formatRate(item.suggested_rate_per_day ?? 0)}{' '}
        {conditions.recurrence_anchor && (
          <span style={{ color: 'var(--text3)' }}>· {conditions.recurrence_anchor}</span>
        )}
      </p>

      {/* Sample transactions */}
      {sampleMerchants.length > 0 && (
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
            Sample transactions
          </div>
          {displayedSamples.map((name, i) => (
            <div key={i} style={{ padding: '3px 0', fontSize: 12, color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {name}
            </div>
          ))}
          {sampleMerchants.length > 3 && !showAllSamples && (
            <button
              onClick={() => setShowAllSamples(true)}
              style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', fontSize: 12, padding: 0, marginTop: 4 }}
            >
              show {sampleMerchants.length - 3} more
            </button>
          )}
        </div>
      )}

      {/* Confidence component */}
      <ConfidenceComponent
        confidence={item.confidence ?? 0}
        merchantConfidence={item.merchant_confidence ?? 0}
        timingConfidence={item.timing_confidence ?? 0}
        amountConfidence={item.amount_confidence ?? 0}
        defaultExpanded={true}
      />

      {/* Type assignment */}
      <div style={{ display: 'flex', gap: 12, marginBottom: 12, flexWrap: 'wrap' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)' }}>Type:</span>
          <select
            value={selectedType}
            onChange={(e) => setSelectedType(e.target.value as EntryType)}
            style={{
              background: 'var(--surface2)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              padding: '4px 8px',
              color: 'var(--text)',
              fontSize: 12,
              cursor: 'pointer',
            }}
          >
            {(Object.keys(ENTRY_TYPE_LABELS) as EntryType[]).map((t) => (
              <option key={t} value={t}>{ENTRY_TYPE_LABELS[t]}</option>
            ))}
          </select>
        </div>
      </div>

      {/* Inline editor panel */}
      {showInlineEditor && (
        <div
          style={{
            padding: 12,
            background: 'var(--surface2)',
            borderRadius: 4,
            marginBottom: 12,
            border: '1px solid var(--border)',
          }}
        >
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 8 }}>
            Inline editor — common fields (name, type, label, period, projected rate).
            For condition logic, use the full entry editor.
          </div>
          <div style={{ fontSize: 12, color: 'var(--text3)' }}>
            Name: {item.suggested_name} · Rate: {formatRate(item.suggested_rate_per_day ?? 0)}
          </div>
        </div>
      )}

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
          onClick={() => setShowInlineEditor(!showInlineEditor)}
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
          onClick={() => void handleApprove()}
          disabled={submitting}
          style={{
            background: 'var(--accent)',
            border: 'none',
            borderRadius: 4,
            cursor: submitting ? 'default' : 'pointer',
            color: '#fff',
            fontSize: 13,
            fontWeight: 600,
            padding: '6px 14px',
            opacity: submitting ? 0.6 : 1,
          }}
        >
          Approve →
        </button>
      </div>
    </div>
  )
}
