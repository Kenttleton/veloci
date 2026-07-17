interface LabelPillProps {
  name: string | null
  className?: string
}

export function LabelPill({ name, className }: LabelPillProps) {
  const displayName = name || 'No label'
  const isEmpty = !name

  return (
    <span
      className={className}
      style={{
        display: 'inline-block',
        padding: '1px 8px',
        borderRadius: 3,
        fontSize: '0.75rem',
        background: 'var(--surface2)',
        color: isEmpty ? 'var(--text3)' : 'var(--text2)',
        whiteSpace: 'nowrap',
      }}
    >
      {displayName}
    </span>
  )
}
