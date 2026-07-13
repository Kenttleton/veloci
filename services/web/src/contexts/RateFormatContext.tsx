import React, { createContext, useContext, useState } from 'react'

export type RateFormat = '/day' | '/mo' | '/yr'

interface RateFormatContextValue {
  format: RateFormat
  setFormat: (f: RateFormat) => void
  formatRate: (ratePerDay: number) => string
  formatLabel: string
  granularity: 'day' | 'month' | 'year'
  viewportCandles: number
}

const RateFormatContext = createContext<RateFormatContextValue | null>(null)

export function RateFormatProvider({ children }: { children: React.ReactNode }) {
  const [format, setFormat] = useState<RateFormat>('/mo')

  function formatRate(ratePerDay: number): string {
    let value: number
    if (format === '/day') value = ratePerDay
    else if (format === '/mo') value = ratePerDay * 30.44
    else value = ratePerDay * 365

    const abs = Math.abs(value)
    const formatted =
      abs >= 10000
        ? `$${(value / 1000).toFixed(1)}k`
        : abs >= 1000
          ? `$${value.toLocaleString('en-US', { minimumFractionDigits: 0, maximumFractionDigits: 0 })}`
          : `$${value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`

    return formatted
  }

  const granularity: 'day' | 'month' | 'year' =
    format === '/day' ? 'day' : format === '/mo' ? 'month' : 'year'

  const viewportCandles = format === '/day' ? 14 : format === '/mo' ? 12 : 10

  const formatLabel = format

  return (
    <RateFormatContext.Provider
      value={{ format, setFormat, formatRate, formatLabel, granularity, viewportCandles }}
    >
      {children}
    </RateFormatContext.Provider>
  )
}

export function useRateFormat(): RateFormatContextValue {
  const ctx = useContext(RateFormatContext)
  if (!ctx) throw new Error('useRateFormat must be used within RateFormatProvider')
  return ctx
}
