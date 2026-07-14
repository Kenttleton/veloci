import { useState, useEffect } from 'react'
import { ConfidenceComponent } from './ConfidenceComponent'
import { useRateFormat } from '../../contexts/RateFormatContext'
import { approveReview, rejectReview, updateReview, getLabels, createLabel } from '../../api/resources'
import type { ReviewItem, Label } from '../../api/resources'

interface NewCardProps {
  item: ReviewItem
  onAction: () => void
}

type EntryType = 'standing' | 'variable' | 'irregular'

const ENTRY_TYPE_LABELS: Record<EntryType, string> = {
  standing: 'Standing',
  variable: 'Variable',
  irregular: 'Irregular',
}

function formatDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

function formatAmount(cents: number): string {
  return (Math.abs(cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

export function NewCard({ item, onAction }: NewCardProps) {
  const { formatRate } = useRateFormat()
  const [labels, setLabels] = useState<Label[]>([])
  const [selectedLabel, setSelectedLabel] = useState<string | null>(null)
  const [selectedType, setSelectedType] = useState<EntryType>(item.suggested_entry_type)
  const [showNewLabel, setShowNewLabel] = useState(false)
  const [newLabelName, setNewLabelName] = useState('')
  const [showAllSamples, setShowAllSamples] = useState(false)
  const [showInlineEditor, setShowInlineEditor] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    getLabels().then(setLabels).catch(() => {})
  }, [])

  async function handleApprove() {
    setSubmitting(true)
    try {
      await updateReview(item.id, {
        entry_type: selectedType,
        label_id: selectedLabel ?? undefined,
      })
      await approveReview(item.id)
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

  async function handleCreateLabel() {
    if (!newLabelName.trim()) return
    try {
      const label = await createLabel(newLabelName.trim())
      setLabels((prev) => [...prev, label])
      setSelectedLabel(label.id)
      setShowNewLabel(false)
      setNewLabelName('')
    } catch {
      //
    }
  }

  const displayedSamples = showAllSamples
    ? item.sample_merchants
    : item.sample_merchants.slice(0, 3)

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
        {item.transaction_count} transactions · {formatRate(item.suggested_rate_per_day)}{' '}
        {item.recurrence_anchor && (
          <span style={{ color: 'var(--text3)' }}>· {item.recurrence_anchor}</span>
        )}
      </p>

      {/* Sample transactions */}
      <div style={{ marginBottom: 12 }}>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
          Sample transactions
        </div>
        {displayedSamples.map((s, i) => (
          <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '3px 0' }}>
            <span style={{ width: 56, fontSize: 12, color: 'var(--text2)', flexShrink: 0 }}>
              {formatDate(s.date)}
            </span>
            <span style={{ flex: 1, fontSize: 12, color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {s.payee}
            </span>
            <span style={{ fontSize: 12, color: 'var(--text2)', flexShrink: 0 }}>
              {formatAmount(s.amount_cents)}
            </span>
          </div>
        ))}
        {item.sample_merchants.length > 3 && !showAllSamples && (
          <button
            onClick={() => setShowAllSamples(true)}
            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', fontSize: 12, padding: 0, marginTop: 4 }}
          >
            show {item.sample_merchants.length - 3} more
          </button>
        )}
      </div>

      {/* Confidence component */}
      <ConfidenceComponent
        confidence={item.confidence}
        merchantConfidence={item.merchant_confidence}
        timingConfidence={item.timing_confidence}
        amountConfidence={item.amount_confidence}
        defaultExpanded={true}
      />

      {/* Label + type assignment */}
      <div style={{ display: 'flex', gap: 12, marginBottom: 12, flexWrap: 'wrap' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text3)' }}>Label:</span>
          <select
            value={selectedLabel ?? ''}
            onChange={(e) => {
              const val = e.target.value
              if (val === '__new__') {
                setShowNewLabel(true)
              } else {
                setSelectedLabel(val || null)
              }
            }}
            style={{
              background: 'var(--surface2)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              padding: '4px 8px',
              color: selectedLabel ? 'var(--text)' : 'var(--text3)',
              fontSize: 12,
              cursor: 'pointer',
            }}
          >
            <option value="">No label</option>
            {labels.map((l) => (
              <option key={l.id} value={l.id}>{l.name}</option>
            ))}
            <option value="__new__">New label...</option>
          </select>
        </div>

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

      {/* Inline new label input */}
      {showNewLabel && (
        <div style={{ display: 'flex', gap: 6, marginBottom: 12 }}>
          <input
            type="text"
            placeholder="Label name"
            value={newLabelName}
            autoFocus
            onChange={(e) => setNewLabelName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void handleCreateLabel()
              if (e.key === 'Escape') { setShowNewLabel(false); setNewLabelName('') }
            }}
            style={{
              background: 'var(--surface2)',
              border: '1px solid var(--accent)',
              borderRadius: 4,
              padding: '4px 8px',
              color: 'var(--text)',
              fontSize: 12,
              outline: 'none',
              flex: 1,
            }}
          />
          <button
            onClick={() => void handleCreateLabel()}
            style={{
              background: 'var(--accent)',
              border: 'none',
              borderRadius: 4,
              padding: '4px 10px',
              color: '#fff',
              fontSize: 12,
              cursor: 'pointer',
            }}
          >
            Create
          </button>
          <button
            onClick={() => { setShowNewLabel(false); setNewLabelName('') }}
            style={{
              background: 'none',
              border: '1px solid var(--border)',
              borderRadius: 4,
              padding: '4px 10px',
              color: 'var(--text2)',
              fontSize: 12,
              cursor: 'pointer',
            }}
          >
            Cancel
          </button>
        </div>
      )}

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
            Name: {item.suggested_name} · Rate: {formatRate(item.suggested_rate_per_day)}
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
