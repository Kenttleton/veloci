import { create } from 'zustand'
import { listTransactions } from '../api/generated/velociAPI'
import type { TransactionView } from '../api/generated/velociAPI.schemas'
import { registerStore } from './registry'

type StoreStatus = 'idle' | 'loading' | 'loaded' | 'error'

interface TransactionStore {
  transactions: Record<string, TransactionView>
  status: StoreStatus
  oldestLoadedDate: string | null  // ISO date of the oldest transaction currently in store
  nextCursor: string | null        // cursor for lazy-loading further back in history

  // Initial load anchored to latest data date. Paginates until 30 days are covered.
  load: () => Promise<void>
  // Re-fetch from (oldestLoadedDate - 30 days) to latest. Merges by ID — overwrites stale records.
  refresh: () => Promise<void>
  // Fetch the next cursor page (older history on demand).
  loadMore: () => Promise<void>
  clear: () => void
}

const SPAN_DAYS = 30

function subDays(dateStr: string, days: number): string {
  const d = new Date(dateStr + 'T00:00:00Z')
  d.setUTCDate(d.getUTCDate() - days)
  return d.toISOString().slice(0, 10)
}

function oldestDate(txns: Record<string, TransactionView>): string | null {
  let oldest: string | null = null
  for (const tx of Object.values(txns)) {
    if (!oldest || tx.date < oldest) oldest = tx.date
  }
  return oldest
}

async function fetchAllPages(
  params: Parameters<typeof listTransactions>[0],
  onPage: (txns: TransactionView[], nextCursor: string | null) => boolean,
) {
  let cursor: string | undefined = undefined
  // eslint-disable-next-line no-constant-condition
  while (true) {
    const res = await listTransactions({ ...params, cursor, limit: 200 })
    const page = res.data.data ?? []
    const next = res.data.meta?.next_cursor ?? null
    const shouldContinue = onPage(page, next)
    if (!shouldContinue || !next) break
    cursor = next
  }
}

export const useTransactionStore = create<TransactionStore>((set, get) => ({
  transactions: {},
  status: 'idle',
  oldestLoadedDate: null,
  nextCursor: null,

  async load() {
    if (get().status === 'loading') return
    set({ status: 'loading' })
    try {
      const merged: Record<string, TransactionView> = {}
      let latestDate: string | null = null
      let finalCursor: string | null = null

      await fetchAllPages({}, (page, next) => {
        for (const tx of page) merged[tx.id] = tx
        if (!latestDate && page.length > 0) latestDate = page[0].date
        finalCursor = next

        // Stop paginating once we have 30 days from the latest date
        if (latestDate && page.length > 0) {
          const oldest = page[page.length - 1].date
          if (oldest <= subDays(latestDate, SPAN_DAYS)) return false
        }
        return true
      })

      set({
        transactions: merged,
        status: 'loaded',
        oldestLoadedDate: oldestDate(merged),
        nextCursor: finalCursor,
      })
    } catch {
      set({ status: 'error' })
    }
  },

  async refresh() {
    // Re-fetch from (oldestLoadedDate - 30d) to latest, merge by ID
    const { oldestLoadedDate, transactions } = get()
    const dateFrom = oldestLoadedDate ? subDays(oldestLoadedDate, SPAN_DAYS) : undefined

    try {
      const refreshed: Record<string, TransactionView> = {}

      await fetchAllPages({ date_from: dateFrom }, (page) => {
        for (const tx of page) refreshed[tx.id] = tx
        return true
      })

      set({
        transactions: { ...transactions, ...refreshed },
        oldestLoadedDate: oldestDate({ ...transactions, ...refreshed }),
        status: 'loaded',
      })
    } catch {
      // leave existing data intact on refresh failure
    }
  },

  async loadMore() {
    const { nextCursor, transactions, status } = get()
    if (!nextCursor || status === 'loading') return
    set({ status: 'loading' })
    try {
      const res = await listTransactions({ cursor: nextCursor, limit: 200 })
      const page = res.data.data ?? []
      const next = res.data.meta?.next_cursor ?? null
      const merged: Record<string, TransactionView> = { ...transactions }
      for (const tx of page) merged[tx.id] = tx

      set({
        transactions: merged,
        status: 'loaded',
        oldestLoadedDate: oldestDate(merged),
        nextCursor: next,
      })
    } catch {
      set({ status: 'loaded' })
    }
  },

  clear() {
    set({ transactions: {}, status: 'idle', oldestLoadedDate: null, nextCursor: null })
  },
}))

registerStore('transactions', () => useTransactionStore.getState().clear())
