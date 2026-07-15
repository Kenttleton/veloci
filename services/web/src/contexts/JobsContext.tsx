import React, { createContext, useContext, useState, useCallback } from 'react'

// TODO(task-6-11): Interim local types — will be replaced when activity/review hooks are rebuilt
export interface SseJobEvent {
  job_id: string
  job_type: 'import.process' | 'entries.reprocess' | 'account.analyze'
  status: 'queued' | 'processing' | 'complete' | 'failed'
  error: string | null
  queued_at: string
  completed_at: string | null
}

export interface Job {
  id: string
  entity_id: string
  job_type: 'import.process' | 'entries.reprocess' | 'account.analyze'
  status: 'queued' | 'processing' | 'complete' | 'failed'
  error: string | null
  retriable: boolean
  queued_at: string
  completed_at: string | null
  triggered_by: string
  account_id: string | null
  account_name: string | null
  current_stage: number | null
  total_stages: number | null
  current_stage_name: string | null
  metadata: {
    transactions_imported?: number
    transactions_skipped_duplicate?: number
    entries_processed?: number
    snapshots_written?: number
  }
  stages: Array<{
    stage_number: number
    name: string
    status: 'pending' | 'running' | 'complete' | 'failed'
    elapsed_ms: number | null
    error: string | null
  }>
}

interface JobsContextValue {
  jobs: Job[]
  runningJobs: Job[]
  setJobs: (jobs: Job[]) => void
  upsertJobFromEvent: (event: SseJobEvent) => void
  hasRunningJobs: boolean
  pendingJobId: string | null
}

const JobsContext = createContext<JobsContextValue | null>(null)

export function JobsProvider({ children }: { children: React.ReactNode }) {
  const [jobs, setJobsState] = useState<Job[]>([])

  const setJobs = useCallback((incoming: Job[]) => {
    setJobsState(incoming)
  }, [])

  const upsertJobFromEvent = useCallback((event: SseJobEvent) => {
    setJobsState((prev) => {
      const idx = prev.findIndex((j) => j.id === event.job_id)
      if (idx === -1) {
        // New job not yet in list — create a minimal record
        const newJob: Job = {
          id: event.job_id,
          entity_id: '',
          job_type: event.job_type,
          status: event.status,
          error: event.error,
          retriable: false,
          queued_at: event.queued_at,
          completed_at: event.completed_at,
          triggered_by: '',
          account_id: null,
          account_name: null,
          current_stage: null,
          total_stages: null,
          current_stage_name: null,
          metadata: {},
          stages: [],
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
