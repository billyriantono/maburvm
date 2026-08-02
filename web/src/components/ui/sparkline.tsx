"use client"

interface SparklineProps {
  data: number[]
  /** Value mapped to full height. Defaults to 100 (percentages). */
  max?: number
  height?: number
  className?: string
  /** Tailwind text-* color class; drives both stroke and translucent fill. */
  colorClass?: string
}

// Sparkline renders a compact area+line trend as inline SVG — no charting deps.
export function Sparkline({ data, max = 100, height = 40, className = "", colorClass = "text-primary" }: SparklineProps) {
  const W = 100
  const pts = Array.isArray(data) ? data : []
  const n = pts.length
  const peak = Math.max(max, ...pts, 1)

  const coords = (() => {
    if (n === 1) {
      const y = (height - (Math.min(pts[0], peak) / peak) * height).toFixed(2)
      return `0,${y} ${W},${y}`
    }
    return pts
      .map((v, i) => {
        const x = ((i / (n - 1)) * W).toFixed(2)
        const y = (height - (Math.min(Math.max(v, 0), peak) / peak) * height).toFixed(2)
        return `${x},${y}`
      })
      .join(" ")
  })()

  return (
    <div className={`w-full ${className}`} style={{ height }}>
      {n === 0 ? (
        <div className="w-full h-full flex items-center justify-center text-[10px] font-medium text-muted-foreground">
          No data yet
        </div>
      ) : (
        <svg viewBox={`0 0 ${W} ${height}`} preserveAspectRatio="none" className={`w-full h-full ${colorClass}`}>
          <polyline points={`0,${height} ${coords} ${W},${height}`} fill="currentColor" fillOpacity={0.15} stroke="none" />
          <polyline points={coords} fill="none" stroke="currentColor" strokeWidth={2} vectorEffect="non-scaling-stroke" />
        </svg>
      )}
    </div>
  )
}
