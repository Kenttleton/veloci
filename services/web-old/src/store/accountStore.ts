import { create } from 'zustand'
import { registerStore } from './registry'

interface AccountUIState {
  scrollTransactions: number
}

interface AccountUIStore {
  accounts: Record<string, AccountUIState>
  setScroll: (id: string, offset: number) => void
  clear: () => void
}

export const useAccountStore = create<AccountUIStore>((set) => ({
  accounts: {},
  setScroll: (id, offset) =>
    set((state) => ({
      accounts: {
        ...state.accounts,
        [id]: { scrollTransactions: offset },
      },
    })),
  clear: () => set({ accounts: {} }),
}))

registerStore('accounts', () => useAccountStore.getState().clear())
