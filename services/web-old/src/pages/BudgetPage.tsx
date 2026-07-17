import { useState } from 'react'
import { useRateFormat } from '../contexts/RateFormatContext'
import type { RateFormat } from '../contexts/RateFormatContext'
import { SummaryStrip } from '../components/budget/SummaryStrip'
import { HorizonGraph } from '../components/budget/HorizonGraph'
import { StackPanel } from '../components/budget/StackPanel'
import { useListEntriesInfinite } from '../api/cursorQuery'
import { useGetSnapshotSummary } from '../api/generated/velociAPI'

export function BudgetPage() {
  const { format, setFormat } = useRateFormat()
  // Use a budget node id (entity-level snapshot) for the horizon
  const [horizonNodeId] = useState<string | null>(null)

  const { data: summaryData, isFetching: summaryLoading } = useGetSnapshotSummary()
  const summary = summaryData?.data?.data ?? null

  const { data: entriesData, isFetching: entriesLoading } = useListEntriesInfinite({ limit: 100 })
  const entries = entriesData?.pages.flatMap((p) => p.data.data ?? []) ?? []

  const formatOptions: RateFormat[] = ['/day', '/mo', '/yr']

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        overflow: 'hidden',
      }}
    >
      {/* Topbar */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '14px 20px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <h1
          style={{
            margin: 0,
            fontSize: 18,
            fontWeight: 700,
            color: 'var(--text)',
            letterSpacing: '-0.02em',
          }}
        >
          Budget
        </h1>

        {/* Rate format toggle */}
        <div
          style={{
            display: 'flex',
            gap: 2,
            background: 'var(--surface)',
            borderRadius: 6,
            padding: 2,
            border: '1px solid var(--border)',
          }}
        >
          {formatOptions.map((f) => (
            <button
              key={f}
              onClick={() => setFormat(f)}
              style={{
                padding: '4px 10px',
                borderRadius: 4,
                border: 'none',
                cursor: 'pointer',
                fontSize: 12,
                fontWeight: format === f ? 600 : 400,
                background: format === f ? 'var(--surface2)' : 'transparent',
                color: format === f ? 'var(--text)' : 'var(--text2)',
                transition: 'all 0.1s',
              }}
            >
              {f}
            </button>
          ))}
        </div>
      </div>

      {/* Scrollable canvas */}
      <div style={{ flex: 1, overflow: 'auto', display: 'flex', flexDirection: 'column' }}>
        <SummaryStrip summary={summary} loading={summaryLoading} />
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 260 }}>
          <HorizonGraph nodeId={horizonNodeId} />
        </div>
        <StackPanel
          entries={entries}
          loading={entriesLoading}
        />
      </div>
    </div>
  )
}
