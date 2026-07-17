import { create } from 'zustand'
import { registerStore } from './registry'

export type StatusFilter = 'all' | 'pending_review' | 'active' | 'inactive'

interface LedgerStore {
  statusFilter: StatusFilter
  scrollOffset: number
  setStatusFilter: (f: StatusFilter) => void
  setScrollOffset: (n: number) => void
  clear: () => void
}

export const useLedgerStore = create<LedgerStore>((set) => ({
  statusFilter: 'all',
  scrollOffset: 0,
  setStatusFilter: (statusFilter) => set({ statusFilter }),
  setScrollOffset: (scrollOffset) => set({ scrollOffset }),
  clear: () => set({ statusFilter: 'all', scrollOffset: 0 }),
}))

registerStore('ledger', () => useLedgerStore.getState().clear())
