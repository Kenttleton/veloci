import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { NewCard } from '../components/review/NewCard'
import { DriftCard } from '../components/review/DriftCard'
import { EndedCard } from '../components/review/EndedCard'
import { useListReviewInfinite } from '../api/cursorQuery'
import type { ReviewView } from '../api/generated/velociAPI.schemas'

type AlertType = 'new' | 'drift' | 'ended'

export function ReviewPage() {
  const queryClient = useQueryClient()
  const { data, fetchNextPage, hasNextPage, isFetching } = useListReviewInfinite({ limit: 50 })
  const items: ReviewView[] = data?.pages.flatMap((p) => p.data.data ?? []) ?? []
  const loading = !data && isFetching

  const [activeFilters, setActiveFilters] = useState<Set<AlertType>>(
    new Set(['new', 'drift', 'ended']),
  )

  function handleAction() {
    void queryClient.invalidateQueries({ queryKey: ['infinite', '/review'] })
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
    const order: Record<string, number> = { new: 0, drift: 1, ended: 2 }
    const aOrder = order[a.alert_type] ?? 99
    const bOrder = order[b.alert_type] ?? 99
    if (aOrder !== bOrder) return aOrder - bOrder
    return (b.confidence ?? 0) - (a.confidence ?? 0)
  })

  const filteredItems = sortedItems.filter((i) =>
    activeFilters.has(i.alert_type as AlertType),
  )

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

            {hasNextPage && (
              <div style={{ textAlign: 'center', padding: 16 }}>
                <button
                  onClick={() => void fetchNextPage()}
                  disabled={isFetching}
                  style={{
                    background: 'none',
                    border: '1px solid var(--border)',
                    borderRadius: 4,
                    padding: '6px 16px',
                    cursor: isFetching ? 'default' : 'pointer',
                    color: 'var(--text2)',
                    fontSize: 13,
                    opacity: isFetching ? 0.5 : 1,
                  }}
                >
                  {isFetching ? 'Loading...' : 'Load older'}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
