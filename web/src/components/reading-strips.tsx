"use client"

import { useMemo, useRef, useState } from "react"
import { RotateCcw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

// Readings drawn as strips stacked on one shared time axis, so a CPU spike, a
// traffic burst and a disk storm line up by eye. Used for a VM and for a node.
// Nothing is drawn for time when nothing was recorded, so an outage or a
// powered-off machine shows as a gap. See DESIGN.md, "Readings".

export const RANGES = [
  { id: "1h", label: "1 hour", minutes: 60 },
  { id: "24h", label: "24 hours", minutes: 1440 },
  { id: "7d", label: "7 days", minutes: 10080 },
] as const
export type RangeId = (typeof RANGES)[number]["id"]
export const minutesOf = (range: RangeId) => RANGES.find((r) => r.id === range)!.minutes

export interface StripSeries {
  key: string
  label?: string
  color: string
  /** One value per reading, aligned with `times`. */
  values: number[]
  /** The live value, when the machine is up and reporting. */
  now?: number
  fmt: (v: number) => string
}
export interface ReadingStrip {
  id: string
  name: string
  /** The value at the top of this strip's scale. */
  top: number
  axis: string
  series: StripSeries[]
  /** Shown instead of the plot when this reading cannot be drawn. */
  note?: string
}

export const CHART_1 = "var(--chart-1)"
export const CHART_2 = "var(--chart-2)"

const RATE_UNITS = ["B/s", "KB/s", "MB/s", "GB/s"]
const NICE = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000]

/** Picks one unit for a pair of byte rates and a round top for the axis. */
export function rateScale(values: number[][], live: (number | undefined)[] = []) {
  const peak = Math.max(1, ...values.flat(), ...live.map((v) => v ?? 0))
  let unit = 0
  let scaled = peak
  while (scaled >= 1024 && unit < RATE_UNITS.length - 1) {
    scaled /= 1024
    unit++
  }
  const div = 1024 ** unit
  const top = NICE.find((n) => n >= scaled) ?? Math.ceil(scaled)
  const fmt = (v: number) => {
    const x = v / div
    return `${x >= 100 ? x.toFixed(0) : x.toFixed(1)} ${RATE_UNITS[unit]}`
  }
  return { top: top * div, axis: `${top} ${RATE_UNITS[unit]}`, fmt }
}

// Plot box in SVG units. The height matches the CSS height, so y maps 1:1.
const W = 600
const H = 64
const TOP = 4
const BASE = 63

