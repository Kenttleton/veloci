import React, { useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import { JobCard } from '../components/activity/JobCard'
import { useListJobsInfinite } from '../api/cursorQuery'

export function ActivityPage() {
  const [searchParams] = useSearchParams()
  const targetJobId = searchParams.get('job')
  const targetRef = useRef<HTMLDivElement>(null)

  const { data, fetchNextPage, hasNextPage, isFetching } = useListJobsInfinite({ limit: 50 })
  const jobs = data?.pages.flatMap((p) => p.data.data ?? []) ?? []
  const loading = !data && isFetching

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

            {hasNextPage && (
              <div style={{ textAlign: 'center', padding: 16 }}>
                <button
                  onClick={() => void fetchNextPage()}
                  disabled={isFetching}
                  style={{
                    background: 'none',
                    border: '1px solid var(--border)',
                    borderRadius: 4,
                    padding: '6px 16px',
                    cursor: isFetching ? 'default' : 'pointer',
                    color: 'var(--text2)',
                    fontSize: 13,
                    opacity: isFetching ? 0.5 : 1,
                  }}
                >
                  {isFetching ? 'Loading...' : 'Load older'}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
