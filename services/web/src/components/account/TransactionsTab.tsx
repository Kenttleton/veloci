import { useState, useEffect } from 'react'
import { ChevronDown, ChevronRight, ExternalLink } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { LabelPill } from '../shared/LabelPill'
import { getTransactions } from '../../api/resources'
import type { Transaction } from '../../api/resources'

interface TransactionsTabProps {
  accountId: string
}

interface EntryGroup {
  entryId: string | null
  entryName: string | null
  labelId: string | null
  labelName: string | null
  isPendingReview: boolean
  transactions: Transaction[]
  cursor: string | null
  hasMore: boolean
  loading: boolean
}

function formatAmount(cents: number, direction?: 'income' | 'expense'): { text: string; color: string } {
  const dollars = Math.abs(cents) / 100
  const text = dollars.toLocaleString('en-US', { style: 'currency', currency: 'USD' })
  const color = cents > 0 || direction === 'income' ? 'var(--income)' : 'var(--commit)'
  return { text, color }
}

function formatDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

export function TransactionsTab({ accountId }: TransactionsTabProps) {
  const navigate = useNavigate()
  const [groups, setGroups] = useState<EntryGroup[]>([])
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())
  const [expandedRows, setExpandedRows] = useState<Map<string, string>>(new Map()) // groupKey -> txId
  const [loading, setLoading] = useState(true)
  const [labelFilter] = useState<string | null>(null)

  useEffect(() => {
    setLoading(true)
    getTransactions({ account_id: accountId, limit: 50 })
      .then(({ data }) => {
        const map = new Map<string, EntryGroup>()
        for (const tx of data) {
          const key = tx.entry_id ?? '__unmatched__'
          if (!map.has(key)) {
            map.set(key, {
              entryId: tx.entry_id,
              entryName: tx.entry_name,
              labelId: tx.label_id,
              labelName: null,
              isPendingReview: tx.pending_review,
              transactions: [],
              cursor: null,
              hasMore: false,
              loading: false,
            })
          }
          map.get(key)!.transactions.push(tx)
        }
        setGroups(Array.from(map.values()))
        // Expand first group by default
        if (map.size > 0) {
          setExpandedGroups(new Set([map.keys().next().value ?? '']))
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [accountId])

  function toggleGroup(key: string) {
    setExpandedGroups((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  function toggleRow(groupKey: string, txId: string) {
    setExpandedRows((prev) => {
      const next = new Map(prev)
      if (next.get(groupKey) === txId) {
        next.delete(groupKey)
      } else {
        next.set(groupKey, txId)
      }
      return next
    })
  }

  function loadMoreForGroup(groupKey: string) {
    const group = groups.find((g) => (g.entryId ?? '__unmatched__') === groupKey)
    if (!group || !group.hasMore || group.loading) return

    setGroups((prev) =>
      prev.map((g) =>
        (g.entryId ?? '__unmatched__') === groupKey ? { ...g, loading: true } : g,
      ),
    )

    getTransactions({
      account_id: accountId,
      entry_id: group.entryId ?? undefined,
      after: group.cursor ?? undefined,
      limit: 50,
    })
      .then(({ data, meta }) => {
        setGroups((prev) =>
          prev.map((g) => {
            if ((g.entryId ?? '__unmatched__') !== groupKey) return g
            return {
              ...g,
              transactions: [...g.transactions, ...data],
              cursor: meta.next_cursor ?? null,
              hasMore: meta.has_more ?? false,
              loading: false,
            }
          }),
        )
      })
      .catch(() => {
        setGroups((prev) =>
          prev.map((g) =>
            (g.entryId ?? '__unmatched__') === groupKey ? { ...g, loading: false } : g,
          ),
        )
      })
  }

  const filteredGroups = labelFilter
    ? groups.filter((g) => g.labelId === labelFilter)
    : groups

  if (loading) {
    return (
      <div style={{ padding: 20 }}>
        <div style={{ height: 40, background: 'var(--surface)', borderRadius: 6, opacity: 0.4 }} />
      </div>
    )
  }

  if (groups.length === 0) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <p style={{ color: 'var(--text2)', marginBottom: 8 }}>No matched transactions yet.</p>
        <p style={{ color: 'var(--text3)', fontSize: 13, marginBottom: 16 }}>
          Transactions appear here once the engine has processed this account's imports
          and patterns have been matched to entries.
        </p>
        <button
          onClick={() => navigate('/review')}
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            color: 'var(--accent)',
            fontSize: 13,
          }}
        >
          Go to Review →
        </button>
      </div>
    )
  }

  return (
    <div>
      {filteredGroups.map((group) => {
        const groupKey = group.entryId ?? '__unmatched__'
        const isExpanded = expandedGroups.has(groupKey)
        const expandedTxId = expandedRows.get(groupKey)
        const totalCents = group.transactions.reduce((s, t) => s + t.amount_cents, 0)
        const { text: totalText, color: totalColor } = formatAmount(totalCents)

        return (
          <div
            key={groupKey}
            style={{
              borderBottom: '1px solid var(--border)',
              borderLeft: group.isPendingReview ? '3px solid var(--accent)' : 'none',
            }}
          >
            {/* Group header */}
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
              onClick={() => toggleGroup(groupKey)}
            >
              <span style={{ color: 'var(--text3)', flexShrink: 0 }}>
                {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              </span>
              <span
                style={{
                  fontWeight: 600,
                  fontSize: 13,
                  color: group.isPendingReview ? 'var(--text2)' : 'var(--text)',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {group.entryName ?? 'Unmatched'}
              </span>
              {group.isPendingReview && (
                <span
                  style={{
                    fontSize: 10,
                    padding: '1px 6px',
                    borderRadius: 3,
                    background: 'var(--surface2)',
                    color: 'var(--text3)',
                    flexShrink: 0,
                  }}
                >
                  pending
                </span>
              )}
              {!group.isPendingReview && group.labelId && (
                <LabelPill
                  name={group.labelName}
                  className="cursor-pointer"
                />
              )}
              <div style={{ flex: 1 }} />
              <span style={{ fontSize: 12, color: 'var(--text2)', flexShrink: 0 }}>
                {group.transactions.length} transactions
              </span>
              <span
                style={{
                  fontSize: 13,
                  color: totalColor,
                  fontWeight: 500,
                  flexShrink: 0,
                  marginLeft: 8,
                }}
              >
                {totalText}
              </span>
            </div>

            {/* Transaction rows */}
            {isExpanded && (
              <div>
                {group.transactions.map((tx) => {
                  const isExpRow = expandedTxId === tx.id
                  const { text: amtText, color: amtColor } = formatAmount(tx.amount_cents)

                  return (
                    <div key={tx.id}>
                      <div
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          gap: 8,
                          padding: '7px 16px 7px 28px',
                          borderTop: '1px solid var(--border)',
                          background: isExpRow ? 'var(--surface2)' : 'var(--surface)',
                          cursor: 'pointer',
                          transition: 'background 0.1s',
                        }}
                        onClick={() => toggleRow(groupKey, tx.id)}
                      >
                        <span style={{ width: 88, fontSize: 12, color: 'var(--text2)', flexShrink: 0 }}>
                          {formatDate(tx.date)}
                        </span>
                        <span style={{ flex: 1, fontSize: 13, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {tx.merchant_normalized}
                        </span>
                        <span style={{ fontSize: 12, color: amtColor, textAlign: 'right', flexShrink: 0 }}>
                          {amtText}
                        </span>
                      </div>

                      {/* Inline detail panel */}
                      {isExpRow && (
                        <div
                          style={{
                            padding: '12px 28px',
                            background: 'var(--bg)',
                            borderTop: '1px solid var(--border)',
                            borderBottom: '1px solid var(--border)',
                          }}
                        >
                          <div style={{ marginBottom: 10 }}>
                            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
                              Entries matched
                            </div>
                            {group.entryId ? (
                              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                                <button
                                  style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent)', fontSize: 13, padding: 0, display: 'flex', alignItems: 'center', gap: 4 }}
                                >
                                  {group.entryName}
                                  <ExternalLink size={11} />
                                </button>
                                {tx.confidence !== null && (
                                  <span style={{ color: 'var(--text3)', fontSize: 11 }}>{tx.confidence.toFixed(2)}</span>
                                )}
                              </div>
                            ) : (
                              <span style={{ color: 'var(--text3)', fontSize: 13 }}>No entries matched</span>
                            )}
                          </div>

                          <div style={{ marginBottom: 10 }}>
                            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
                              Raw import
                            </div>
                            <div style={{ fontSize: 12, color: 'var(--text2)' }}>
                              {tx.imported_payee} · {amtText} · batch {tx.import_batch_id.slice(0, 8)}
                            </div>
                          </div>

                          <button
                            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent)', fontSize: 12, padding: 0 }}
                          >
                            View in Imports tab
                          </button>
                        </div>
                      )}
                    </div>
                  )
                })}

                {group.hasMore && (
                  <div style={{ padding: '8px 28px', borderTop: '1px solid var(--border)' }}>
                    <button
                      onClick={() => loadMoreForGroup(groupKey)}
                      disabled={group.loading}
                      style={{
                        background: 'none',
                        border: '1px solid var(--border)',
                        borderRadius: 4,
                        padding: '4px 12px',
                        cursor: group.loading ? 'default' : 'pointer',
                        color: 'var(--text2)',
                        fontSize: 12,
                        opacity: group.loading ? 0.5 : 1,
                      }}
                    >
                      {group.loading ? 'Loading...' : 'Load more'}
                    </button>
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
