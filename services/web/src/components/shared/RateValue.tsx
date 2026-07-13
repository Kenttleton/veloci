import { useRateFormat } from '../../contexts/RateFormatContext'

interface RateValueProps {
  ratePerDay: number
  className?: string
  showUnit?: boolean
}

export function RateValue({ ratePerDay, className, showUnit = false }: RateValueProps) {
  const { formatRate, format } = useRateFormat()
  return (
    <span className={className}>
      {formatRate(ratePerDay)}
      {showUnit && <span style={{ color: 'var(--text3)', fontSize: '0.85em', marginLeft: 2 }}>{format}</span>}
    </span>
  )
}
