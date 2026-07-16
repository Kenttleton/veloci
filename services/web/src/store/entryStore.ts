import { create } from 'zustand'
import { listEntries } from '../api/generated/velociAPI'
import type { EntryView } from '../api/generated/velociAPI.schemas'
import { registerStore } from './registry'

type StoreStatus = 'idle' | 'loading' | 'loaded' | 'error'

interface EntryStore {
  entries: Record<string, EntryView>
  status: StoreStatus
  nextCursor: string | null

  // Load all entries (entries are definitional, not event-based — fetch all statuses).
  load: () => Promise<void>
  // Re-fetch all entries and merge by ID — overwrites stale records.
  refresh: () => Promise<void>
  // Fetch the next cursor page.
  loadMore: () => Promise<void>
  clear: () => void
}

async function fetchAllEntryPages(
  params: Parameters<typeof listEntries>[0],
  onPage: (entries: EntryView[], nextCursor: string | null) => boolean,
) {
  let cursor: string | undefined = undefined
  // eslint-disable-next-line no-constant-condition
  while (true) {
    const res = await listEntries({ ...params, cursor, limit: 200 })
    const page = res.data.data ?? []
    const next = res.data.meta?.next_cursor ?? null
    const shouldContinue = onPage(page, next)
    if (!shouldContinue || !next) break
    cursor = next
  }
}

export const useEntryStore = create<EntryStore>((set, get) => ({
  entries: {},
  status: 'idle',
  nextCursor: null,

  async load() {
    if (get().status === 'loading') return
    set({ status: 'loading' })
    try {
      const merged: Record<string, EntryView> = {}
      let finalCursor: string | null = null

      // Fetch all statuses — entries are budget lines, not high-volume events
      await fetchAllEntryPages({ status: 'all' }, (page, next) => {
        for (const e of page) merged[e.id] = e
        finalCursor = next
        return true
      })

      set({ entries: merged, status: 'loaded', nextCursor: finalCursor })
    } catch {
      set({ status: 'error' })
    }
  },

  async refresh() {
    const { entries } = get()
    try {
      const refreshed: Record<string, EntryView> = {}

      await fetchAllEntryPages({ status: 'all' }, (page) => {
        for (const e of page) refreshed[e.id] = e
        return true
      })

      set({
        entries: { ...entries, ...refreshed },
        status: 'loaded',
      })
    } catch {
      // leave existing data intact on refresh failure
    }
  },

  async loadMore() {
    const { nextCursor, entries, status } = get()
    if (!nextCursor || status === 'loading') return
    set({ status: 'loading' })
    try {
      const res = await listEntries({ cursor: nextCursor, limit: 200 })
      const page = res.data.data ?? []
      const next = res.data.meta?.next_cursor ?? null
      const merged: Record<string, EntryView> = { ...entries }
      for (const e of page) merged[e.id] = e

      set({ entries: merged, status: 'loaded', nextCursor: next })
    } catch {
      set({ status: 'loaded' })
    }
  },

  clear() {
    set({ entries: {}, status: 'idle', nextCursor: null })
  },
}))

registerStore('entries', () => useEntryStore.getState().clear())
