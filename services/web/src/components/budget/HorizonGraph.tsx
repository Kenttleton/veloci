import React, { useEffect, useRef, useState, useCallback } from 'react'
import {
  createChart,
  CandlestickSeries,
  LineSeries,
  type IChartApi,
  type ISeriesApi,
  type Time,
  type CandlestickData,
  type LineData,
} from 'lightweight-charts'
import { useRateFormat } from '../../contexts/RateFormatContext'
import { useJobs } from '../../contexts/JobsContext'
import { TermTooltip } from '../shared/TermTooltip'
// TODO(task-6-11): getSnapshotHistory will be replaced with generated hook
import { useNavigate } from 'react-router-dom'

// Interim local type until budget components are rebuilt in tasks 6-11
interface SnapshotCandle {
  period_start: string
  period_end: string
  open: number
  close: number
  high: number
  low: number
  actual_rate_per_day: number
  projected_rate_per_day: number
  drift_per_day: number
  slope_per_day: number
  entry_start_date: string
  entry_end_date: string | null
}

async function getSnapshotHistory(
  nodeId: string,
  params: { before?: string; limit?: number; granularity?: string },
): Promise<{ data: SnapshotCandle[]; next_cursor: string | null; has_more: boolean }> {
  const token = localStorage.getItem('token')
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const p: Record<string, string> = {}
  if (params.before) p.before = params.before
  if (params.limit) p.limit = String(params.limit)
  if (params.granularity) p.granularity = params.granularity
  const qs = Object.keys(p).length ? '?' + new URLSearchParams(p).toString() : ''
  const res = await fetch(`${base}/snapshots/${nodeId}/history${qs}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  return res.json() as Promise<{ data: SnapshotCandle[]; next_cursor: string | null; has_more: boolean }>
}

interface HorizonGraphProps {
  nodeId: string | null
}


export function HorizonGraph({ nodeId }: HorizonGraphProps) {
  const { granularity, viewportCandles } = useRateFormat()
  const { hasRunningJobs, pendingJobId } = useJobs()
  const navigate = useNavigate()

  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleSeriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const lineSeriesRef = useRef<ISeriesApi<'Line'> | null>(null)
  const detailsLinkRef = useRef<HTMLAnchorElement | null>(null)

  const [candles, setCandles] = useState<SnapshotCandle[]>([])
  const [_hasMore, setHasMore] = useState(false)
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const pendingBandRef = useRef<{ start: string; end: string } | null>(null)

  // Derive today's date string
  const todayStr = new Date().toISOString().split('T')[0]

  const loadHistory = useCallback(
    async (before?: string, append = false) => {
      if (!nodeId) return
      setLoading(true)
      try {
        const result = await getSnapshotHistory(nodeId, {
          before,
          limit: 60,
          granularity,
        })
        setCandles((prev) => (append ? [...prev, ...result.data] : result.data))
        setHasMore(result.has_more)
        setCursor(result.next_cursor)
      } catch {
        // ignore
      } finally {
        setLoading(false)
      }
    },
    [nodeId, granularity],
  )

  // Initial load
  useEffect(() => {
    setCandles([])
    setCursor(null)
    void loadHistory()
  }, [loadHistory])

  // Create/destroy chart
  useEffect(() => {
    if (!containerRef.current) return

    const container = containerRef.current
    const chart = createChart(container, {
      width: container.clientWidth,
      height: container.clientHeight,
      layout: {
        background: { color: 'transparent' },
        textColor: '#8a9bb8',
      },
      grid: {
        vertLines: { color: '#2d3a52' },
        horzLines: { color: '#2d3a52' },
      },
      crosshair: {
        vertLine: { color: '#7aa3e0', width: 1, style: 2 },
        horzLine: { color: '#7aa3e0', width: 1, style: 2 },
      },
      rightPriceScale: {
        borderColor: '#2d3a52',
        textColor: '#8a9bb8',
      },
      timeScale: {
        borderColor: '#2d3a52',
        timeVisible: true,
        fixLeftEdge: false,
        fixRightEdge: false,
      },
      handleScroll: true,
      handleScale: true,
    })

    chartRef.current = chart

    const candleSeries = chart.addSeries(CandlestickSeries, {
      upColor: '#52c47a',
      downColor: '#d97070',
      borderUpColor: '#52c47a',
      borderDownColor: '#d97070',
      wickUpColor: '#52c47a',
      wickDownColor: '#d97070',
    })
    candleSeriesRef.current = candleSeries

    const lineSeries = chart.addSeries(LineSeries, {
      color: '#7aa3e0',
      lineWidth: 2,
      lineStyle: 2, // dashed
    })
    lineSeriesRef.current = lineSeries

    // Handle resize
    const observer = new ResizeObserver(() => {
      chart.applyOptions({
        width: container.clientWidth,
        height: container.clientHeight,
      })
    })
    observer.observe(container)

    // Subscribe to time scale changes for lazy loading
    chart.timeScale().subscribeVisibleLogicalRangeChange((range) => {
      if (!range) return
      if (range.from < 5 && !loading) {
        void loadHistory(cursor ?? undefined, true)
      }
    })

    return () => {
      observer.disconnect()
      chart.remove()
      chartRef.current = null
      candleSeriesRef.current = null
      lineSeriesRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Feed data to chart
  useEffect(() => {
    if (!candleSeriesRef.current || !lineSeriesRef.current) return
    if (candles.length === 0) return

    const sorted = [...candles].sort(
      (a, b) => new Date(a.period_start).getTime() - new Date(b.period_start).getTime(),
    )

    const candleData: CandlestickData<Time>[] = sorted.map((c) => ({
      time: c.period_start as Time,
      open: c.open,
      high: c.high,
      low: c.low,
      close: c.close,
    }))

    const lineData: LineData<Time>[] = sorted.map((c) => ({
      time: c.period_start as Time,
      value: c.projected_rate_per_day,
    }))

    candleSeriesRef.current.setData(candleData)
    lineSeriesRef.current.setData(lineData)

    // Set viewport to show last N candles
    if (candleData.length > 0) {
      const lastTime = candleData[candleData.length - 1].time
      chartRef.current?.timeScale().scrollToPosition(viewportCandles, false)
      void lastTime
    }
  }, [candles, viewportCandles])

  // Today marker using series markers via createSeriesMarkers
  useEffect(() => {
    if (!chartRef.current || !candleSeriesRef.current) return

    chartRef.current.applyOptions({
      crosshair: {
        vertLine: { color: '#7aa3e0', width: 1, style: 2 },
        horzLine: { color: '#7aa3e0', width: 1, style: 2 },
      },
    })
  }, [todayStr])

  // Draw pending band on overlay canvas
  useEffect(() => {
    if (!hasRunningJobs) {
      pendingBandRef.current = null
      return
    }
    // Use today as the start of pending band for simplicity
    pendingBandRef.current = { start: todayStr, end: todayStr }
  }, [hasRunningJobs, todayStr])

  function handleDetailsClick(e: React.MouseEvent) {
    e.preventDefault()
    navigate(`/activity${pendingJobId ? `?job=${pendingJobId}` : ''}`)
  }

  return (
    <div style={{ padding: '0 20px 8px', flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      {/* Legend */}
      <div style={{ display: 'flex', gap: 16, alignItems: 'center', marginBottom: 6, fontSize: 11, color: 'var(--text3)' }}>
        <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          <span style={{ width: 10, height: 10, background: 'var(--margin-pos)', display: 'inline-block', borderRadius: 1 }} />
          <TermTooltip term="Ahead" definition="Actual margin closed above projection for this period.">Ahead</TermTooltip>
        </span>
        <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          <span style={{ width: 10, height: 10, background: 'var(--commit)', display: 'inline-block', borderRadius: 1 }} />
          <TermTooltip term="Behind" definition="Actual margin closed below projection for this period.">Behind</TermTooltip>
        </span>
        <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          <span style={{ width: 16, height: 2, background: 'var(--accent)', display: 'inline-block', borderTop: '2px dashed var(--accent)' }} />
          <TermTooltip term="Projection" definition="Expected margin, calculated from known commitments amortized over time.">Projection</TermTooltip>
        </span>
        <span style={{ color: 'var(--text3)' }}>
          body = <TermTooltip term="Drift" definition="Difference between actual and projected rate. +$X = ahead, −$X = behind.">drift</TermTooltip> magnitude · wick = period range · drag to scroll
        </span>
      </div>

      {/* Chart container */}
      <div style={{ position: 'relative', flex: 1, minHeight: 220 }}>
        <div
          ref={containerRef}
          style={{
            width: '100%',
            height: '100%',
            opacity: hasRunningJobs ? 0.55 : 1,
            transition: 'opacity 0.2s',
          }}
        />

        {/* TODAY marker text */}
        <div
          style={{
            position: 'absolute',
            top: 4,
            right: 8,
            fontSize: 10,
            color: 'var(--text3)',
            pointerEvents: 'none',
          }}
        >
          TODAY
        </div>

        {/* Pending details link anchored to right of pending band */}
        {hasRunningJobs && (
          <a
            ref={detailsLinkRef}
            href="/activity"
            onClick={handleDetailsClick}
            style={{
              position: 'absolute',
              top: 12,
              right: 12,
              fontSize: 11,
              color: 'var(--text3)',
              textDecoration: 'none',
              zIndex: 10,
              background: 'var(--surface)',
              padding: '2px 6px',
              borderRadius: 3,
              border: '1px solid var(--border)',
            }}
          >
            → Details
          </a>
        )}

        {loading && (
          <div
            style={{
              position: 'absolute',
              top: 8,
              left: 8,
              fontSize: 11,
              color: 'var(--text3)',
            }}
          >
            Loading...
          </div>
        )}

        {!nodeId && (
          <div
            style={{
              position: 'absolute',
              inset: 0,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              color: 'var(--text3)',
              fontSize: 13,
            }}
          >
            Select a node to view horizon
          </div>
        )}
      </div>
    </div>
  )
}
