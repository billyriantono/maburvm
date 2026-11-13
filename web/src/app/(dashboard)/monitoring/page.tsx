"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusBadge } from "@/components/status-badge"
import { CHART_1, CHART_2, RANGES, ReadingStrips, minutesOf, rateScale, type RangeId, type ReadingStrip } from "@/components/reading-strips"
import { attentionList, silentFor } from "@/lib/attention"
import { useDashboardOverview, type DashboardNode } from "@/lib/hooks/use-dashboard"
import { useNodeMetricsHistory } from "@/lib/hooks/use-nodes"

// Monitoring is the fleet on one time axis. Every node gets the same four
// strips, one under the other and all on the range chosen at the top, so a
// spike on one node can be compared with the others at the same moment. Above
// them, anything that needs someone to act.

const GIB = 1024 ** 3

function NodeReadings({ n, range }: { n: DashboardNode; range: RangeId }) {
  const { data, isLoading, error, refetch } = useNodeMetricsHistory(n.id, minutesOf(range))
  const silent = silentFor(n)
  const up = n.status === "active" && !silent

  const { times, strips } = useMemo(() => {
    const samples = (data ?? []).filter((s) => s.status !== "offline")
    const rx = samples.map((s) => s.network_rx_bytes_per_sec)
    const tx = samples.map((s) => s.network_tx_bytes_per_sec)
    const net = rateScale([rx, tx])
    const conn = samples.map((s) => s.conntrack_count)
    const connMax = samples.reduce((m, s) => Math.max(m, s.conntrack_max), 0)
    // Scaled to what was seen, not to the table size: the table holds millions
    // and a normal day would be a flat line at the bottom.
    const connTop = [1000, 2000, 5000, 10_000, 20_000, 50_000, 100_000, 200_000, 500_000, 1_000_000, 2_000_000, 5_000_000].find((v) => v >= Math.max(...conn, 1)) ?? Math.max(...conn, 1)
    const count = (v: number) => Math.round(v).toLocaleString()
    const pct = (v: number) => `${Math.round(v)}%`
    const strips: ReadingStrip[] = [
      { id: "cpu", name: "CPU", top: 100, axis: "100%", series: [{ key: "cpu", color: CHART_1, values: samples.map((s) => s.cpu_usage), fmt: pct }] },
      { id: "mem", name: "Memory in use", top: 100, axis: "100%", series: [{ key: "mem", color: CHART_1, values: samples.map((s) => s.memory_usage), fmt: pct }] },
      {
        id: "net",
        name: "Network",
        top: net.top,
        axis: net.axis,
        series: [
          { key: "rx", label: "In", color: CHART_1, values: rx, fmt: net.fmt },
          { key: "tx", label: "Out", color: CHART_2, values: tx, fmt: net.fmt },
        ],
      },
      {
        id: "conn",
        name: connMax > 0 ? `Connections tracked, of ${connMax.toLocaleString()}` : "Connections tracked",
        top: connTop,
        axis: connTop.toLocaleString(),
        // A node that predates this reading sends zeros for it.
        note: connMax === 0 && conn.every((v) => v === 0) ? "This node's agent does not report its connection table." : undefined,
        series: [{ key: "conn", color: CHART_1, values: conn, fmt: count }],
      },
    ]
    return { times: samples.map((s) => new Date(s.recorded_at).getTime()), strips }
  }, [data])

  const ramRatio = n.memory_total_bytes > 0 ? n.ram_allocated_mb / 1024 / (n.memory_total_bytes / GIB) : null
  const diskShare = n.disk_total_bytes > 0 ? (n.disk_used_bytes / n.disk_total_bytes) * 100 : null

  return (
    <ReadingStrips
      title={n.name}
      heading="h2"
      range={range}
      times={times}
      strips={strips}
      loading={isLoading}
      error={error as Error | null}
      onRetry={() => refetch()}
      running={up}
      offWord="Down"
      emptyHint={up ? "One is recorded every minute, so the first will appear shortly." : "Readings are recorded only while the node answers the panel."}
    >
      <StatusBadge status={n.status} />
      {silent && <span className="text-xs font-medium text-destructive">This node {silent}</span>}
      <span className="text-xs text-muted-foreground">
        {[
          n.region,
          `${n.running} of ${n.vm_count} VMs running`,
          ramRatio !== null ? `memory promised ${ramRatio.toFixed(1)} times installed` : "",
          diskShare !== null ? `storage ${Math.round(diskShare)}% full` : "",
        ]
          .filter(Boolean)
          .join(", ")}
      </span>
      <Link href={`/nodes/${n.id}`} className="text-xs font-semibold underline underline-offset-4">
        Open node
      </Link>
    </ReadingStrips>
  )
}

