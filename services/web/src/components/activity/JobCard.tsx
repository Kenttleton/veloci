import { useState } from 'react'
import { format, parseISO, isToday, isYesterday } from 'date-fns'
import { Check, AlertTriangle, ChevronDown, ChevronRight } from 'lucide-react'
import type { JobView } from '../../api/generated/velociAPI.schemas'

interface JobCardProps {
  job: JobView
  forceExpanded?: boolean
}

const JOB_TYPE_LABELS: Record<string, string> = {
  'import.process': 'Import',
  'entries.reprocess': 'Entries reprocess',
  'account.analyze': 'Recalculate',
}

type JobMetadata = {
  transactions_imported?: number
  transactions_skipped_duplicate?: number
  entries_processed?: number
  snapshots_written?: number
}

function formatTimestamp(dateStr: string): string {
  const d = parseISO(dateStr)
  const time = format(d, 'h:mm a')
  if (isToday(d)) return time
  if (isYesterday(d)) return `Yesterday · ${time}`
  return `${format(d, 'MMM d')} · ${time}`
}

function StatusDot({ status }: { status: string }) {
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

function summaryLine(job: JobView): string {
  const meta = job.metadata as JobMetadata | null | undefined

  if (job.status === 'queued') return 'Waiting to start'
  if (job.status === 'processing') {
    return 'Processing'
  }
  if (job.status === 'complete') {
    const start = job.started_at ?? job.queued_at
    const elapsed = job.completed_at && start
      ? ((parseISO(job.completed_at).getTime() - parseISO(start).getTime()) / 1000).toFixed(1)
      : null
    const parts: string[] = []
    if (elapsed) parts.push(`Completed in ${elapsed}s`)
    if (job.job_type === 'import.process' && meta?.transactions_imported != null) {
      parts.push(`${meta.transactions_imported} transactions imported`)
      if (meta.transactions_skipped_duplicate) {
        parts.push(`${meta.transactions_skipped_duplicate} duplicates skipped`)
      }
    }
    if (job.job_type === 'entries.reprocess' && meta?.entries_processed != null) {
      parts.push(`${meta.entries_processed} entries reprocessed`)
    }
    if (job.job_type === 'account.analyze' && meta?.snapshots_written != null) {
      parts.push(`${meta.snapshots_written} snapshots written`)
    }
    return parts.join(' · ')
  }
  if (job.status === 'failed') {
    return 'Failed'
  }
  return ''
}

export function JobCard({ job, forceExpanded = false }: JobCardProps) {
  const [expanded, setExpanded] = useState(forceExpanded || job.status === 'failed')

  const jobTypeLabel = JOB_TYPE_LABELS[job.job_type] ?? job.job_type
  const title = jobTypeLabel
  const summary = summaryLine(job)
  const isFailed = job.status === 'failed'

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

      {/* Expanded detail — error info */}
      {expanded && isFailed && (
        <div
          style={{
            borderTop: '1px solid var(--border)',
            padding: '8px 14px 12px 32px',
            display: 'flex',
            flexDirection: 'column',
            gap: 4,
          }}
        >
          {job.error && (
            <div
              style={{
                padding: '8px 10px',
                background: 'var(--surface2)',
                borderRadius: 4,
                fontFamily: 'monospace',
                fontSize: 12,
                color: 'var(--text2)',
                lineHeight: 1.5,
              }}
            >
              {job.error}
            </div>
          )}
          <div style={{ display: 'flex', gap: 8, marginTop: 4 }}>
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
        </div>
      )}
    </div>
  )
}
