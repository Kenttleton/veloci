import { useEffect, useState } from 'react'
import { useRateFormat } from '../contexts/RateFormatContext'
import type { RateFormat } from '../contexts/RateFormatContext'
import { SummaryStrip } from '../components/budget/SummaryStrip'
import { HorizonGraph } from '../components/budget/HorizonGraph'
import { StackPanel } from '../components/budget/StackPanel'
// TODO(task-6-11): getSnapshotSummary/getEntries will be replaced with generated hooks
import type { EntryView } from '../api/generated/velociAPI.schemas'

// Extend EntryView with period/actual that SummaryStrip needs
interface SnapshotSummary {
  income_rate: number
  commitments_rate: number
  margin_rate: number
  projection_rate: number
  drift_rate: number
  period: string
  actual: boolean
}

type Entry = EntryView

async function _apiFetch<T>(path: string): Promise<T> {
  const token = localStorage.getItem('token')
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const res = await fetch(`${base}${path}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  return res.json() as Promise<T>
}

async function getSnapshotSummary(): Promise<SnapshotSummary> {
  const result = await _apiFetch<{ data: SnapshotSummary }>('/snapshots/summary')
  return result.data
}

async function getEntries(): Promise<Entry[]> {
  const result = await _apiFetch<{ data: Entry[] }>('/entries')
  return result.data ?? []
}

export function BudgetPage() {
  const { format, setFormat } = useRateFormat()
  const [summary, setSummary] = useState<SnapshotSummary | null>(null)
  const [entries, setEntries] = useState<Entry[]>([])
  const [summaryLoading, setSummaryLoading] = useState(true)
  const [entriesLoading, setEntriesLoading] = useState(true)
  // Use a budget node id (entity-level snapshot) for the horizon
  const [horizonNodeId] = useState<string | null>(null)

  useEffect(() => {
    setSummaryLoading(true)
    getSnapshotSummary()
      .then(setSummary)
      .catch(() => {})
      .finally(() => setSummaryLoading(false))
  }, [])

  useEffect(() => {
    setEntriesLoading(true)
    getEntries()
      .then(setEntries)
      .catch(() => {})
      .finally(() => setEntriesLoading(false))
  }, [])

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
          pulsePeriod={summary?.period}
          isActual={summary?.actual}
        />
      </div>
    </div>
  )
}
