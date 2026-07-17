import { useState, useRef, useEffect, useMemo } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { format, parseISO } from 'date-fns'
import { useApproveEntry, useRejectEntry, useListTransactions, useListLabels, useUpdateEntry, useListAccounts, useCreateLabel, useUpdateEntryConditions, useTriggerReprocess } from '../api/generated/velociAPI'
import type { EntryView } from '../api/generated/velociAPI.schemas'
import { useLedgerStore } from '../store/ledgerStore'
import type { StatusFilter } from '../store/ledgerStore'
import { useEntryStore } from '../store/entryStore'

// ── Helpers ──────────────────────────────────────────────────────────────────

const STATUS_ORDER: Record<string, number> = { pending_review: 0, active: 1, inactive: 2 }

function statusBadge(status: string) {
  const styles: Record<string, React.CSSProperties> = {
    pending_review: { background: 'var(--accent)', color: '#fff' },
    active:         { background: 'color-mix(in srgb, var(--income) 20%, transparent)', color: 'var(--income)' },
    inactive:       { background: 'var(--surface2)', color: 'var(--text3)' },
  }
  const labels: Record<string, string> = { pending_review: 'Review', active: 'Active', inactive: 'Inactive' }
  return (
    <span style={{
      fontSize: 10, fontWeight: 700, padding: '2px 6px', borderRadius: 10, whiteSpace: 'nowrap',
      ...styles[status],
    }}>
      {labels[status] ?? status}
    </span>
  )
}

function rateLabel(ratePerDay: number | null): string {
  if (ratePerDay === null || ratePerDay === undefined) return '—'
  return '$' + (Math.abs(ratePerDay) * 30 / 100).toFixed(0) + '/mo'
}

function alertBadge(alertType: string | null) {
  if (!alertType) return null
  const styles: Record<string, React.CSSProperties> = {
    new:   { background: 'color-mix(in srgb, var(--accent) 18%, transparent)', color: 'var(--accent)' },
    drift: { background: 'color-mix(in srgb, #f59e0b 18%, transparent)',       color: '#f59e0b' },
    ended: { background: 'color-mix(in srgb, var(--commit) 18%, transparent)', color: 'var(--commit)' },
  }
  return (
    <span style={{
      fontSize: 9, fontWeight: 700, padding: '1px 5px', borderRadius: 8, whiteSpace: 'nowrap', textTransform: 'uppercase', letterSpacing: '0.04em',
      ...styles[alertType] ?? {},
    }}>
      {alertType}
    </span>
  )
}

function confidencePct(v: number | null) {
  if (v === null || v === undefined) return null
  return Math.round(v * 100)
}

// ── Entry details panel (conditions JSON + confidence side-by-side) ───────────

function confColor(v: number | null) {
  const pct = confidencePct(v) ?? 0
  return pct >= 80 ? 'var(--income)' : pct >= 55 ? '#f59e0b' : 'var(--commit)'
}

