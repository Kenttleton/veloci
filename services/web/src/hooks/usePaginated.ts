import { useState, useEffect, useCallback } from 'react'
import type { ApiMeta } from '../api/resources'

type FetchFn<T> = (params: { after?: string; limit?: number }) => Promise<{
  data: T[]
  meta: ApiMeta
}>

interface UsePaginatedResult<T> {
  items: T[]
  loadMore: () => void
  hasMore: boolean
  loading: boolean
  error: Error | null
  reset: () => void
}

export function usePaginated<T>(
  fetchFn: FetchFn<T>,
  limit = 50,
): UsePaginatedResult<T> {
  const [items, setItems] = useState<T[]>([])
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const [initialized, setInitialized] = useState(false)

  const load = useCallback(
    async (after?: string, reset = false) => {
      setLoading(true)
      setError(null)
      try {
        const result = await fetchFn({ after, limit })
        if (reset) {
          setItems(result.data)
        } else {
          setItems((prev) => [...prev, ...result.data])
        }
        setCursor(result.meta.next_cursor)
        setHasMore(result.meta.has_more ?? false)
      } catch (e) {
        setError(e instanceof Error ? e : new Error('Unknown error'))
      } finally {
        setLoading(false)
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [fetchFn, limit],
  )

  useEffect(() => {
    if (!initialized) {
      setInitialized(true)
      void load(undefined, true)
    }
  }, [initialized, load])

  const loadMore = useCallback(() => {
    if (!loading && hasMore && cursor) {
      void load(cursor)
    }
  }, [loading, hasMore, cursor, load])

  const reset = useCallback(() => {
    setItems([])
    setCursor(undefined)
    setHasMore(false)
    setInitialized(false)
  }, [])

  return { items, loadMore, hasMore, loading, error, reset }
}
