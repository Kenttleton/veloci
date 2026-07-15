import React, { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useRateFormat } from '../../contexts/RateFormatContext'
import { useJobs } from '../../contexts/JobsContext'
import { TermTooltip } from '../shared/TermTooltip'
import { LabelPill } from '../shared/LabelPill'
import { PendingDetailsLink } from '../shared/PendingBadge'
import { RateValue } from '../shared/RateValue'
// TODO(task-6-11): Entry will be replaced with EntryView from generated schemas
import type { EntryView } from '../../api/generated/velociAPI.schemas'

// EntryView from generated API has most fields but some differ; extend for compatibility
interface Entry extends EntryView {
  // These fields exist in EntryView: id, name, direction, entry_type, label_id, label_name,
  // actual_rate, projected_rate, drift_rate, tag, period, status
  // Interim alias for the fields used in StackPanel
}

interface LabelGroup {
  labelId: string | null
  labelName: string | null
  entries: Entry[]
}

interface StackPanelProps {
  entries: Entry[]
  loading: boolean
  pulsePeriod?: string
  isActual?: boolean
}

function groupByLabel(entries: Entry[]): LabelGroup[] {
  const map = new Map<string, LabelGroup>()
  const nullKey = '__null__'

  for (const entry of entries) {
    const key = entry.label_id ?? nullKey
    if (!map.has(key)) {
      map.set(key, {
        labelId: entry.label_id,
        labelName: entry.label_name,
        entries: [],
      })
    }
    map.get(key)!.entries.push(entry)
  }

  return Array.from(map.values())
}

function DriftBadge({ entry }: { entry: Entry }) {
  if (!entry.tag) return null
  const isHit = entry.tag === 'hit'
  return (
    <span
      style={{
        fontSize: 10,
        fontWeight: 700,
        padding: '1px 6px',
        borderRadius: 3,
        background: isHit
          ? 'color-mix(in srgb, var(--commit) 20%, transparent)'
          : 'color-mix(in srgb, var(--margin-pos) 20%, transparent)',
        color: isHit ? 'var(--commit)' : 'var(--margin-pos)',
        border: `1px solid ${isHit ? 'var(--commit)' : 'var(--margin-pos)'}`,
        marginLeft: 6,
      }}
    >
      <TermTooltip
        term={isHit ? 'Hit' : 'Boost'}
        definition={
          isHit
            ? 'An unexpected cost that pushed a commitment above its projected rate.'
            : 'An unexpected positive — a commitment cost less, or income exceeded expectation.'
        }
      >
        {isHit ? 'Hit' : 'Boost'}
      </TermTooltip>
    </span>
  )
}