function ConfidenceBar({ label, value }: { label: string; value: number | null }) {
  const pct = confidencePct(value)
  if (pct === null) return null
  const color = confColor(value)
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      <span style={{ fontSize: 11, color: 'var(--text3)', width: 60, flexShrink: 0 }}>{label}</span>
      <div style={{ flex: 1, height: 4, borderRadius: 2, background: 'var(--border)' }}>
        <div style={{ width: `${pct}%`, height: '100%', borderRadius: 2, background: color }} />
      </div>
      <span style={{ fontSize: 11, color, width: 30, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>{pct}%</span>
    </div>
  )
}

function EntryDetailsPanel({ entry }: { entry: EntryView }) {
  const [text, setText] = useState(() => {
    try { return JSON.stringify(entry.conditions, null, 2) } catch { return '' }
  })
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [confExpanded, setConfExpanded] = useState(true)
  const updateConditions = useUpdateEntryConditions()
  const upsert = useEntryStore((s) => s.upsert)

  function handleBlur() {
    try { JSON.parse(text) } catch {
      setJsonError('Invalid JSON — fix before saving')
      return
    }
    setJsonError(null)
    updateConditions.mutate(
      { id: entry.id, data: { conditions: JSON.parse(text) } },
      {
        onSuccess: (res) => { const u = res.data.data; if (u) upsert(u) },
        onError: () => setJsonError('Failed to save'),
      },
    )
  }

  const hasConf = entry.confidence !== null

  return (
    <div style={{ borderTop: '1px solid var(--border)', display: 'flex', background: 'var(--bg)', minHeight: 0 }}>
      {/* JSON editor — left, takes remaining space */}
      <div style={{ flex: 1, padding: '10px 20px', minWidth: 0 }}>
        <div style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: 4 }}>
          Matching Rule
        </div>
        <textarea
          value={text}
          onChange={(e) => { setText(e.target.value); setJsonError(null) }}
          onBlur={handleBlur}
          onClick={(e) => e.stopPropagation()}
          rows={5}
          spellCheck={false}
          style={{
            width: '100%', fontFamily: 'monospace', fontSize: 11,
            background: 'var(--surface)', color: 'var(--text)',
            border: `1px solid ${jsonError ? 'var(--commit)' : 'var(--border)'}`,
            borderRadius: 4, padding: '6px 8px', resize: 'vertical', boxSizing: 'border-box',
          }}
        />
        {jsonError && <span style={{ fontSize: 11, color: 'var(--commit)' }}>{jsonError}</span>}
        {!jsonError && updateConditions.isPending && <span style={{ fontSize: 11, color: 'var(--text3)' }}>Saving…</span>}
        {!jsonError && updateConditions.isSuccess && !updateConditions.isPending && (
          <span style={{ fontSize: 11, color: 'var(--income)' }}>Saved — status reset to pending review</span>
        )}
      </div>

      {/* Confidence — right, collapsible */}
      {hasConf && (
        <div style={{
          borderLeft: '1px solid var(--border)', padding: '10px 14px', flexShrink: 0,
          width: confExpanded ? 220 : 'auto', transition: 'width 0.15s',
        }}>
          {/* Header row: label + overall pct + toggle */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: confExpanded ? 8 : 0 }}>
            <span style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em', whiteSpace: 'nowrap' }}>
              Confidence
            </span>
            <span style={{ fontSize: 11, fontWeight: 600, color: confColor(entry.confidence), fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' }}>
              {confidencePct(entry.confidence)}%
            </span>
            <button
              onClick={(e) => { e.stopPropagation(); setConfExpanded(v => !v) }}
              style={{ marginLeft: 'auto', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0, display: 'flex', alignItems: 'center' }}
            >
              {confExpanded ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
            </button>
          </div>
          {confExpanded && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
              <ConfidenceBar label="Merchant" value={entry.merchant_confidence} />
              <ConfidenceBar label="Timing"   value={entry.timing_confidence} />
              <ConfidenceBar label="Amount"   value={entry.amount_confidence} />
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ── Entry transaction drill-down ──────────────────────────────────────────────

function EntryTransactions({ entryId }: { entryId: string }) {
  const { data, isLoading } = useListTransactions({ entry_id: entryId, limit: 200 })
  const { data: accountsData } = useListAccounts(undefined)
  const accountNames = useMemo(() => {
    const m: Record<string, string> = {}
    for (const a of accountsData?.data?.data ?? []) m[a.id] = a.name
    return m
  }, [accountsData])
  const rows = useMemo(
    () => [...(data?.data?.data ?? [])].sort((a, b) => (a.date < b.date ? 1 : a.date > b.date ? -1 : 0)),
    [data],
  )

  if (isLoading) {
    return (
      <div style={{ padding: '12px 20px', color: 'var(--text3)', fontSize: 12 }}>
        Loading…
      </div>
    )
  }

  if (rows.length === 0) {
    return (
      <div style={{ padding: '12px 20px', color: 'var(--text3)', fontSize: 12 }}>
        No transactions matched to this entry yet.
      </div>
    )
  }
  return (
    <div style={{ borderTop: '1px solid var(--border)', background: 'var(--bg)' }}>
      <div style={{
        display: 'grid',
        gridTemplateColumns: '90px 1fr 1fr 90px 80px',
        padding: '4px 20px',
        gap: 8,
        fontSize: 10,
        color: 'var(--text3)',
        textTransform: 'uppercase',
        letterSpacing: '0.04em',
      }}>
        <span>Date</span><span>Merchant</span><span>Account</span><span>Amount</span><span>Status</span>
      </div>
      {rows.map((tx) => (
        <div key={tx.id} style={{
          display: 'grid',
          gridTemplateColumns: '90px 1fr 1fr 90px 80px',
          padding: '5px 20px',
          gap: 8,
          fontSize: 12,
          color: 'var(--text2)',
          borderTop: '1px solid var(--border)',
        }}>
          <span>{format(parseISO(tx.date), 'MMM d, yy')}</span>
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {tx.merchant_normalized || tx.imported_payee}
          </span>
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--text3)' }}>
            {accountNames[tx.account_id] ?? tx.account_id}
          </span>
          <span style={{ color: tx.amount_cents < 0 ? 'var(--commit)' : 'var(--income)' }}>
            {(Math.abs(tx.amount_cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })}
          </span>
          <span>{tx.settlement_status}</span>
        </div>
      ))}
    </div>
  )
}

// ── Review edit panel ─────────────────────────────────────────────────────────

function EntryReviewPanel({ entry, onDone }: { entry: EntryView; onDone: () => void }) {
  const { data: labelsData } = useListLabels({})
  const labels = labelsData?.data?.data ?? []

  const [labelName, setLabelName] = useState(entry.label_name ?? '')
  const [resolvedLabelId, setResolvedLabelId] = useState<string | null>(entry.label_id ?? null)
  const [direction, setDirection] = useState(entry.direction)
  const [entryType, setEntryType] = useState(entry.entry_type)
  const [saveError, setSaveError] = useState<string | null>(null)

  const approve = useApproveEntry()
  const reject = useRejectEntry()
  const update = useUpdateEntry()
  const createLabel = useCreateLabel()

  function save(overrides: { labelId?: string | null; direction?: string; entryType?: string } = {}) {
    setSaveError(null)
    update.mutate({
      id: entry.id,
      data: {
        label_id: 'labelId' in overrides ? (overrides.labelId ?? null) : resolvedLabelId,
        direction: overrides.direction ?? direction,
        entry_type: overrides.entryType ?? entryType,
        period_days: parseInt(entry.period ?? '30', 10) || 30,
        status: 'pending_review',
        start_date: entry.start_date,
        end_date: entry.end_date ?? null,
        conditions: entry.conditions ?? null,
        priority: entry.priority ?? 0,
        project_tentatively: false,
        projected_rate_per_day: entry.projected_rate ?? null,
        variable_method: null,
      },
    }, {
      onError: (err: unknown) => {
        const detail = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail
        setSaveError(detail ?? 'Failed to save — please try again')
      },
    })
  }

  function handleLabelBlur() {
    const trimmed = labelName.trim()
    if (!trimmed) {
      setResolvedLabelId(null)
      save({ labelId: null })
      return
    }
    const existing = labels.find((l) => l.name.toLowerCase() === trimmed.toLowerCase())
    if (existing) {
      setResolvedLabelId(existing.id)
      save({ labelId: existing.id })
    } else {
      setSaveError(null)
      createLabel.mutate({ data: { name: trimmed } }, {
        onSuccess: (res) => {
          const id = res.data.data?.id ?? null
          setResolvedLabelId(id)
          save({ labelId: id })
        },
        onError: (err: unknown) => {
          const status = (err as { response?: { status?: number } })?.response?.status
          const detail = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail
          setSaveError(
            status === 409
              ? `Label "${trimmed}" already exists — try a different name`
              : (detail ?? 'Could not create label')
          )
        },
      })
    }
  }

  function handleDirectionChange(val: string) {
    setSaveError(null)
    setDirection(val)
    save({ direction: val })
  }

  function handleEntryTypeChange(val: string) {
    setSaveError(null)
    setEntryType(val)
    save({ entryType: val })
  }

  const isBusy = approve.isPending || reject.isPending || update.isPending || createLabel.isPending

  const fieldStyle: React.CSSProperties = {
    fontSize: 12, padding: '3px 6px', borderRadius: 4,
    border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text)',
  }

  return (
    <div style={{
      borderTop: '1px solid var(--border)', background: 'var(--surface2)',
      padding: '10px 20px', display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap',
    }}>
      {/* Label */}
      <label style={{ fontSize: 11, color: 'var(--text3)', display: 'flex', flexDirection: 'column', gap: 3 }}>
        Label
        <input
          type="text"
          value={labelName}
          onChange={(e) => setLabelName(e.target.value)}
          onBlur={handleLabelBlur}
          onClick={(e) => e.stopPropagation()}
          placeholder="e.g. Groceries"
          style={{ ...fieldStyle, width: 160 }}
        />
      </label>

      {/* Direction */}
      <label style={{ fontSize: 11, color: 'var(--text3)', display: 'flex', flexDirection: 'column', gap: 3 }}>
        Direction
        <select
          value={direction}
          onChange={(e) => handleDirectionChange(e.target.value)}
          style={fieldStyle}
          onClick={(e) => e.stopPropagation()}
        >
          <option value="expense">Expense</option>
          <option value="income">Income</option>
        </select>
      </label>

      {/* Entry type */}
      <label style={{ fontSize: 11, color: 'var(--text3)', display: 'flex', flexDirection: 'column', gap: 3 }}>
        Type
        <select
          value={entryType}
          onChange={(e) => handleEntryTypeChange(e.target.value)}
          style={fieldStyle}
          onClick={(e) => e.stopPropagation()}
        >
          <option value="standing">Standing</option>
          <option value="variable">Variable</option>
          <option value="irregular">Irregular</option>
        </select>
      </label>

      <div style={{ marginLeft: 'auto', display: 'flex', gap: 8, alignItems: 'center' }}>
        {saveError && (
          <span style={{ fontSize: 11, color: 'var(--commit)', maxWidth: 220 }}>{saveError}</span>
        )}
        {!saveError && (update.isPending || createLabel.isPending) && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>Saving…</span>
        )}
        <button
          onClick={(e) => { e.stopPropagation(); reject.mutate({ id: entry.id }, { onSuccess: onDone }) }}
          disabled={isBusy}
          style={actionBtn('var(--commit)')}
        >
          {reject.isPending ? '…' : 'Reject'}
        </button>
        <button
          onClick={(e) => { e.stopPropagation(); approve.mutate({ id: entry.id }, { onSuccess: onDone }) }}
          disabled={isBusy}
          style={actionBtn('var(--income)')}
        >
          {approve.isPending ? '…' : 'Approve'}
        </button>
      </div>
    </div>
  )
}


// ── Row actions (header-level, non-review) ────────────────────────────────────

function EntryActions({ entry }: { entry: EntryView }) {
  if (entry.status === 'active') {
    return <span style={{ fontSize: 11, color: 'var(--text3)' }}>Edit · End</span>
  }
  return null
}

function actionBtn(color: string): React.CSSProperties {
  return {
    fontSize: 11, fontWeight: 600, padding: '3px 10px', borderRadius: 4,
    border: `1px solid ${color}`, background: 'none', color, cursor: 'pointer',
  }
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function LedgerPage() {
  const parentRef = useRef<HTMLDivElement>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const reprocess = useTriggerReprocess()

  const statusFilter = useLedgerStore((s) => s.statusFilter)
  const scrollOffset = useLedgerStore((s) => s.scrollOffset)
  const setStatusFilter = useLedgerStore((s) => s.setStatusFilter)
  const setScrollOffset = useLedgerStore((s) => s.setScrollOffset)

  const entryMap = useEntryStore((s) => s.entries)
  const entryStatus = useEntryStore((s) => s.status)
  const entryNextCursor = useEntryStore((s) => s.nextCursor)
  const loadMoreEntries = useEntryStore((s) => s.loadMore)

  const allEntries: EntryView[] = useMemo(() => Object.values(entryMap), [entryMap])

  const filtered = allEntries
    .filter((e) => statusFilter === 'all' || e.status === statusFilter)
    .sort((a, b) => (STATUS_ORDER[a.status] ?? 9) - (STATUS_ORDER[b.status] ?? 9))

  const counts = {
    pending_review: allEntries.filter((e) => e.status === 'pending_review').length,
    active:         allEntries.filter((e) => e.status === 'active').length,
    inactive:       allEntries.filter((e) => e.status === 'inactive').length,
  }

  const virtualizer = useVirtualizer({
    count: filtered.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 44,
    overscan: 8,
    initialOffset: scrollOffset,
    measureElement: (el) => el.getBoundingClientRect().height,
  })

  function toggleExpand(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function invalidate() {
    void useEntryStore.getState().refresh()
  }

  useEffect(() => {
    const el = parentRef.current
    if (!el) return
    const save = () => setScrollOffset(el.scrollTop)
    el.addEventListener('scroll', save, { passive: true })
    return () => el.removeEventListener('scroll', save)
  }, [setScrollOffset])

  const lastIdx = virtualizer.getVirtualItems().at(-1)?.index
  useEffect(() => {
    if (lastIdx !== undefined && lastIdx >= filtered.length - 20 && entryNextCursor && entryStatus !== 'loading') {
      void loadMoreEntries()
    }
  }, [lastIdx, filtered.length, entryNextCursor, entryStatus, loadMoreEntries])

  const filterPills: Array<{ key: StatusFilter; label: string; color: string }> = [
    { key: 'all',           label: `All ${allEntries.length}`,                   color: 'var(--text2)' },
    { key: 'pending_review',label: `Review ${counts.pending_review}`,            color: 'var(--accent)' },
    { key: 'active',        label: `Active ${counts.active}`,                    color: 'var(--income)' },
    { key: 'inactive',      label: `Inactive ${counts.inactive}`,                color: 'var(--text3)' },
  ]

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '14px 20px', borderBottom: '1px solid var(--border)', flexShrink: 0, flexWrap: 'wrap',
      }}>
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em', marginRight: 4 }}>
          Ledger
        </h1>
        {filterPills.map(({ key, label, color }) => (
          <button
            key={key}
            onClick={() => setStatusFilter(key)}
            style={{
              padding: '4px 10px', borderRadius: 12, fontSize: 12, fontWeight: 600, cursor: 'pointer',
              border: statusFilter === key ? 'none' : '1px solid var(--border)',
              background: statusFilter === key ? color : 'transparent',
              color: statusFilter === key ? '#fff' : 'var(--text2)',
              transition: 'all 0.1s',
            }}
          >
            {label}
          </button>
        ))}
        <button
          onClick={() => reprocess.mutate()}
          disabled={reprocess.isPending}
          style={{
            marginLeft: 'auto', padding: '4px 12px', borderRadius: 6, fontSize: 12, fontWeight: 600,
            border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text2)',
            cursor: reprocess.isPending ? 'default' : 'pointer', opacity: reprocess.isPending ? 0.6 : 1,
          }}
        >
          {reprocess.isPending ? 'Queued…' : 'Run Engine'}
        </button>
      </div>

      {/* Column headers */}
      <div style={{
        display: 'grid',
        gridTemplateColumns: '28px 1fr 80px 70px 90px 110px 140px',
        padding: '5px 20px', gap: 8,
        borderBottom: '1px solid var(--border)', background: 'var(--bg)', flexShrink: 0,
      }}>
        {['', 'Entry', 'Type', 'Dir', 'Rate/mo', 'Status', 'Conf'].map((h, i) => (
          <span key={i} style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
            {h}
          </span>
        ))}
      </div>

      {/* Virtual rows */}
      {entryStatus === 'loading' && allEntries.length === 0 ? (
        <div style={{ padding: 32, color: 'var(--text3)', textAlign: 'center' }}>Loading…</div>
      ) : filtered.length === 0 ? (
        <div style={{ padding: 32, color: 'var(--text3)', textAlign: 'center' }}>
          {statusFilter === 'all' ? 'No entries yet.' : `No ${statusFilter.replace('_', ' ')} entries.`}
        </div>
      ) : (
        <div ref={parentRef} style={{ flex: 1, overflowY: 'auto', minHeight: 0 }}>
          <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((vRow) => {
              const entry = filtered[vRow.index]
              if (!entry) return null
              const isExpanded = expanded.has(entry.id)
              const isInactive = entry.status === 'inactive'

              return (
                <div
                  key={entry.id}
                  data-index={vRow.index}
                  ref={virtualizer.measureElement}
                  style={{ position: 'absolute', top: vRow.start, left: 0, right: 0 }}
                >
                  {/* Entry row */}
                  <div
                    onClick={() => toggleExpand(entry.id)}
                    style={{
                      display: 'grid',
                      gridTemplateColumns: '28px 1fr 80px 70px 90px 110px 140px',
                      alignItems: 'center',
                      padding: '0 20px', height: 44, gap: 8,
                      cursor: 'pointer',
                      borderBottom: isExpanded ? 'none' : '1px solid var(--border)',
                      background: isExpanded ? 'var(--surface2)' : 'var(--surface)',
                      opacity: isInactive ? 0.55 : 1,
                    }}
                  >
                    <span style={{ color: 'var(--text3)', display: 'flex', alignItems: 'center' }}>
                      {isExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                    </span>
                    <span style={{
                      fontSize: 13, fontWeight: 500, color: 'var(--text)',
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }}>
                      {entry.name}
                    </span>
                    <span style={{ fontSize: 12, color: 'var(--text2)' }}>{entry.entry_type}</span>
                    <span style={{
                      fontSize: 12,
                      color: entry.direction === 'income' ? 'var(--income)' : 'var(--commit)',
                    }}>
                      {entry.direction}
                    </span>
                    <span style={{ fontSize: 12, color: 'var(--text)', fontVariantNumeric: 'tabular-nums' }}>
                      {rateLabel(entry.actual_rate)}
                    </span>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                      {statusBadge(entry.status)}
                      {alertBadge(entry.alert_type)}
                    </span>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 6 }} onClick={(e) => e.stopPropagation()}>
                      {entry.confidence !== null && (
                        <span style={{
                          fontSize: 11, fontVariantNumeric: 'tabular-nums',
                          color: (confidencePct(entry.confidence) ?? 0) >= 80 ? 'var(--income)' : (confidencePct(entry.confidence) ?? 0) >= 55 ? '#f59e0b' : 'var(--commit)',
                        }}>
                          {confidencePct(entry.confidence)}%
                        </span>
                      )}
                      <EntryActions entry={entry} />
                    </span>
                  </div>

                  {/* Expanded drill-down */}
                  {isExpanded && (
                    <>
                      {entry.status === 'pending_review' && (
                        <EntryReviewPanel entry={entry} onDone={invalidate} />
                      )}
                      <EntryDetailsPanel entry={entry} />
                      <EntryTransactions entryId={entry.id} />
                    </>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {entryStatus === 'loading' && allEntries.length > 0 && (
        <div style={{ padding: '8px 20px', color: 'var(--text3)', fontSize: 12, flexShrink: 0 }}>
          Loading more…
        </div>
      )}
    </div>
  )
}
