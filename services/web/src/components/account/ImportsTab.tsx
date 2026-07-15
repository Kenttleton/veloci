import { format, parseISO } from 'date-fns'
import { useListImportsInfinite } from '../../api/cursorQuery'
import type { ImportView } from '../../api/generated/velociAPI.schemas'

interface ImportsTabProps {
  accountId: string
}


function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, { bg: string; text: string }> = {
    complete: { bg: 'color-mix(in srgb, var(--accent) 15%, transparent)', text: 'var(--accent)' },
    processing: { bg: 'color-mix(in srgb, #facc15 15%, transparent)', text: '#ca8a04' },
    failed: { bg: 'color-mix(in srgb, #ef4444 15%, transparent)', text: '#b91c1c' },
    pending: { bg: 'var(--surface2)', text: 'var(--text3)' },
  }
  const style = colors[status.toLowerCase()] ?? { bg: 'var(--surface2)', text: 'var(--text3)' }
  return (
    <span
      style={{
        fontSize: 10,
        fontWeight: 600,
        letterSpacing: '0.04em',
        textTransform: 'uppercase',
        padding: '2px 6px',
        borderRadius: 4,
        background: style.bg,
        color: style.text,
      }}
    >
      {status}
    </span>
  )
}

export function ImportsTab({ accountId: _accountId }: ImportsTabProps) {
  const { data, fetchNextPage, hasNextPage, isFetching } = useListImportsInfinite({ limit: 50 })
  const rows: ImportView[] = data?.pages.flatMap((p) => p.data.data ?? []) ?? []

  if (!data && isFetching) {
    return (
      <div style={{ padding: 20, color: 'var(--text3)', fontSize: 13 }}>Loading...</div>
    )
  }

  if (rows.length === 0 && !isFetching) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <p style={{ color: 'var(--text2)', marginBottom: 8 }}>No imports for this account.</p>
        <p style={{ color: 'var(--text3)', fontSize: 13 }}>
          Use the import button in the sidebar to upload a CSV.
        </p>
      </div>
    )
  }

  return (
    <div>
      {/* Column headers */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          padding: '6px 16px',
          borderBottom: '1px solid var(--border)',
          background: 'var(--bg)',
        }}
      >
        <span style={{ width: 120, fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
          Uploaded
        </span>
        <span style={{ width: 64, fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em', textAlign: 'right' }}>
          Rows
        </span>
        <span style={{ flex: 1, fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
          Date range
        </span>
        <span style={{ width: 80, fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
          Status
        </span>
      </div>

      {/* Import rows */}
      {rows.map((imp) => (
        <div
          key={imp.id}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '10px 16px',
            borderBottom: '1px solid var(--border)',
            background: 'var(--surface)',
          }}
        >
          <span style={{ width: 120, fontSize: 13, color: 'var(--text)', flexShrink: 0 }}>
            {format(parseISO(imp.uploaded_at), 'MMM d, yyyy')}
          </span>
          <span style={{ width: 64, fontSize: 13, color: 'var(--text2)', textAlign: 'right', flexShrink: 0 }}>
            {imp.row_count != null ? imp.row_count.toLocaleString() : '—'}
          </span>
          <span style={{ flex: 1, fontSize: 12, color: 'var(--text3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {imp.date_range_start && imp.date_range_end
              ? `${format(parseISO(imp.date_range_start), 'MMM d')} – ${format(parseISO(imp.date_range_end), 'MMM d')}`
              : '—'}
          </span>
          <span style={{ width: 80, flexShrink: 0 }}>
            <StatusBadge status={imp.status} />
          </span>
        </div>
      ))}

      {/* Load more */}
      {hasNextPage && (
        <div style={{ padding: '10px 16px', borderTop: '1px solid var(--border)' }}>
          <button
            onClick={() => void fetchNextPage()}
            disabled={isFetching}
            style={{
              background: 'none',
              border: 'none',
              cursor: isFetching ? 'default' : 'pointer',
              padding: 0,
              color: 'var(--accent)',
              fontSize: 13,
              opacity: isFetching ? 0.5 : 1,
            }}
          >
            {isFetching ? 'Loading...' : 'Load more'}
          </button>
        </div>
      )}
    </div>
  )
}
