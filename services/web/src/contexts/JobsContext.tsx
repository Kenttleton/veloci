import React, { createContext, useContext, useState, useCallback } from 'react'
import type { JobView } from '../api/generated/velociAPI.schemas'

// SSE event shape (kept separate — not from the API spec):
export interface SseJobEvent {
  job_id: string
  job_type: string
  status: string
  error: string | null
  queued_at: string
  completed_at: string | null
}

interface JobsContextValue {
  jobs: JobView[]
  runningJobs: JobView[]
  setJobs: (jobs: JobView[]) => void
  upsertJobFromEvent: (event: SseJobEvent) => void
  hasRunningJobs: boolean
  pendingJobId: string | null
}

const JobsContext = createContext<JobsContextValue | null>(null)

export function JobsProvider({ children }: { children: React.ReactNode }) {
  const [jobs, setJobsState] = useState<JobView[]>([])

  const setJobs = useCallback((incoming: JobView[]) => {
    setJobsState(incoming)
  }, [])

  const upsertJobFromEvent = useCallback((event: SseJobEvent) => {
    setJobsState((prev) => {
      const idx = prev.findIndex((j) => j.id === event.job_id)
      if (idx === -1) {
        // New job not yet in list — create a minimal record
        const newJob: JobView = {
          id: event.job_id,
          job_type: event.job_type,
          status: event.status,
          error: event.error,
          queued_at: event.queued_at,
          started_at: null,
          completed_at: event.completed_at,
          triggered_by: '',
          metadata: {},
        }
        return [newJob, ...prev]
      }
      const updated = [...prev]
      updated[idx] = {
        ...updated[idx],
        status: event.status,
        error: event.error,
        completed_at: event.completed_at,
      }
      return updated
    })
  }, [])

  const runningJobs = jobs.filter(
    (j) => j.status === 'queued' || j.status === 'processing',
  )

  const hasRunningJobs = runningJobs.length > 0

  const pendingJobId = runningJobs[0]?.id ?? null

  return (
    <JobsContext.Provider
      value={{ jobs, runningJobs, setJobs, upsertJobFromEvent, hasRunningJobs, pendingJobId }}
    >
      {children}
    </JobsContext.Provider>
  )
}

export function useJobs(): JobsContextValue {
  const ctx = useContext(JobsContext)
  if (!ctx) throw new Error('useJobs must be used within JobsProvider')
  return ctx
}
