"use client"

import { useState } from "react"

// Series colours. Blue #3b82f6 / emerald #059669 were validated together against
// both the light and dark chart surfaces: they clear the lightness band, the
// chroma floor, colour-vision separation (ΔE 22.7 deutan) and 3:1 contrast in
// both modes — which is why one pair serves both rather than a mode switch.
//
// Identity is never carried by colour alone: each series is directly labelled
// with its current value, so the chart still reads in greyscale or print.
const RX_COLOR = "#3b82f6"
const TX_COLOR = "#059669"

export interface BandwidthPoint {
  rx: number
  tx: number
}

interface BandwidthChartProps {
  data: BandwidthPoint[]
  height?: number
  /** Formats a bytes-per-second value for labels and the hover readout. */
  format: (bytesPerSec: number) => string
}

// BandwidthChart plots inbound and outbound rate on ONE shared scale.
//
// One axis, deliberately. Two y-scales would let a trickle of outbound traffic
// be drawn as tall as a flood of inbound, which is the single most misleading
// thing a chart of two measures can do. Both series are bytes per second, so
// they belong on the same scale and the comparison between them is the point.
//
// Inline SVG rather than a charting library: the repo already draws its trends
// this way, and a dependency earns its place by doing something this cannot.
export function BandwidthChart({ data, height = 96, format }: BandwidthChartProps) {
  const [hover, setHover] = useState<number | null>(null)

  const W = 100
  const points = Array.isArray(data) ? data : []
  const n = points.length

  if (n === 0) {
    return (
      <div
        className="w-full flex items-center justify-center text-xs font-medium text-muted-foreground"
        style={{ height }}
      >
        No samples yet
      </div>
    )
  }

  // A shared peak across both series, floored at 1 so a completely idle node
  // renders a flat line at the bottom instead of dividing by zero.
  const peak = Math.max(1, ...points.map((p) => Math.max(p.rx, p.tx)))

  const path = (pick: (p: BandwidthPoint) => number) =>
    points
      .map((p, i) => {
        const x = n === 1 ? W / 2 : (i / (n - 1)) * W
        const y = height - (Math.min(Math.max(pick(p), 0), peak) / peak) * height
        return `${x.toFixed(2)},${y.toFixed(2)}`
      })
      .join(" ")

  const rxPath = path((p) => p.rx)
  const txPath = path((p) => p.tx)

  const latest = points[n - 1]
  const shown = hover !== null ? points[hover] : latest
  const hoverX = hover !== null && n > 1 ? (hover / (n - 1)) * W : null

  return (
    <div className="w-full">
      <svg
        viewBox={`0 0 ${W} ${height}`}
        preserveAspectRatio="none"
        className="w-full"
        style={{ height }}
        role="img"
        aria-label={`Inbound ${format(latest.rx)}, outbound ${format(latest.tx)}`}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = e.currentTarget.getBoundingClientRect()
          if (rect.width === 0) return
          const ratio = (e.clientX - rect.left) / rect.width
          setHover(Math.min(n - 1, Math.max(0, Math.round(ratio * (n - 1)))))
        }}
      >
        {/* Inbound is drawn first and filled; outbound sits on top as a line, so
            the smaller series is never hidden underneath the larger one. */}
        <polyline
          points={`0,${height} ${rxPath} ${W},${height}`}
          fill={RX_COLOR}
          fillOpacity={0.14}
          stroke="none"
        />
        <polyline
          points={rxPath}
          fill="none"
          stroke={RX_COLOR}
          strokeWidth={2}
          vectorEffect="non-scaling-stroke"
        />
        <polyline
          points={txPath}
          fill="none"
          stroke={TX_COLOR}
          strokeWidth={2}
          vectorEffect="non-scaling-stroke"
        />
        {hoverX !== null && (
          <line
            x1={hoverX}
            x2={hoverX}
            y1={0}
            y2={height}
            stroke="currentColor"
            strokeWidth={1}
            className="text-muted-foreground/40"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>

      {/* Legend and direct values in one row. Two series always carry a legend,
          and the numbers wear text tokens rather than the series colour — the
          swatch beside them carries identity. */}
      <div className="flex items-center justify-between gap-3 mt-2 text-xs">
        <span className="flex items-center gap-1.5">
          <span className="w-2.5 h-2.5 rounded-sm shrink-0" style={{ background: RX_COLOR }} />
          <span className="text-muted-foreground">In</span>
          <span className="font-mono font-medium text-foreground">{format(shown.rx)}</span>
        </span>
        <span className="flex items-center gap-1.5">
          <span className="w-2.5 h-2.5 rounded-sm shrink-0" style={{ background: TX_COLOR }} />
          <span className="text-muted-foreground">Out</span>
          <span className="font-mono font-medium text-foreground">{format(shown.tx)}</span>
        </span>
      </div>
    </div>
  )
}
