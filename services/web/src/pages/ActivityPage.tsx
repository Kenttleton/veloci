import React, { useEffect, useCallback, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import { JobCard } from '../components/activity/JobCard'
import { useJobs } from '../contexts/JobsContext'
// TODO(task-6-11): getJobs will be replaced with generated hook

async function getJobs(params: { after?: string; limit?: number }): Promise<{ data: import('../contexts/JobsContext').Job[]; meta: { next_cursor?: string; has_more?: boolean } }> {
  const token = localStorage.getItem('token')
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const p: Record<string, string> = {}
  if (params.after) p.after = params.after
  if (params.limit) p.limit = String(params.limit)
  const qs = Object.keys(p).length ? '?' + new URLSearchParams(p).toString() : ''
  const res = await fetch(`${base}/jobs${qs}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  return res.json() as Promise<{ data: import('../contexts/JobsContext').Job[]; meta: { next_cursor?: string; has_more?: boolean } }>
}

export function ActivityPage() {
  const [searchParams] = useSearchParams()
  const targetJobId = searchParams.get('job')
  const { jobs, setJobs } = useJobs()
  const [loading, setLoading] = React.useState(true)
  const [hasMore, setHasMore] = React.useState(false)
  const [cursor, setCursor] = React.useState<string | undefined>(undefined)
  const [loadingMore, setLoadingMore] = React.useState(false)
  const targetRef = useRef<HTMLDivElement>(null)

  const loadJobs = useCallback(async (after?: string, reset = false) => {
    if (reset) setLoading(true)
    else setLoadingMore(true)
    try {
      const result = await getJobs({ after, limit: 50 })
      if (reset) {
        setJobs(result.data)
      } else {
        setJobs([...jobs, ...result.data])
      }
      setCursor(result.meta.next_cursor)
      setHasMore(result.meta.has_more ?? false)
    } catch {
      //
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }, [jobs, setJobs])

  useEffect(() => {
    void loadJobs(undefined, true)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Scroll to target job
  useEffect(() => {
    if (targetJobId && targetRef.current) {
      targetRef.current.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
  }, [targetJobId, jobs])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '14px 20px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em' }}>
          Activity
        </h1>
      </div>

      {/* Content */}
      <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px' }}>
        {loading ? (
          <div>
            {[0, 1, 2].map((i) => (
              <div
                key={i}
                style={{
                  height: 80,
                  background: 'var(--surface)',
                  borderRadius: 4,
                  marginBottom: 8,
                  opacity: 0.4,
                }}
              />
            ))}
          </div>
        ) : jobs.length === 0 ? (
          <div style={{ textAlign: 'center', paddingTop: 48 }}>
            <p style={{ color: 'var(--text2)', marginBottom: 8, fontSize: 15 }}>No activity yet.</p>
            <p style={{ color: 'var(--text3)', fontSize: 13 }}>
              Jobs appear here after you import transactions or make changes that trigger a recalculation.
            </p>
          </div>
        ) : (
          <>
            {jobs.map((job) => (
              <div
                key={job.id}
                ref={job.id === targetJobId ? targetRef : undefined}
              >
                <JobCard
                  job={job}
                  forceExpanded={job.id === targetJobId}
                />
              </div>
            ))}

            {hasMore && (
              <div style={{ textAlign: 'center', padding: 16 }}>
                <button
                  onClick={() => void loadJobs(cursor)}
                  disabled={loadingMore}
                  style={{
                    background: 'none',
                    border: '1px solid var(--border)',
                    borderRadius: 4,
                    padding: '6px 16px',
                    cursor: loadingMore ? 'default' : 'pointer',
                    color: 'var(--text2)',
                    fontSize: 13,
                    opacity: loadingMore ? 0.5 : 1,
                  }}
                >
                  {loadingMore ? 'Loading...' : 'Load older'}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
