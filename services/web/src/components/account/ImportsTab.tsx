import { useState, useEffect } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
// TODO(task-6-11): getImports/getImportTransactions will be replaced with generated hooks

// Interim local types until account components are rebuilt in tasks 6-11
interface ImportBatch {
  id: string
  account_id: string
  processed_at: string
  transactions_imported: number
  transactions_skipped_duplicate: number
  source_name: string
}

interface ImportTransaction {
  id: string
  account_id: string
  import_batch_id: string
  imported_payee: string
  merchant_normalized: string
  amount_cents: number
  date: string
  is_duplicate: boolean
  created_at: string
}

async function _apiFetch<T>(path: string, params?: Record<string, string>): Promise<T> {
  const token = localStorage.getItem('token')
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const qs = params ? '?' + new URLSearchParams(params).toString() : ''
  const res = await fetch(`${base}${path}${qs}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  return res.json() as Promise<T>
}

async function getImports(params: { account_id?: string; after?: string; limit?: number }): Promise<{ data: ImportBatch[] }> {
  const p: Record<string, string> = {}
  if (params.account_id) p.account_id = params.account_id
  if (params.after) p.after = params.after
  if (params.limit) p.limit = String(params.limit)
  return _apiFetch<{ data: ImportBatch[] }>('/imports', p)
}

async function getImportTransactions(params: { account_id?: string; import_batch_id?: string; after?: string; limit?: number }): Promise<{ data: ImportTransaction[] }> {
  const p: Record<string, string> = {}
  if (params.account_id) p.account_id = params.account_id
  if (params.import_batch_id) p.import_batch_id = params.import_batch_id
  if (params.after) p.after = params.after
  if (params.limit) p.limit = String(params.limit)
  return _apiFetch<{ data: ImportTransaction[] }>('/transactions', p)
}

interface ImportsTabProps {
  accountId: string
}

function formatDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

function formatFullDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' })
}

function formatAmount(cents: number): string {
  return (Math.abs(cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

export function ImportsTab({ accountId }: ImportsTabProps) {
  const [batches, setBatches] = useState<ImportBatch[]>([])
  const [batchTransactions, setBatchTransactions] = useState<
    Map<string, ImportTransaction[]>
  >(new Map())
  const [expandedBatches, setExpandedBatches] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(true)
  const [showDuplicates, setShowDuplicates] = useState(true)
  const [searchTerm, setSearchTerm] = useState('')
  const [selectedBatch, setSelectedBatch] = useState<string | 'all'>('all')

  useEffect(() => {
    setLoading(true)
    getImports({ account_id: accountId, limit: 50 })
      .then(({ data }) => {
        setBatches(data)
        // Expand most recent 3 by default
        const toExpand = new Set(data.slice(0, 3).map((b) => b.id))
        setExpandedBatches(toExpand)
        // Load transactions for recent batches
        data.slice(0, 3).forEach((batch) => {
          loadBatchTransactions(batch.id)
        })
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [accountId])

  function loadBatchTransactions(batchId: string) {
    if (batchTransactions.has(batchId)) return
    getImportTransactions({ account_id: accountId, import_batch_id: batchId, limit: 100 })
      .then(({ data }) => {
        setBatchTransactions((prev) => new Map(prev).set(batchId, data))
      })
      .catch(() => {})
  }

  function toggleBatch(batchId: string) {
    setExpandedBatches((prev) => {
      const next = new Set(prev)
      if (next.has(batchId)) {
        next.delete(batchId)
      } else {
        next.add(batchId)
        loadBatchTransactions(batchId)
      }
      return next
    })
  }

  const displayBatches = selectedBatch === 'all' ? batches : batches.filter((b) => b.id === selectedBatch)

  if (loading) {
    return (
      <div style={{ padding: 20 }}>
        <div style={{ height: 40, background: 'var(--surface)', borderRadius: 6, opacity: 0.4 }} />
      </div>
    )
  }

  if (batches.length === 0) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <p style={{ color: 'var(--text2)', marginBottom: 8 }}>No imports for this account.</p>
        <p style={{ color: 'var(--text3)', fontSize: 13 }}>
          Use the import button in the sidebar to upload a CSV.
        </p>
      </div>
    )
  }

  return (
    <div>
      {/* Filter bar */}
      <div
        style={{
          display: 'flex',
          gap: 8,
          padding: '10px 16px',
          borderBottom: '1px solid var(--border)',
          alignItems: 'center',
          flexWrap: 'wrap',
        }}
      >
        <select
          value={selectedBatch}
          onChange={(e) => setSelectedBatch(e.target.value)}
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text2)',
            fontSize: 12,
            cursor: 'pointer',
          }}
        >
          <option value="all">All batches</option>
          {batches.map((b) => (
            <option key={b.id} value={b.id}>
              {formatFullDate(b.processed_at)}
            </option>
          ))}
        </select>

        <button
          onClick={() => setShowDuplicates(!showDuplicates)}
          style={{
            background: showDuplicates ? 'var(--surface2)' : 'var(--bg)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 10px',
            color: showDuplicates ? 'var(--text2)' : 'var(--text3)',
            fontSize: 12,
            cursor: 'pointer',
          }}
        >
          Show duplicates {showDuplicates ? '●' : '○'}
        </button>

        <input
          type="text"
          placeholder="Search merchant..."
          value={searchTerm}
          onChange={(e) => setSearchTerm(e.target.value)}
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text)',
            fontSize: 12,
            outline: 'none',
            flex: 1,
            minWidth: 120,
            maxWidth: 240,
          }}
        />
      </div>

      {/* Batch groups */}
      {displayBatches.map((batch) => {
        const isExpanded = expandedBatches.has(batch.id)
        const txs = batchTransactions.get(batch.id) ?? []
        const filteredTxs = txs.filter((tx) => {
          if (!showDuplicates && tx.is_duplicate) return false
          if (searchTerm) {
            const q = searchTerm.toLowerCase()
            return (
              tx.imported_payee.toLowerCase().includes(q) ||
              tx.merchant_normalized.toLowerCase().includes(q)
            )
          }
          return true
        })

        return (
          <div key={batch.id} style={{ borderBottom: '1px solid var(--border)' }}>
            {/* Batch header */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                padding: '10px 16px',
                cursor: 'pointer',
                background: 'var(--surface)',
                userSelect: 'none',
              }}
              onClick={() => toggleBatch(batch.id)}
            >
              <span style={{ color: 'var(--text3)', flexShrink: 0 }}>
                {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              </span>
              <span style={{ fontWeight: 600, fontSize: 13, color: 'var(--text)' }}>
                Imported {formatFullDate(batch.processed_at)}
              </span>
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                {batch.source_name} CSV
              </span>
              <div style={{ flex: 1 }} />
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                {batch.transactions_imported} rows
              </span>
              {batch.transactions_skipped_duplicate > 0 && (
                <span style={{ fontSize: 12, color: 'var(--text3)' }}>
                  · {batch.transactions_skipped_duplicate} duplicates skipped
                </span>
              )}
            </div>

            {/* Raw transaction rows */}
            {isExpanded && (
              <div>
                {/* Column headers */}
                <div
                  style={{
                    display: 'flex',
                    gap: 8,
                    padding: '5px 16px 5px 28px',
                    background: 'var(--bg)',
                    borderTop: '1px solid var(--border)',
                  }}
                >
                  <span style={{ width: 88, fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>Date</span>
                  <span style={{ flex: 1, fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>Raw merchant</span>
                  <span style={{ flex: 1, fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>Normalized</span>
                  <span style={{ width: 88, fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em', textAlign: 'right' }}>Amount</span>
                  <span style={{ width: 64, fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em', textAlign: 'center' }}>Status</span>
                </div>

                {filteredTxs.map((tx) => (
                  <div
                    key={tx.id}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 8,
                      padding: '6px 16px 6px 28px',
                      borderTop: '1px solid var(--border)',
                      background: 'var(--surface)',
                    }}
                  >
                    <span style={{ width: 88, fontSize: 12, color: 'var(--text2)', flexShrink: 0 }}>
                      {formatDate(tx.date)}
                    </span>
                    <span
                      style={{
                        flex: 1,
                        fontSize: 13,
                        color: tx.is_duplicate ? 'var(--text3)' : 'var(--text)',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        textDecoration: tx.is_duplicate ? 'line-through' : 'none',
                      }}
                    >
                      {tx.imported_payee}
                    </span>
                    <span
                      style={{
                        flex: 1,
                        fontSize: 12,
                        color: 'var(--text3)',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {tx.merchant_normalized}
                    </span>
                    <span
                      style={{
                        width: 88,
                        fontSize: 12,
                        color: tx.is_duplicate ? 'var(--text3)' : 'var(--text)',
                        textAlign: 'right',
                        flexShrink: 0,
                      }}
                    >
                      {formatAmount(tx.amount_cents)}
                    </span>
                    <span style={{ width: 64, textAlign: 'center', flexShrink: 0 }}>
                      {tx.is_duplicate && (
                        <span
                          style={{
                            fontSize: 10,
                            padding: '1px 5px',
                            borderRadius: 3,
                            background: 'var(--surface2)',
                            color: 'var(--text3)',
                          }}
                        >
                          duplicate
                        </span>
                      )}
                    </span>
                  </div>
                ))}

                {filteredTxs.length === 0 && (
                  <div style={{ padding: '12px 28px', color: 'var(--text3)', fontSize: 13 }}>
                    No transactions match current filters.
                  </div>
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
