"use client"

import { useMemo, useState } from "react"
import { useVMMetricsHistory } from "@/lib/hooks/use-vms"
import type { VMMetrics, VMMetricSample } from "@/types/vm"
import { CHART_1, CHART_2, ReadingStrips, minutesOf, rateScale, type RangeId, type ReadingStrip } from "@/components/reading-strips"

// A VM's four readings on one time axis. Every number comes from the samples
// the panel stores each minute; the current value is the live one.

const GIB = 1024 ** 3

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
  const { data, isLoading, error, refetch } = useVMMetricsHistory(vmId, minutesOf(range))

  const { times, strips } = useMemo(() => {
    const samples: VMMetricSample[] = data ?? []
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
    const strips: ReadingStrip[] = [
      {
        id: "cpu",
        name: "CPU",
        top: 100,
        axis: "100%",
        series: [{ key: "cpu", color: CHART_1, values: samples.map((s) => s.cpu_usage), now: liveNow?.cpu_percent, fmt: (v) => `${Math.round(v)}%` }],
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
            color: CHART_1,
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
          { key: "rx", label: "In", color: CHART_1, values: rx, now: liveNow?.network_rx_bytes_per_sec, fmt: net.fmt },
          { key: "tx", label: "Out", color: CHART_2, values: tx, now: liveNow?.network_tx_bytes_per_sec, fmt: net.fmt },
        ],
      },
      {
        id: "dsk",
        name: "Disk",
        top: dsk.top,
        axis: dsk.axis,
        series: [
          { key: "rd", label: "Read", color: CHART_1, values: rd, now: liveNow?.disk_read_bytes_per_sec, fmt: dsk.fmt },
          { key: "wr", label: "Write", color: CHART_2, values: wr, now: liveNow?.disk_write_bytes_per_sec, fmt: dsk.fmt },
        ],
      },
    ]
    return { times: samples.map((s) => new Date(s.recorded_at).getTime()), strips }
  }, [data, running, live, ramMB])

  return (
    <ReadingStrips
      title="Readings"
      range={range}
      onRange={setRange}
      times={times}
      strips={strips}
      loading={isLoading}
      error={error as Error | null}
      onRetry={() => refetch()}
      running={running}
      emptyHint={`One is recorded every minute while the VM is running${running ? ", so the first will appear shortly." : "; start it to begin recording."}`}
      className={className}
    />
  )
}
