import { useNavigate } from 'react-router-dom'

interface PendingBadgeProps {
  jobId?: string | null
}

export function PendingDetailsLink({ jobId }: PendingBadgeProps) {
  const navigate = useNavigate()

  function handleClick() {
    if (jobId) {
      navigate(`/activity?job=${jobId}`)
    } else {
      navigate('/activity')
    }
  }

  return (
    <button
      onClick={handleClick}
      style={{
        background: 'none',
        border: 'none',
        cursor: 'pointer',
        color: 'var(--text3)',
        fontSize: '11px',
        padding: 0,
        whiteSpace: 'nowrap',
      }}
    >
      → Details
    </button>
  )
}
