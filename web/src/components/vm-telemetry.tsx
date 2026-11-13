"use client"

import { useMemo, useRef, useState } from "react"
import { RotateCcw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useVMMetricsHistory } from "@/lib/hooks/use-vms"
import type { VMMetrics, VMMetricSample } from "@/types/vm"
import { cn } from "@/lib/utils"

// The four readings of a VM drawn as strips on one shared time axis, so a CPU
// spike, a traffic burst and a disk storm line up by eye. Every number comes
// from the samples the panel stores each minute; nothing is drawn while the VM
// is off, so powered-off time shows as a gap.

const RANGES = [
  { id: "1h", label: "1 hour", minutes: 60 },
  { id: "24h", label: "24 hours", minutes: 1440 },
  { id: "7d", label: "7 days", minutes: 10080 },
] as const
type RangeId = (typeof RANGES)[number]["id"]

// Plot box in SVG units. The height matches the CSS height, so y maps 1:1.
const W = 600
const H = 64
const TOP = 4
const BASE = 63

const GIB = 1024 ** 3
const RATE_UNITS = ["B/s", "KB/s", "MB/s", "GB/s"]
const NICE = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000]

interface Series {
  key: string
  label?: string
  color: string
  values: number[]
  /** The live value, when the VM is running and reporting. */
  now?: number
  fmt: (v: number) => string
}
interface Strip {
  id: string
  name: string
  top: number
  axis: string
  series: Series[]
  /** Shown instead of the plot when this reading cannot be drawn. */
  note?: string
}

