import { useState, useCallback, useEffect } from 'react'
import { NewCard } from '../components/review/NewCard'
import { DriftCard } from '../components/review/DriftCard'
import { EndedCard } from '../components/review/EndedCard'
// TODO(task-6-11): getReview will be replaced with generated hook

// Interim local type until review components are rebuilt in tasks 6-11
interface ReviewItem {
  id: string
  entry_id: string
  suggested_name: string
  alert_type: 'new' | 'drift' | 'ended'
  status: 'pending' | 'approved' | 'rejected'
  confidence: number | null
  merchant_confidence: number | null
  timing_confidence: number | null
  amount_confidence: number | null
  suggested_entry_type: string | null
  suggested_rate_per_day: number | null
  recurrence_anchor: string | null
  sample_merchants: Array<{ date: string; payee: string; amount_cents: number }>
  transaction_count: number
  old_rate_per_day?: number
  new_rate_per_day?: number
  old_timing?: string
  new_timing?: string
  transaction_evidence?: Array<{ date: string; payee: string; amount_cents: number }>
  has_manual_projection?: boolean
  manual_projection_per_day?: number
  last_seen_date?: string
  next_due_date?: string
  days_overdue?: number
  current_rate_per_day?: number
  created_at: string
}

async function getReview(params: { after?: string; limit?: number }): Promise<{ data: ReviewItem[]; meta: { next_cursor?: string; has_more?: boolean } }> {
  const token = localStorage.getItem('token')
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const p: Record<string, string> = {}
  if (params.after) p.after = params.after
  if (params.limit) p.limit = String(params.limit)
  const qs = Object.keys(p).length ? '?' + new URLSearchParams(p).toString() : ''
  const res = await fetch(`${base}/review${qs}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  return res.json() as Promise<{ data: ReviewItem[]; meta: { next_cursor?: string; has_more?: boolean } }>
}

type AlertType = 'new' | 'drift' | 'ended'

export function ReviewPage() {
  const [items, setItems] = useState<ReviewItem[]>([])
  const [loading, setLoading] = useState(true)
  const [activeFilters, setActiveFilters] = useState<Set<AlertType>>(
    new Set(['new', 'drift', 'ended']),
  )
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)

  const loadItems = useCallback(async (after?: string, reset = false) => {
    if (reset) setLoading(true)
    else setLoadingMore(true)
    try {
      const result = await getReview({ after, limit: 50 })
      const incoming = result.data
      if (reset) {
        setItems(incoming)
      } else {
        setItems((prev) => [...prev, ...incoming])
      }
      setCursor(result.meta.next_cursor)
      setHasMore(result.meta.has_more ?? false)
    } catch {
      //
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }, [])

  useEffect(() => {
    void loadItems(undefined, true)
  }, [loadItems])

  function handleAction() {
    // Reload after any card action
    void loadItems(undefined, true)
  }

  function toggleFilter(type: AlertType) {
    setActiveFilters((prev) => {
      const next = new Set(prev)
      if (next.has(type)) next.delete(type)
      else next.add(type)
      return next
    })
  }

  // Sort: new → drift → ended, within each group by confidence desc
  const sortedItems = [...items].sort((a, b) => {
    const order: Record<AlertType, number> = { new: 0, drift: 1, ended: 2 }
    const aOrder = order[a.alert_type]
    const bOrder = order[b.alert_type]
    if (aOrder !== bOrder) return aOrder - bOrder
    return b.confidence - a.confidence
  })

  const filteredItems = sortedItems.filter((i) => activeFilters.has(i.alert_type))

  const counts = {
    new: items.filter((i) => i.alert_type === 'new').length,
    drift: items.filter((i) => i.alert_type === 'drift').length,
    ended: items.filter((i) => i.alert_type === 'ended').length,
  }

  const filterConfig: Array<{ type: AlertType; color: string }> = [
    { type: 'new', color: 'var(--accent)' },
    { type: 'drift', color: 'var(--commit)' },
    { type: 'ended', color: 'var(--text3)' },
  ]

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '14px 20px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
          flexWrap: 'wrap',
        }}
      >
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em', marginRight: 8 }}>
          Review
        </h1>
        {filterConfig.map(({ type, color }) => {
          const count = counts[type]
          const isActive = activeFilters.has(type)
          const isZero = count === 0
          return (
            <button
              key={type}
              onClick={() => !isZero && toggleFilter(type)}
              disabled={isZero}
              style={{
                padding: '4px 10px',
                borderRadius: 12,
                border: isActive ? 'none' : `1px solid var(--border)`,
                background: isActive ? color : 'transparent',
                color: isActive ? '#fff' : 'var(--text2)',
                fontSize: 12,
                fontWeight: 600,
                cursor: isZero ? 'default' : 'pointer',
                opacity: isZero ? 0.4 : 1,
                transition: 'all 0.1s',
              }}
            >
              {type}: {count}
            </button>
          )
        })}
      </div>

      {/* Content */}
      <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px' }}>
        {loading ? (
          <div>
            {[0, 1, 2].map((i) => (
              <div
                key={i}
                style={{
                  height: 160,
                  background: 'var(--surface)',
                  borderRadius: 4,
                  marginBottom: 12,
                  opacity: 0.4,
                }}
              />
            ))}
          </div>
        ) : filteredItems.length === 0 ? (
          <div style={{ textAlign: 'center', paddingTop: 48 }}>
            {items.length === 0 ? (
              <>
                <p style={{ color: 'var(--text2)', marginBottom: 8, fontSize: 15 }}>Queue clear.</p>
                <p style={{ color: 'var(--text3)', fontSize: 13 }}>
                  No patterns are waiting for review. New patterns will appear here after your next import.
                </p>
              </>
            ) : (
              <>
                <p style={{ color: 'var(--text2)', marginBottom: 8, fontSize: 15 }}>
                  No {Array.from(activeFilters).join(' or ')} alerts pending.
                </p>
                <button
                  onClick={() => setActiveFilters(new Set(['new', 'drift', 'ended']))}
                  style={{
                    background: 'none',
                    border: 'none',
                    cursor: 'pointer',
                    color: 'var(--accent)',
                    fontSize: 13,
                  }}
                >
                  Show all types
                </button>
              </>
            )}
          </div>
        ) : (
          <>
            {filteredItems.map((item) => {
              if (item.alert_type === 'new') {
                return <NewCard key={item.id} item={item} onAction={handleAction} />
              }
              if (item.alert_type === 'drift') {
                return <DriftCard key={item.id} item={item} onAction={handleAction} />
              }
              return <EndedCard key={item.id} item={item} onAction={handleAction} />
            })}

            {hasMore && (
              <div style={{ textAlign: 'center', padding: 16 }}>
                <button
                  onClick={() => void loadItems(cursor)}
                  disabled={loadingMore}
                  style={{
                    background: 'none',
                    border: '1px solid var(--border)',
                    borderRadius: 4,
                    padding: '6px 16px',
                    cursor: loadingMore ? 'default' : 'pointer',
                    color: 'var(--text2)',
                    fontSize: 13,
                    opacity: loadingMore ? 0.5 : 1,
                  }}
                >
                  {loadingMore ? 'Loading...' : 'Load more'}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
