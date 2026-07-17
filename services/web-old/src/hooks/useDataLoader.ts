import { useEffect } from 'react'
import { useTransactionStore } from '../store/transactionStore'
import { useEntryStore } from '../store/entryStore'

export function useDataLoader() {
  const txStatus = useTransactionStore((s) => s.status)
  const loadTransactions = useTransactionStore((s) => s.load)
  const entryStatus = useEntryStore((s) => s.status)
  const loadEntries = useEntryStore((s) => s.load)

  useEffect(() => {
    if (txStatus === 'idle') void loadTransactions()
  }, [txStatus, loadTransactions])

  useEffect(() => {
    if (entryStatus === 'idle') void loadEntries()
  }, [entryStatus, loadEntries])
}
