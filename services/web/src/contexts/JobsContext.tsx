import React, { createContext, useContext, useState, useCallback } from 'react'
import type { Job, SseJobEvent } from '../api/resources'

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
