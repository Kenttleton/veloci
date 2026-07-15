import { useState } from 'react'
import { Check, X, AlertTriangle, ChevronDown, ChevronRight } from 'lucide-react'
// TODO(task-6-11): retryJob will be replaced with generated mutation hook
import type { Job } from '../../contexts/JobsContext'

async function retryJob(jobId: string): Promise<void> {
  // TODO(task-6-11): stub — will be replaced with useRetryJobMutation from generated API
  const token = localStorage.getItem('token')
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  await fetch(`${base}/jobs/${jobId}/retry`, {
    method: 'POST',
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
}

interface JobCardProps {
  job: Job
  forceExpanded?: boolean
}

const JOB_TYPE_LABELS: Record<string, string> = {
  'import.process': 'Import',
  'entries.reprocess': 'Entries reprocess',
  'account.analyze': 'Recalculate',
}

const STAGE_DISPLAY_NAMES: Record<number, string> = {
  0: 'CSV import',
  1: 'Entry matching',
  2: 'Pattern detection',
  3: 'Rate computation',
  4: 'Label mapping',
  5: 'Trend analysis',
  6: 'Snapshot write',
  7: 'Projection',
}

function formatTimestamp(dateStr: string): string {
  const d = new Date(dateStr)
  const today = new Date()
  const isToday =
    d.getFullYear() === today.getFullYear() &&
    d.getMonth() === today.getMonth() &&
    d.getDate() === today.getDate()
  if (isToday) {
    return d.toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' })
  }
  return `${d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })} · ${d.toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' })}`
}

function StatusDot({ status }: { status: Job['status'] }) {
  if (status === 'complete') {
    return <Check size={14} style={{ color: 'var(--margin-pos)', flexShrink: 0 }} />
  }
  if (status === 'failed') {
    return <AlertTriangle size={14} style={{ color: 'var(--commit)', flexShrink: 0 }} />
  }
  return (
    <span
      style={{
        width: 8,
        height: 8,
        borderRadius: '50%',
        background: status === 'processing' ? 'var(--accent)' : 'var(--text3)',
        display: 'inline-block',
        flexShrink: 0,
      }}
    />
  )
}

function summaryLine(job: Job): string {
  if (job.status === 'queued') return 'Waiting to start'
  if (job.status === 'processing') {
    const stageName = job.current_stage != null ? STAGE_DISPLAY_NAMES[job.current_stage] : 'Processing'
    const stageNum = job.current_stage != null ? `Stage ${job.current_stage} of ${job.total_stages ?? '?'}` : ''
    return `${stageNum}${stageNum ? ' — ' : ''}${stageName}`
  }
  if (job.status === 'complete') {
    const elapsed = job.completed_at && job.queued_at
      ? ((new Date(job.completed_at).getTime() - new Date(job.queued_at).getTime()) / 1000).toFixed(1)
      : null
    const parts: string[] = []
    if (elapsed) parts.push(`Completed in ${elapsed}s`)
    if (job.job_type === 'import.process' && job.metadata.transactions_imported != null) {
      parts.push(`${job.metadata.transactions_imported} transactions imported`)
      if (job.metadata.transactions_skipped_duplicate) {
        parts.push(`${job.metadata.transactions_skipped_duplicate} duplicates skipped`)
      }
    }
    if (job.job_type === 'entries.reprocess' && job.metadata.entries_processed != null) {
      parts.push(`${job.metadata.entries_processed} entries reprocessed`)
    }
    if (job.job_type === 'account.analyze' && job.metadata.snapshots_written != null) {
      parts.push(`${job.metadata.snapshots_written} snapshots written`)
    }
    return parts.join(' · ')
  }
  if (job.status === 'failed') {
    const failedStage = job.stages.find((s) => s.status === 'failed')
    if (failedStage) {
      return `Failed at Stage ${failedStage.stage_number} — ${STAGE_DISPLAY_NAMES[failedStage.stage_number] ?? failedStage.name}`
    }
    return 'Failed'
  }
  return ''
}

export function JobCard({ job, forceExpanded = false }: JobCardProps) {
  const [expanded, setExpanded] = useState(forceExpanded || job.status === 'failed')
  const [retrying, setRetrying] = useState(false)

  const jobTypeLabel = JOB_TYPE_LABELS[job.job_type] ?? job.job_type
  const accountSuffix = job.account_name ? ` — ${job.account_name}` : ''
  const title = `${jobTypeLabel}${accountSuffix}`
  const summary = summaryLine(job)
  const isFailed = job.status === 'failed'

  async function handleRetry() {
    setRetrying(true)
    try {
      await retryJob(job.id)
    } catch {
      //
    } finally {
      setRetrying(false)
    }
  }

  function copyError() {
    const text = `Job ID: ${job.id}\nError: ${job.error ?? 'Unknown'}`
    void navigator.clipboard.writeText(text)
  }

  return (
    <div
      style={{
        background: isFailed
          ? 'color-mix(in srgb, var(--surface) 94%, var(--commit) 6%)'
          : 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: 4,
        marginBottom: 8,
        overflow: 'hidden',
      }}
    >
      {/* Card header */}
      <div
        style={{
          display: 'flex',
          alignItems: 'flex-start',
          gap: 10,
          padding: '12px 14px',
          cursor: job.status !== 'queued' ? 'pointer' : 'default',
        }}
        onClick={() => job.status !== 'queued' && setExpanded(!expanded)}
      >
        <div style={{ marginTop: 2 }}>
          <StatusDot status={job.status} />
        </div>

        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 8 }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {title}
            </span>
            <span style={{ fontSize: 11, color: 'var(--text3)', flexShrink: 0 }}>
              {formatTimestamp(job.queued_at)}
            </span>
          </div>
          <div style={{ fontSize: 12, color: 'var(--text3)', marginTop: 1 }}>
            Triggered by {job.triggered_by}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginTop: 2 }}>
            {summary}
          </div>
        </div>

        {job.status !== 'queued' && (
          <div style={{ marginTop: 2, color: 'var(--text3)', flexShrink: 0 }}>
            {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          </div>
        )}
      </div>

      {/* Expanded stage list */}
      {expanded && job.stages.length > 0 && (
        <div
          style={{
            borderTop: '1px solid var(--border)',
            padding: '8px 14px 12px 32px',
            display: 'flex',
            flexDirection: 'column',
            gap: 4,
          }}
        >
          {job.stages.map((stage) => {
            const isComplete = stage.status === 'complete'
            const isRunning = stage.status === 'running'
            const isStageFailed = stage.status === 'failed'
            const isPending = stage.status === 'pending'

            return (
              <div key={stage.stage_number}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ width: 16, flexShrink: 0 }}>
                    {isComplete && <Check size={12} style={{ color: 'var(--margin-pos)' }} />}
                    {isRunning && (
                      <span
                        style={{
                          width: 7,
                          height: 7,
                          borderRadius: '50%',
                          background: 'var(--accent)',
                          display: 'inline-block',
                        }}
                      />
                    )}
                    {isStageFailed && <X size={12} style={{ color: 'var(--commit)' }} />}
                    {isPending && (
                      <span style={{ fontSize: 12, color: 'var(--text3)' }}>—</span>
                    )}
                  </span>
                  <span style={{ fontSize: 12, color: isPending ? 'var(--text3)' : 'var(--text2)' }}>
                    Stage {stage.stage_number} — {STAGE_DISPLAY_NAMES[stage.stage_number] ?? stage.name}
                  </span>
                  <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
                    {isComplete && stage.elapsed_ms != null
                      ? `${(stage.elapsed_ms / 1000).toFixed(1)}s`
                      : isRunning
                        ? 'running...'
                        : isPending
                          ? '—'
                          : ''}
                  </span>
                </div>

                {/* Error detail */}
                {isStageFailed && stage.error && (
                  <div
                    style={{
                      marginTop: 6,
                      marginLeft: 24,
                      padding: '8px 10px',
                      background: 'var(--surface2)',
                      borderRadius: 4,
                      fontFamily: 'monospace',
                      fontSize: 12,
                      color: 'var(--text2)',
                      lineHeight: 1.5,
                    }}
                  >
                    {stage.error}
                  </div>
                )}
              </div>
            )
          })}

          {/* Failed actions */}
          {isFailed && (
            <div style={{ display: 'flex', gap: 8, marginTop: 8, marginLeft: 24 }}>
              {job.retriable && (
                <button
                  onClick={() => void handleRetry()}
                  disabled={retrying}
                  style={{
                    background: 'none',
                    border: '1px solid var(--border)',
                    borderRadius: 4,
                    padding: '4px 10px',
                    cursor: retrying ? 'default' : 'pointer',
                    color: 'var(--text2)',
                    fontSize: 12,
                    opacity: retrying ? 0.5 : 1,
                  }}
                >
                  {retrying ? 'Retrying...' : 'Retry'}
                </button>
              )}
              <button
                onClick={copyError}
                style={{
                  background: 'none',
                  border: '1px solid var(--border)',
                  borderRadius: 4,
                  padding: '4px 10px',
                  cursor: 'pointer',
                  color: 'var(--text2)',
                  fontSize: 12,
                }}
              >
                Copy error
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
