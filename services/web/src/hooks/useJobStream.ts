import { useEffect, useRef } from 'react'
import { useJobs } from '../contexts/JobsContext'
import type { SseJobEvent } from '../contexts/JobsContext'
import { getToken } from '../auth/tokens'
import { useTransactionStore } from '../store/transactionStore'
import { useEntryStore } from '../store/entryStore'

const TRANSACTION_JOB_TYPES = new Set([
  'import.process',
  'account.analyze',
  'rules.reprocess',
])

const BASE = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
const MAX_BACKOFF_MS = 30_000

export function useJobStream() {
  const { upsertJobFromEvent } = useJobs()
  const refreshTransactions = useTransactionStore((s) => s.refresh)
  const refreshEntries = useEntryStore((s) => s.refresh)
  const backoffRef = useRef(1000)
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    let cancelled = false

    function connect() {
      if (cancelled) return
      const token = getToken()
      if (!token) return

      const url = `${BASE}/jobs/stream?token=${encodeURIComponent(token)}`
      const es = new EventSource(url)
      esRef.current = es

      es.onmessage = (e: MessageEvent<string>) => {
        try {
          const event = JSON.parse(e.data) as SseJobEvent
          upsertJobFromEvent(event)
          backoffRef.current = 1000
          if (event.status === 'complete' && TRANSACTION_JOB_TYPES.has(event.job_type)) {
            void refreshTransactions()
            void refreshEntries()
          }
        } catch {
          // ignore parse errors
        }
      }

      es.onerror = () => {
        es.close()
        esRef.current = null
        if (!cancelled) {
          const delay = backoffRef.current
          backoffRef.current = Math.min(delay * 2, MAX_BACKOFF_MS)
          timeoutRef.current = setTimeout(connect, delay)
        }
      }
    }

    connect()

    return () => {
      cancelled = true
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
      if (esRef.current) {
        esRef.current.close()
        esRef.current = null
      }
    }
  }, [upsertJobFromEvent, refreshTransactions, refreshEntries])
}