export function ReadingStrips({
  title,
  range,
  onRange,
  times,
  strips,
  loading,
  error,
  onRetry,
  running,
  offWord = "Off",
  emptyHint,
  heading: Heading = "h2",
  className,
  children,
}: {
  title: string
  range: RangeId
  /** Leave out when a control elsewhere on the page sets the range. */
  onRange?: (range: RangeId) => void
  /** When each reading was taken, in milliseconds, oldest first. */
  times: number[]
  strips: ReadingStrip[]
  loading: boolean
  error?: Error | null
  onRetry: () => void
  /** Whether the machine is up, so a missing live value reads as "No reading" rather than "Off". */
  running: boolean
  offWord?: string
  /** Said after "No readings in the last hour." */
  emptyHint: string
  heading?: "h2" | "h3"
  className?: string
  /** Shown beside the title: a status line, a link. */
  children?: React.ReactNode
}) {
  const minutes = minutesOf(range)
  const wrapRef = useRef<HTMLDivElement>(null)
  const plotRef = useRef<HTMLDivElement>(null)
  const [hover, setHover] = useState<{ i: number; left: number } | null>(null)

  const { xs, gapBefore, fresh, start, end } = useMemo(() => {
    const windowMs = minutes * 60_000
    const end = Date.now()
    const start = end - windowMs
    const xs = times.map((t) => Math.min(1, Math.max(0, (t - start) / windowMs)))
    // A reading is expected every minute, or every averaged slice on long
    // ranges. Anything much longer than that between two points is a gap.
    const step = Math.max(60_000, windowMs / 360)
    const gapBefore = times.map((t, i) => i === 0 || t - times[i - 1] > step * 2.5)
    const fresh = times.length > 0 && end - times[times.length - 1] <= step * 2.5
    return { xs, gapBefore, fresh, start, end }
  }, [times, minutes])
  const count = times.length
  const at = hover && hover.i < count ? hover : null

  const y = (v: number, top: number) => BASE - (Math.min(Math.max(v, 0), top) / (top || 1)) * (BASE - TOP)
  const linePath = (values: number[], top: number) =>
    values.map((v, i) => `${gapBefore[i] ? "M" : "L"}${(xs[i] * W).toFixed(1)} ${y(v, top).toFixed(1)}`).join("")
  const areaPath = (values: number[], top: number) => {
    let d = ""
    let from = ""
    values.forEach((v, i) => {
      const x = (xs[i] * W).toFixed(1)
      if (gapBefore[i]) {
        if (from) d += `L${(xs[i - 1] * W).toFixed(1)} ${BASE}L${from} ${BASE}Z`
        from = x
        d += `M${x} ${y(v, top).toFixed(1)}`
      } else d += `L${x} ${y(v, top).toFixed(1)}`
    })
    if (from) d += `L${(xs[xs.length - 1] * W).toFixed(1)} ${BASE}L${from} ${BASE}Z`
    return d
  }

  const place = (i: number) => {
    const plot = plotRef.current?.getBoundingClientRect()
    const wrap = wrapRef.current?.getBoundingClientRect()
    if (!plot || !wrap) return
    setHover({ i, left: plot.left - wrap.left + xs[i] * plot.width })
  }
  const onPointer = (clientX: number) => {
    const plot = plotRef.current?.getBoundingClientRect()
    if (!plot || count === 0) return
    const f = (clientX - plot.left) / plot.width
    if (f < 0 || f > 1) return setHover(null)
    let best = 0
    for (let i = 1; i < xs.length; i++) if (Math.abs(xs[i] - f) < Math.abs(xs[best] - f)) best = i
    place(best)
  }

  const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })
  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" })
  const stamp = (t: number) => (range === "1h" ? clock.format(t) : `${day.format(t)}, ${clock.format(t)}`)
  const ticks = [0, 0.25, 0.5, 0.75].map((f) => {
    const t = start + f * (end - start)
    return range === "7d" ? day.format(t) : clock.format(t)
  })

  const readout = (s: StripSeries) => {
    if (at) return s.fmt(s.values[at.i])
    if (s.now !== undefined) return s.fmt(s.now)
    if (!running) return offWord
    return fresh && s.values.length ? s.fmt(s.values[s.values.length - 1]) : "No reading"
  }
  const span = RANGES.find((r) => r.id === range)!.label.replace(/^1 /, "")
  const headingId = `readings-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`
  const firstPlot = strips.findIndex((strip) => !strip.note)

  return (
    <section className={cn("rounded-lg border bg-card p-5 text-card-foreground", className)} aria-labelledby={headingId}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
          <Heading id={headingId} className="text-base font-semibold">
            {title}
          </Heading>
          {children}
        </div>
        {onRange && (
          <div role="group" aria-label="Time range" className="flex gap-1.5">
            {RANGES.map((r) => (
              <Button
                key={r.id}
                type="button"
                size="sm"
                variant={r.id === range ? "default" : "outline"}
                aria-pressed={r.id === range}
                onClick={() => {
                  setHover(null)
                  onRange(r.id)
                }}
              >
                {r.label}
              </Button>
            ))}
          </div>
        )}
      </div>

      {loading ? (
        <div className="mt-6 space-y-4" aria-busy="true" aria-label="Loading readings">
          {strips.map((s) => (
            <Skeleton key={s.id} className="h-16 w-full" />
          ))}
        </div>
      ) : error ? (
        <div className="mt-6 flex flex-wrap items-center gap-3" role="alert">
          <p className="text-sm text-muted-foreground">Could not load the readings: {error.message}</p>
          <Button size="sm" variant="outline" onClick={onRetry} className="gap-2">
            <RotateCcw className="h-3 w-3" aria-hidden="true" />
            Try again
          </Button>
        </div>
      ) : count === 0 ? (
        <p className="mt-6 text-sm text-muted-foreground">
          No readings in the last {span}. {emptyHint}
        </p>
      ) : (
        <>
          <div
            ref={wrapRef}
            tabIndex={0}
            role="group"
            aria-label={`${title} over the last ${span}. Left and right arrow keys step through time, Escape returns to now.`}
            className="relative mt-8 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-card"
            onPointerMove={(e) => onPointer(e.clientX)}
            onPointerLeave={() => setHover(null)}
            onBlur={() => setHover(null)}
            onKeyDown={(e) => {
              if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                e.preventDefault()
                const from = at?.i ?? count - 1
                place(Math.min(count - 1, Math.max(0, from + (e.key === "ArrowLeft" ? -1 : 1))))
              } else if (e.key === "Escape") setHover(null)
            }}
          >
            {at && (
              <div className="pointer-events-none absolute inset-y-0 z-10 w-px bg-foreground/60" style={{ left: at.left }}>
                <span
                  className={cn(
                    "absolute -top-6 whitespace-nowrap rounded-sm bg-foreground px-1.5 py-0.5 text-[11px] font-semibold text-background",
                    xs[at.i] < 0.12 ? "left-0" : xs[at.i] > 0.88 ? "right-0" : "left-1/2 -translate-x-1/2"
                  )}
                >
                  {stamp(times[at.i])}
                </span>
              </div>
            )}
            {strips.map((strip, n) => (
              <div
                key={strip.id}
                className="grid grid-cols-[minmax(0,1fr)_3.5rem] gap-x-3.5 border-t border-hair py-3 sm:grid-cols-[9.5rem_minmax(0,1fr)_3.5rem]"
              >
                <div className="col-span-2 mb-2 flex flex-wrap items-baseline gap-x-4 gap-y-1 sm:col-span-1 sm:mb-0 sm:block">
                  <p className="text-xs font-medium text-muted-foreground">{strip.name}</p>
                  {strip.series.map((s) => (
                    <p key={s.key} className={cn("flex items-center gap-1.5 text-xs text-muted-foreground", s.label ? "sm:mt-1" : "sm:mt-0.5")}>
                      {s.label && (
                        <>
                          <span className="inline-block w-3.5 border-t-2" style={{ borderColor: s.color }} aria-hidden="true" />
                          {s.label}
                        </>
                      )}
                      <span className={cn("font-semibold text-foreground", s.label ? "text-sm sm:ml-auto" : "text-lg sm:text-xl")}>
                        {strip.note ? "Not reported" : readout(s)}
                      </span>
                    </p>
                  ))}
                </div>
                {strip.note ? (
                  <p className="col-span-2 flex h-16 items-center text-xs text-muted-foreground">{strip.note}</p>
                ) : (
                  <>
                    <div ref={n === firstPlot ? plotRef : undefined} className="relative h-16 border-b border-border">
                      <span className="absolute inset-x-0 top-1 border-t border-hair" aria-hidden="true" />
                      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="absolute inset-0 h-full w-full overflow-visible" aria-hidden="true">
                        {strip.series.length === 1 && <path d={areaPath(strip.series[0].values, strip.top)} fill={strip.series[0].color} fillOpacity={0.1} stroke="none" />}
                        {strip.series.map((s) => (
                          <path
                            key={s.key}
                            d={linePath(s.values, strip.top)}
                            fill="none"
                            stroke={s.color}
                            strokeWidth={2}
                            strokeLinejoin="round"
                            strokeLinecap="round"
                            vectorEffect="non-scaling-stroke"
                          />
                        ))}
                      </svg>
                      {/* The newest point, marked only while the machine is still reporting. */}
                      {fresh &&
                        strip.series.map((s) => (
                          <span
                            key={s.key}
                            aria-hidden="true"
                            className="absolute h-2 w-2 rounded-full ring-2 ring-card"
                            style={{
                              background: s.color,
                              left: `calc(${xs[count - 1] * 100}% - 4px)`,
                              top: y(s.values[count - 1], strip.top) - 4,
                            }}
                          />
                        ))}
                    </div>
                    <div className="flex h-16 flex-col justify-between whitespace-nowrap text-[11px] leading-none text-muted-foreground" aria-hidden="true">
                      <span>{strip.axis}</span>
                      <span>0</span>
                    </div>
                  </>
                )}
              </div>
            ))}
          </div>
          <div className="grid grid-cols-[minmax(0,1fr)_3.5rem] gap-x-3.5 sm:grid-cols-[9.5rem_minmax(0,1fr)_3.5rem]" aria-hidden="true">
            <div className="flex justify-between pt-1.5 text-[11px] text-muted-foreground sm:col-start-2">
              {ticks.map((t, i) => (
                <span key={i}>{t}</span>
              ))}
              <span>now</span>
            </div>
          </div>

          <details className="mt-3 text-sm">
            <summary className="w-fit py-1.5 font-semibold">Readings as a table</summary>
            <div className="overflow-x-auto">
              <table className="w-full min-w-[24rem] border-collapse">
                <thead>
                  <tr className="text-xs text-muted-foreground">
                    <th className="py-1.5 pr-3 text-left font-medium">Reading</th>
                    <th className="px-3 py-1.5 text-right font-medium">Now</th>
                    <th className="px-3 py-1.5 text-right font-medium">Lowest</th>
                    <th className="py-1.5 pl-3 text-right font-medium">Highest</th>
                  </tr>
                </thead>
                <tbody>
                  {strips
                    .filter((strip) => !strip.note)
                    .flatMap((strip) =>
                      strip.series.map((s) => (
                        <tr key={s.key} className="border-t border-hair">
                          <td className="py-1.5 pr-3">{s.label ? `${strip.name} ${s.label.toLowerCase()}` : strip.name}</td>
                          <td className="px-3 py-1.5 text-right">{s.now !== undefined ? s.fmt(s.now) : running ? "No reading" : offWord}</td>
                          <td className="px-3 py-1.5 text-right">{s.fmt(Math.min(...s.values))}</td>
                          <td className="py-1.5 pl-3 text-right">{s.fmt(Math.max(...s.values))}</td>
                        </tr>
                      ))
                    )}
                </tbody>
              </table>
            </div>
          </details>
        </>
      )}
    </section>
  )
}