export function StackPanel({ entries, loading, pulsePeriod, isActual = true }: StackPanelProps) {
  const { format, formatRate } = useRateFormat()
  const { hasRunningJobs, pendingJobId } = useJobs()
  const [collapsed, setCollapsed] = useState(false)
  const [expandedLabels, setExpandedLabels] = useState<Set<string>>(new Set())

  const incomeEntries = entries.filter((e) => e.direction === 'income')
  const expenseEntries = entries.filter((e) => e.direction === 'expense')

  const totalIncome = incomeEntries.reduce((sum, e) => sum + e.actual_rate, 0)
  const totalExpense = expenseEntries.reduce((sum, e) => sum + e.actual_rate, 0)
  const totalMargin = totalIncome - totalExpense
  const totalDrift = entries.reduce((sum, e) => sum + e.drift_rate, 0)

  const labelGroups = groupByLabel(expenseEntries)

  function toggleLabel(key: string) {
    setExpandedLabels((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  function driftColor(drift: number): string {
    if (drift > 0) return 'var(--margin-pos)'
    if (drift < 0) return 'var(--commit)'
    return 'var(--text3)'
  }

  function driftDisplay(drift: number): string {
    if (drift === 0) return '$0'
    const sign = drift > 0 ? '+' : '−'
    return `${sign}${formatRate(Math.abs(drift))}`
  }

  const colStyle: React.CSSProperties = {
    fontSize: 11,
    color: 'var(--text3)',
    fontWeight: 600,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
  }

  if (loading) {
    return (
      <div style={{ padding: '0 20px', background: 'var(--bg)' }}>
        <div style={{ height: 40, background: 'var(--surface)', borderRadius: 6, opacity: 0.4 }} />
      </div>
    )
  }

  return (
    <div
      style={{
        background: 'var(--bg)',
        borderTop: '1px solid var(--border)',
        flexShrink: 0,
      }}
    >
      {/* Panel header */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          padding: '10px 20px',
          borderBottom: collapsed ? 'none' : '1px solid var(--border)',
          cursor: 'pointer',
          userSelect: 'none',
        }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <span style={{ fontWeight: 600, fontSize: 13, color: 'var(--text)' }}>
          <TermTooltip term="Stack" definition="Waterfall breakdown of the Pulse period showing each commitment's rate and drift.">
            Stack
          </TermTooltip>
        </span>
        {pulsePeriod && (
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>— {pulsePeriod}</span>
        )}
        <span
          style={{
            fontSize: 10,
            background: 'var(--surface2)',
            border: isActual ? 'none' : '1px dashed var(--text3)',
            borderRadius: 3,
            padding: '0 5px',
            color: 'var(--text3)',
          }}
        >
          {isActual ? 'actual' : 'projected'}
        </span>
        <div style={{ flex: 1 }} />
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          entry · type · actual {format} · drift {format}
        </span>
        <span style={{ color: 'var(--text3)', marginLeft: 8 }}>
          {collapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
        </span>
      </div>

      {!collapsed && (
        <div style={{ maxHeight: 400, overflow: 'auto' }}>
          {/* Column headers */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              padding: '6px 20px',
              borderBottom: '1px solid var(--border)',
              gap: 8,
            }}
          >
            <div style={{ ...colStyle, width: 148 }}>Entry / type</div>
            <div style={{ ...colStyle, flex: 1 }}>Track</div>
            <div style={{ ...colStyle, width: 80, textAlign: 'right' }}>Actual {format}</div>
            <div style={{ ...colStyle, width: 72, textAlign: 'right' }}>
              <TermTooltip term="Drift" definition="Difference between actual and projected rate. +$X = ahead, −$X = behind.">Drift</TermTooltip> {format}
            </div>
          </div>

          {/* Income anchor row */}
          {incomeEntries.length > 0 && (
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                padding: '8px 20px',
                borderBottom: '1px solid var(--border)',
                gap: 8,
                background: 'var(--surface)',
              }}
            >
              <div style={{ width: 148 }}>
                <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--income)' }}>Income</div>
                <div style={{ fontSize: 11, color: 'var(--text3)' }}>anchor</div>
              </div>
              <div style={{ flex: 1 }}>
                <div style={{ height: 4, borderRadius: 2, background: 'var(--income)', width: '100%' }} />
              </div>
              <div
                style={{ width: 80, textAlign: 'right', color: 'var(--income)', fontWeight: 600, fontSize: 13 }}
              >
                <RateValue ratePerDay={totalIncome} />
              </div>
              <div style={{ width: 72, textAlign: 'right', color: 'var(--text3)', fontSize: 12 }}>—</div>
            </div>
          )}

          {/* Label groups */}
          {labelGroups.map((group) => {
            const groupKey = group.labelId ?? '__null__'
            const isExpanded = expandedLabels.has(groupKey)
            const groupTotal = group.entries.reduce((sum, e) => sum + e.actual_rate, 0)
            const groupDrift = group.entries.reduce((sum, e) => sum + e.drift_rate, 0)
            const groupPct = totalIncome > 0 ? (groupTotal / totalIncome) * 100 : 0

            return (
              <div key={groupKey}>
                {/* Label group header */}
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    padding: '7px 20px',
                    borderBottom: '1px solid var(--border)',
                    gap: 8,
                    cursor: 'pointer',
                    background: 'var(--bg)',
                    userSelect: 'none',
                  }}
                  onClick={() => toggleLabel(groupKey)}
                >
                  <div style={{ width: 148, display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ color: 'var(--text3)', flexShrink: 0 }}>
                      {isExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                    </span>
                    <LabelPill name={group.labelName} />
                  </div>
                  <div style={{ flex: 1 }}>
                    <div
                      style={{
                        height: 4,
                        borderRadius: 2,
                        background: 'var(--commit)',
                        width: `${Math.min(groupPct, 100)}%`,
                        opacity: 0.6,
                      }}
                    />
                  </div>
                  <div style={{ width: 80, textAlign: 'right', color: 'var(--text2)', fontSize: 12 }}>
                    <RateValue ratePerDay={groupTotal} />
                  </div>
                  <div
                    style={{
                      width: 72,
                      textAlign: 'right',
                      fontSize: 12,
                      color: driftColor(groupDrift),
                    }}
                  >
                    {driftDisplay(groupDrift)}
                  </div>
                </div>

                {/* Commitment rows */}
                {isExpanded &&
                  group.entries.map((entry) => {
                    const entryPct = totalIncome > 0 ? (entry.actual_rate / totalIncome) * 100 : 0
                    const isPending = hasRunningJobs
                    return (
                      <div
                        key={entry.id}
                        className={isPending ? 'pending' : undefined}
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          padding: '6px 20px 6px 32px',
                          borderBottom: '1px solid var(--border)',
                          gap: 8,
                          background:
                            entry.tag === 'hit'
                              ? 'color-mix(in srgb, var(--surface) 90%, var(--commit) 10%)'
                              : entry.tag === 'boost'
                                ? 'color-mix(in srgb, var(--surface) 90%, var(--margin-pos) 10%)'
                                : 'var(--surface)',
                          borderLeftColor:
                            entry.tag === 'hit'
                              ? 'var(--commit)'
                              : entry.tag === 'boost'
                                ? 'var(--margin-pos)'
                                : isPending
                                  ? 'var(--accent)'
                                  : undefined,
                          borderLeftWidth:
                            entry.tag ? 2 : isPending ? 2 : undefined,
                          borderLeftStyle: entry.tag || isPending ? 'solid' : undefined,
                        }}
                      >
                        <div style={{ width: 148 - 12, display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                          <span style={{ fontSize: 13, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 120 }}>
                            {entry.name}
                          </span>
                          <DriftBadge entry={entry} />
                        </div>
                        <div style={{ flex: 1 }}>
                          <div
                            style={{
                              height: 4,
                              borderRadius: 2,
                              background: 'var(--commit)',
                              width: `${Math.min(entryPct, 100)}%`,
                            }}
                          />
                        </div>
                        <div
                          style={{
                            width: 80,
                            textAlign: 'right',
                            fontSize: 13,
                            color: 'var(--text)',
                          }}
                        >
                          <RateValue ratePerDay={entry.actual_rate} />
                        </div>
                        <div
                          style={{
                            width: 72,
                            textAlign: 'right',
                            fontSize: 12,
                            color: driftColor(entry.drift_rate),
                          }}
                        >
                          {driftDisplay(entry.drift_rate)}
                        </div>
                        {isPending && (
                          <div style={{ position: 'absolute', right: 20 }}>
                            <PendingDetailsLink jobId={pendingJobId} />
                          </div>
                        )}
                      </div>
                    )
                  })}
              </div>
            )
          })}

          {/* Margin anchor row */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              padding: '8px 20px',
              gap: 8,
              background: 'var(--surface)',
            }}
          >
            <div style={{ width: 148 }}>
              <div style={{ fontSize: 13, fontWeight: 600, color: totalMargin >= 0 ? 'var(--margin-pos)' : 'var(--margin-neg)' }}>
                <TermTooltip term="Margin" definition="Income minus all commitments at the selected rate.">Margin</TermTooltip>
              </div>
              <div style={{ fontSize: 11, color: 'var(--text3)' }}>anchor</div>
            </div>
            <div style={{ flex: 1 }}>
              {totalIncome > 0 && totalMargin > 0 && (
                <div
                  style={{
                    height: 4,
                    borderRadius: 2,
                    background: 'var(--margin-pos)',
                    width: `${Math.min((totalMargin / totalIncome) * 100, 100)}%`,
                  }}
                />
              )}
            </div>
            <div
              style={{
                width: 80,
                textAlign: 'right',
                fontWeight: 600,
                fontSize: 13,
                color: totalMargin >= 0 ? 'var(--margin-pos)' : 'var(--margin-neg)',
              }}
            >
              <RateValue ratePerDay={totalMargin} />
            </div>
            <div
              style={{
                width: 72,
                textAlign: 'right',
                fontSize: 12,
                color: driftColor(totalDrift),
                fontWeight: 600,
              }}
            >
              {driftDisplay(totalDrift)}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
