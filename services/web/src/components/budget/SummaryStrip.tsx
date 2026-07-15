import React from 'react'
import { useRateFormat } from '../../contexts/RateFormatContext'
import { useJobs } from '../../contexts/JobsContext'
import { TermTooltip } from '../shared/TermTooltip'
import { PendingDetailsLink } from '../shared/PendingBadge'
// TODO(task-6-11): SnapshotSummary will be replaced with SnapshotSummaryView from generated schemas
import type { SnapshotSummaryView } from '../../api/generated/velociAPI.schemas'

// SnapshotSummaryView from generated API doesn't have actual/period fields yet
// Extend with the fields used in this component
interface SnapshotSummary extends SnapshotSummaryView {
  period?: string
  actual?: boolean
}

interface SummaryStripProps {
  summary: SnapshotSummary | null
  loading: boolean
}

export function SummaryStrip({ summary, loading }: SummaryStripProps) {
  const { formatRate, format } = useRateFormat()
  const { hasRunningJobs, pendingJobId } = useJobs()

  const isPending = hasRunningJobs

  const incomeRate = summary?.income_rate ?? 0
  const commitRate = summary?.commitments_rate ?? 0
  const marginRate = summary?.margin_rate ?? 0
  const isActual = summary?.actual ?? true
  const period = summary?.period ?? ''

  const commitPct = incomeRate > 0 ? Math.min((commitRate / incomeRate) * 100, 100) : 0
  const marginPct = Math.max(0, 100 - commitPct)
  const isNegativeMargin = marginRate < 0

  function cellStyle(isPendingCell: boolean): React.CSSProperties {
    return {
      flex: 1,
      padding: '12px 16px',
      background: 'var(--surface)',
      borderRadius: 6,
      opacity: isPendingCell ? 0.55 : 1,
      borderLeft: isPendingCell ? '2px solid var(--accent)' : 'none',
      minWidth: 0,
    }
  }

  function valueDisplay(ratePerDay: number, isNeg = false) {
    return (
      <div
        style={{
          fontSize: 22,
          fontWeight: 600,
          color: isNeg ? 'var(--margin-neg)' : 'var(--text)',
          lineHeight: 1.2,
        }}
      >
        {formatRate(ratePerDay)}
      </div>
    )
  }

  if (loading) {
    return (
      <div style={{ display: 'flex', gap: 8, padding: '12px 20px 8px' }}>
        {[0, 1, 2].map((i) => (
          <div
            key={i}
            style={{
              flex: 1,
              height: 72,
              background: 'var(--surface)',
              borderRadius: 6,
              opacity: 0.4,
            }}
          />
        ))}
      </div>
    )
  }

  return (
    <div style={{ padding: '12px 20px 8px' }}>
      {/* Equation row */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'stretch' }}>
        {/* Income cell */}
        <div style={cellStyle(isPending)}>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
            <TermTooltip term="Income" definition="Total incoming money at the selected rate period." />
          </div>
          {valueDisplay(incomeRate)}
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
            {format} · {period}
            {' '}
            <span
              style={{
                background: 'var(--surface2)',
                border: isActual ? 'none' : '1px dashed var(--text3)',
                borderRadius: 3,
                padding: '0 5px',
                fontSize: 10,
                color: 'var(--text3)',
              }}
            >
              {isActual ? 'actual' : 'projected'}
            </span>
          </div>
          {isPending && <PendingDetailsLink jobId={pendingJobId} />}
        </div>

        {/* Minus operator */}
        <div style={{ display: 'flex', alignItems: 'center', color: 'var(--text3)', fontSize: 18, flexShrink: 0 }}>
          −
        </div>

        {/* Commitments cell */}
        <div style={cellStyle(isPending)}>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
            <TermTooltip term="Commitments" definition="Sum of all recurring expenses and planned spending at the selected rate." />
          </div>
          {valueDisplay(commitRate)}
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
            {format} · {period}
            {' '}
            <span
              style={{
                background: 'var(--surface2)',
                border: isActual ? 'none' : '1px dashed var(--text3)',
                borderRadius: 3,
                padding: '0 5px',
                fontSize: 10,
                color: 'var(--text3)',
              }}
            >
              {isActual ? 'actual' : 'projected'}
            </span>
          </div>
          {isPending && <PendingDetailsLink jobId={pendingJobId} />}
        </div>

        {/* Equals operator */}
        <div style={{ display: 'flex', alignItems: 'center', color: 'var(--text3)', fontSize: 18, flexShrink: 0 }}>
          =
        </div>

        {/* Margin cell */}
        <div
          style={{
            ...cellStyle(isPending),
            background: isNegativeMargin
              ? 'color-mix(in srgb, var(--surface) 85%, var(--margin-neg) 15%)'
              : 'var(--surface)',
          }}
        >
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
            <TermTooltip term="Margin" definition="Income minus all commitments at the selected rate." />
          </div>
          {valueDisplay(marginRate, isNegativeMargin)}
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
            {format} · {Math.round(marginPct)}%
            {' '}
            <span
              style={{
                background: 'var(--surface2)',
                border: isActual ? 'none' : '1px dashed var(--text3)',
                borderRadius: 3,
                padding: '0 5px',
                fontSize: 10,
                color: 'var(--text3)',
              }}
            >
              {isActual ? 'actual' : 'projected'}
            </span>
          </div>
          {isPending && <PendingDetailsLink jobId={pendingJobId} />}
        </div>
      </div>

      {/* Proportion bar */}
      <div
        style={{
          height: 3,
          borderRadius: 2,
          overflow: 'hidden',
          marginTop: 8,
          background: 'var(--surface)',
          opacity: isPending ? 0.55 : 1,
        }}
      >
        {isNegativeMargin ? (
          <div style={{ width: '100%', height: '100%', background: 'var(--margin-neg)' }} />
        ) : (
          <div style={{ display: 'flex', height: '100%' }}>
            <div
              style={{
                width: `${commitPct}%`,
                background: 'var(--commit)',
                transition: 'width 0.3s',
              }}
            />
            <div
              style={{
                width: `${marginPct}%`,
                background: 'var(--margin-pos)',
                transition: 'width 0.3s',
              }}
            />
          </div>
        )}
      </div>

      {/* Labels under proportion bar */}
      <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 3 }}>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {Math.round(commitPct)}% commitments
        </span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {Math.round(marginPct)}% margin
        </span>
      </div>
    </div>
  )
}