export default function MonitoringPage() {
  const { data: o, isLoading, error, refetch } = useDashboardOverview()
  const [range, setRange] = useState<RangeId>("1h")
  const nodes = o?.nodes ?? []
  const needs = o ? attentionList(o) : []
  const answering = nodes.filter((n) => n.status === "active" && !silentFor(n)).length

  return (
    <div className="mx-auto max-w-7xl">
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Monitoring</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {isLoading
              ? "Loading the fleet"
              : error || !o
                ? "The fleet could not be loaded."
                : `${answering} of ${nodes.length} ${nodes.length === 1 ? "node" : "nodes"} answering, ${o.totals.running} of ${o.totals.vms} VMs running. ` +
                  (needs.length ? `${needs.length} ${needs.length === 1 ? "thing needs" : "things need"} attention.` : "Nothing needs attention.")}
          </p>
        </div>
        <div role="group" aria-label="Time range for every node" className="flex gap-1.5">
          {RANGES.map((r) => (
            <Button key={r.id} type="button" size="sm" variant={r.id === range ? "default" : "outline"} aria-pressed={r.id === range} onClick={() => setRange(r.id)}>
              {r.label}
            </Button>
          ))}
        </div>
      </div>

      {isLoading ? (
        <div className="space-y-4" aria-busy="true">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-72 w-full" />
          ))}
        </div>
      ) : error || !o ? (
        <div className="rounded-lg border bg-card p-8" role="alert">
          <p className="font-medium">The fleet could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
          <Button className="mt-4" variant="outline" onClick={() => refetch()}>
            Try again
          </Button>
        </div>
      ) : (
        <div className="space-y-5">
          <section aria-labelledby="needs-attention" className="rounded-lg border bg-card text-card-foreground">
            <h2 id="needs-attention" className="border-b border-hair px-5 py-3 text-sm font-semibold">
              {needs.length ? `Needs attention (${needs.length})` : "Needs attention"}
            </h2>
            {needs.length === 0 ? (
              <p className="px-5 py-4 text-sm text-muted-foreground">
                Nothing right now. Every node is answering with memory and CPU below 90%, no VM has failed or stalled, no backup or job failed in the last 24 hours, and no
                storage or address range is nearly full.
              </p>
            ) : (
              <ul className="divide-y divide-hair">
                {needs.map((n) => (
                  <li key={n.key} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3 text-sm">
                    <span className={`h-2.5 w-2.5 shrink-0 rounded-full ${n.bad ? "bg-destructive" : "bg-warning"}`} aria-hidden="true" />
                    <span className="min-w-0 flex-1 basis-64">{n.text}</span>
                    <Link href={n.href} className="font-semibold underline underline-offset-4">
                      {n.go}
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>

          {nodes.length === 0 ? (
            <div className="rounded-lg border bg-card p-8">
              <p className="font-medium">No node is connected yet.</p>
              <p className="mt-1 text-sm text-muted-foreground">Readings appear here once a node is added and answering.</p>
              <Button asChild className="mt-4">
                <Link href="/nodes/new">Add a node</Link>
              </Button>
            </div>
          ) : (
            nodes.map((n) => <NodeReadings key={n.id} n={n} range={range} />)
          )}
        </div>
      )}
    </div>
  )
}