/** Picks one unit for a pair of byte rates and a round top for the axis. */
function rateScale(values: number[][], live: (number | undefined)[]) {
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

export function VMTelemetry({
  vmId,
  running,
  live,
  ramMB,
  className,
}: {
  vmId: string
  running: boolean
  /** The current reading, polled separately every few seconds. */
  live?: VMMetrics
  ramMB: number
  className?: string
}) {
  const [range, setRange] = useState<RangeId>("1h")
  const minutes = RANGES.find((r) => r.id === range)!.minutes
  const { data, isLoading, error, refetch } = useVMMetricsHistory(vmId, minutes)
  const wrapRef = useRef<HTMLDivElement>(null)
  const plotRef = useRef<HTMLDivElement>(null)
  const [hover, setHover] = useState<{ i: number; left: number } | null>(null)

  const view = useMemo(() => {
    const samples: VMMetricSample[] = data ?? []
    const windowMs = minutes * 60_000
    const end = Date.now()
    const start = end - windowMs
    const times = samples.map((s) => new Date(s.recorded_at).getTime())
    const xs = times.map((t) => Math.min(1, Math.max(0, (t - start) / windowMs)))
    // A sample is expected every minute, or every averaged slice on long
    // ranges. Anything much longer than that between two points is a gap.
    const step = Math.max(60_000, windowMs / 360)
    const gapBefore = times.map((t, i) => i === 0 || t - times[i - 1] > step * 2.5)
    const fresh = times.length > 0 && end - times[times.length - 1] <= step * 2.5

    const liveNow = running ? live : undefined
    const ramGB = ramMB / 1024
    const mem = samples.map((s) => s.memory_used_bytes / GIB)
    const memReported = mem.some((v) => v > 0) || (liveNow?.memory_used ?? 0) > 0
    const rx = samples.map((s) => s.network_rx_bytes_per_sec)
    const tx = samples.map((s) => s.network_tx_bytes_per_sec)
    const rd = samples.map((s) => s.disk_read_bytes_per_sec)
    const wr = samples.map((s) => s.disk_write_bytes_per_sec)
    const net = rateScale([rx, tx], [liveNow?.network_rx_bytes_per_sec, liveNow?.network_tx_bytes_per_sec])
    const dsk = rateScale([rd, wr], [liveNow?.disk_read_bytes_per_sec, liveNow?.disk_write_bytes_per_sec])
    const c1 = "var(--chart-1)"
    const c2 = "var(--chart-2)"

    const strips: Strip[] = [
      {
        id: "cpu",
        name: "CPU",
        top: 100,
        axis: "100%",
        series: [{ key: "cpu", color: c1, values: samples.map((s) => s.cpu_usage), now: liveNow?.cpu_percent, fmt: (v) => `${Math.round(v)}%` }],
      },
      {
        id: "mem",
        name: "Memory",
        top: ramGB,
        axis: `${Number(ramGB.toFixed(1))} GB`,
        // A running machine never uses zero memory. Zero means the operating
        // system inside is not reporting it.
        note: memReported ? undefined : "Not reported by this VM. The guest agent inside it supplies this reading.",
        series: [
          {
            key: "mem",
            color: c1,
            values: mem,
            now: liveNow && liveNow.memory_used > 0 ? liveNow.memory_used / GIB : undefined,
            fmt: (v) => `${v.toFixed(1)} GB`,
          },
        ],
      },
      {
        id: "net",
        name: "Network",
        top: net.top,
        axis: net.axis,
        series: [
          { key: "rx", label: "In", color: c1, values: rx, now: liveNow?.network_rx_bytes_per_sec, fmt: net.fmt },
          { key: "tx", label: "Out", color: c2, values: tx, now: liveNow?.network_tx_bytes_per_sec, fmt: net.fmt },
        ],
      },
      {
        id: "dsk",
        name: "Disk",
        top: dsk.top,
        axis: dsk.axis,
        series: [
          { key: "rd", label: "Read", color: c1, values: rd, now: liveNow?.disk_read_bytes_per_sec, fmt: dsk.fmt },
          { key: "wr", label: "Write", color: c2, values: wr, now: liveNow?.disk_write_bytes_per_sec, fmt: dsk.fmt },
        ],
      },
    ]
    return { strips, xs, times, gapBefore, fresh, start, end, count: samples.length }
  }, [data, minutes, running, live, ramMB])

  const { strips, xs, times, gapBefore, fresh, count } = view

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
    const t = view.start + f * (view.end - view.start)
    return range === "7d" ? day.format(t) : clock.format(t)
  })

  const readout = (s: Series) => {
    if (hover) return s.fmt(s.values[hover.i])
    if (s.now !== undefined) return s.fmt(s.now)
    if (!running) return "Off"
    return fresh && s.values.length ? s.fmt(s.values[s.values.length - 1]) : "No reading"
  }
  const rangeLabel = RANGES.find((r) => r.id === range)!.label

  return (
    <section className={cn("rounded-lg border bg-card p-5 text-card-foreground", className)} aria-labelledby="vm-readings">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="vm-readings" className="text-base font-semibold">
          Readings
        </h2>
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
                setRange(r.id)
              }}
            >
              {r.label}
            </Button>
          ))}
        </div>
      </div>

      {isLoading ? (
        <div className="mt-6 space-y-4" aria-busy="true" aria-label="Loading readings">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : error ? (
        <div className="mt-6 flex flex-wrap items-center gap-3" role="alert">
          <p className="text-sm text-muted-foreground">Could not load the readings: {(error as Error).message}</p>
          <Button size="sm" variant="outline" onClick={() => refetch()} className="gap-2">
            <RotateCcw className="h-3 w-3" aria-hidden="true" />
            Try again
          </Button>
        </div>
      ) : count === 0 ? (
        <p className="mt-6 text-sm text-muted-foreground">
          No readings in the last {rangeLabel.replace(/^1 /, "")}. One is recorded every minute while the VM is running
          {running ? ", so the first will appear shortly." : "; start it to begin recording."}
        </p>
      ) : (
        <>
          <div
            ref={wrapRef}
            tabIndex={0}
            role="group"
            aria-label={`Readings over the last ${rangeLabel.replace(/^1 /, "")}. Left and right arrow keys step through time, Escape returns to now.`}
            className="relative mt-8 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-card"
            onPointerMove={(e) => onPointer(e.clientX)}
            onPointerLeave={() => setHover(null)}
            onBlur={() => setHover(null)}
            onKeyDown={(e) => {
              if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                e.preventDefault()
                const from = hover?.i ?? count - 1
                place(Math.min(count - 1, Math.max(0, from + (e.key === "ArrowLeft" ? -1 : 1))))
              } else if (e.key === "Escape") setHover(null)
            }}
          >
            {hover && (
              <div className="pointer-events-none absolute inset-y-0 z-10 w-px bg-foreground/60" style={{ left: hover.left }}>
                <span
                  className={cn(
                    "absolute -top-6 whitespace-nowrap rounded-sm bg-foreground px-1.5 py-0.5 text-[11px] font-semibold text-background",
                    xs[hover.i] < 0.12 ? "left-0" : xs[hover.i] > 0.88 ? "right-0" : "left-1/2 -translate-x-1/2"
                  )}
                >
                  {stamp(times[hover.i])}
                </span>
              </div>
            )}
            {strips.map((strip, n) => (
              <div
                key={strip.id}
                className="grid grid-cols-[minmax(0,1fr)_3.5rem] gap-x-3.5 border-t border-hair py-3 sm:grid-cols-[9.5rem_minmax(0,1fr)_3.5rem]"
              >
                <div className="col-span-2 mb-2 flex flex-wrap items-baseline gap-x-4 gap-y-1 sm:col-span-1 sm:mb-0 sm:block">
                  <h3 className="text-xs font-medium text-muted-foreground">{strip.name}</h3>
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
                    <div ref={n === 0 ? plotRef : undefined} className="relative h-16 border-b border-border">
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
                      {/* The newest point, marked only while the VM is still reporting. */}
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
                          <td className="px-3 py-1.5 text-right">{s.now !== undefined ? s.fmt(s.now) : running ? "No reading" : "Off"}</td>
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
