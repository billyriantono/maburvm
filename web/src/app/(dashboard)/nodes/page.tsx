"use client"

import { useState } from "react"
import Link from "next/link"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Meter } from "@/components/ui/meter"
import { Sparkline } from "@/components/ui/sparkline"
import { StatusBadge } from "@/components/status-badge"
import { useConfirm } from "@/components/confirm-provider"
import { useCopy } from "@/lib/use-feedback"
import { useDashboardOverview, type DashboardNode } from "@/lib/hooks/use-dashboard"
import { useDeleteNode, useNodeMetricsHistory } from "@/lib/hooks/use-nodes"

// The nodes page answers what an operator opens it for: is each node up, how
// loaded is it, and how much room is left on it. One row per node, built from
// what the panel has recorded, so it loads at once even when a node is down.

const GIB = 1024 ** 3

function rate(bytesPerSec: number): string {
  const units = ["B/s", "KB/s", "MB/s", "GB/s"]
  let v = Math.max(bytesPerSec, 0)
  let u = 0
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024
    u++
  }
  return `${v >= 10 || u === 0 ? Math.round(v) : v.toFixed(1)} ${units[u]}`
}

function since(iso: string): string {
  const min = Math.floor((Date.now() - new Date(iso).getTime()) / 60_000)
  if (min < 1) return "just now"
  if (min < 60) return `${min} min ago`
  const h = Math.floor(min / 60)
  return h < 48 ? `${h} h ago` : `${Math.floor(h / 24)} days ago`
}

/** Reporting means the node has answered within the last five minutes. */
function reporting(n: DashboardNode): boolean {
  return n.status === "active" && !!n.reported_at && Date.now() - new Date(n.reported_at).getTime() <= 5 * 60_000
}

function NodeRow({ n, onRemove }: { n: DashboardNode; onRemove: () => void }) {
  const { copy, copied } = useCopy()
  const { data: history } = useNodeMetricsHistory(n.id, 60)
  // The collector stores a row of zeros when it cannot reach a node; leave those out.
  const cpu = (history ?? []).filter((s) => s.status !== "offline").map((s) => s.cpu_usage)
  const live = reporting(n)
  const ramTotal = n.memory_total_bytes / GIB
  const ramPromised = n.ram_allocated_mb / 1024
  const ramRatio = ramTotal > 0 ? ramPromised / ramTotal : null
  const diskShare = n.disk_total_bytes > 0 ? (n.disk_used_bytes / n.disk_total_bytes) * 100 : null

  return (
    <li className="grid grid-cols-2 gap-x-6 gap-y-5 px-5 py-5 xl:grid-cols-[minmax(0,1.25fr)_minmax(0,0.75fr)_minmax(0,1.2fr)_minmax(0,1.2fr)_minmax(0,1.25fr)_auto] xl:items-center">
      <div className="col-span-2 min-w-0 xl:col-span-1">
        <Link href={`/nodes/${n.id}`} className="break-all text-lg font-semibold underline-offset-4 hover:underline [font-stretch:112%]">
          {n.name}
        </Link>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
          <StatusBadge variant="lamp" className="text-xs" status={n.status} />
          {n.region && <span className="text-xs text-muted-foreground">{n.region}</span>}
          {n.creation_paused && <span className="text-xs font-medium text-muted-foreground">Closed to new VMs</span>}
        </div>
        <p className="mt-1 text-sm">
          <span className="font-mono">{n.ip}</span>
          <button
            type="button"
            onClick={() => copy(n.ip, n.id)}
            aria-label={copied(n.id) ? "Copied" : `Copy address ${n.ip}`}
            className="ml-2 border-b border-border pb-px text-xs font-semibold hover:border-foreground active:translate-y-px"
          >
            {copied(n.id) ? "Copied" : "Copy"}
          </button>
        </p>
      </div>

      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">VMs</p>
        <p className="text-sm">
          <span className="font-semibold">{n.running}</span> running of {n.vm_count}
        </p>
        <p className="text-xs text-muted-foreground">{n.vcpu_allocated} vCPU promised</p>
      </div>

      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">Memory promised to VMs</p>
        {ramRatio === null ? (
          <p className="text-sm text-muted-foreground">{ramPromised.toFixed(0)} GB. The node has not reported how much it has.</p>
        ) : (
          <>
            <p className="text-sm">
              <span className="font-semibold">{ramPromised.toFixed(0)} GB</span> of {ramTotal.toFixed(0)} GB installed
              {ramRatio > 1 ? ` (${ramRatio.toFixed(1)} times)` : ""}
            </p>
            <Meter percent={ramRatio * 100} over={ramRatio > 1} />
          </>
        )}
      </div>

      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">Storage in use</p>
        {diskShare === null ? (
          <p className="text-sm text-muted-foreground">No storage registered</p>
        ) : (
          <>
            <p className="text-sm">
              <span className="font-semibold">{(n.disk_used_bytes / GIB).toFixed(0)} GB</span> of {(n.disk_total_bytes / GIB).toFixed(0)} GB ({Math.round(diskShare)}%)
            </p>
            <Meter percent={diskShare} over={diskShare >= 90} />
          </>
        )}
      </div>

      <div className="col-span-2 min-w-0 xl:col-span-1">
        <p className="text-xs text-muted-foreground">Load now</p>
        {live && n.cpu_usage !== null && n.memory_usage !== null ? (
          <>
            <div className="flex items-center gap-3" title={`CPU over the last hour, now ${Math.round(n.cpu_usage)}%`}>
              <p className="shrink-0 text-sm">
                CPU <span className="font-semibold">{Math.round(n.cpu_usage)}%</span>, memory <span className="font-semibold">{Math.round(n.memory_usage)}%</span>
              </p>
              {cpu.length > 1 && <Sparkline data={cpu} height={24} className="min-w-0 max-w-[7rem] flex-1" colorClass="text-[color:var(--chart-1)]" />}
            </div>
            {n.net_rx_bytes_per_sec !== null && n.net_tx_bytes_per_sec !== null && (
              <p className="text-xs text-muted-foreground">
                Traffic in {rate(n.net_rx_bytes_per_sec)}, out {rate(n.net_tx_bytes_per_sec)}
              </p>
            )}
          </>
        ) : (
          <p className="text-sm text-muted-foreground">
            {n.reported_at ? `No current reading. Last answered ${since(n.reported_at)}.` : "This node has never answered the panel."}
          </p>
        )}
      </div>

      <div className="col-span-2 flex flex-wrap items-center gap-2 xl:col-span-1 xl:justify-end">
        <Button asChild size="sm" variant="outline">
          <Link href={`/nodes/${n.id}`}>Open</Link>
        </Button>
        <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={onRemove}>
          Remove
        </Button>
      </div>
    </li>
  )
}

export default function NodesListPage() {
  const confirm = useConfirm()
  const { data: o, isLoading, error, refetch } = useDashboardOverview()
  const deleteNode = useDeleteNode()
  const [query, setQuery] = useState("")

  const nodes = o?.nodes ?? []
  const q = query.trim().toLowerCase()
  const shown = q ? nodes.filter((n) => n.name.toLowerCase().includes(q) || n.ip.includes(q) || n.region.toLowerCase().includes(q)) : nodes

  const remove = async (n: DashboardNode) => {
    const ok = await confirm({
      title: `Remove node "${n.name}"?`,
      description:
        n.vm_count > 0
          ? `The panel stops managing this hypervisor. Its ${n.vm_count} ${n.vm_count === 1 ? "VM keeps" : "VMs keep"} running, but can no longer be managed from here.`
          : "The panel stops managing this hypervisor. It has no VMs.",
      confirmLabel: "Remove node",
      destructive: true,
      action: () => deleteNode.mutateAsync(n.id),
    })
    if (!ok) return
    toast.success(`Node ${n.name} removed`)
    refetch()
  }

  const up = nodes.filter(reporting).length
  const summary =
    nodes.length === 0
      ? "No node is connected yet."
      : `${nodes.length} ${nodes.length === 1 ? "node" : "nodes"}, ${up === nodes.length ? "all answering" : `${up} answering`}. ` +
        `${nodes.reduce((a, n) => a + n.vm_count, 0)} VMs with ${nodes.reduce((a, n) => a + n.vcpu_allocated, 0)} vCPU and ` +
        `${Math.round(nodes.reduce((a, n) => a + n.ram_allocated_mb, 0) / 1024)} GB of memory promised.`

  return (
    <div className="mx-auto max-w-7xl">
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Nodes</h1>
          <p className="mt-1 text-sm text-muted-foreground">{isLoading ? "Loading the nodes" : error ? "The nodes could not be loaded." : summary}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/* A filter only earns its place once there are more nodes than fit at a glance. */}
          {nodes.length > 6 && (
            <>
              <label htmlFor="node-filter" className="sr-only">Filter nodes</label>
              <Input id="node-filter" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Filter by name, address or region" className="w-64" />
            </>
          )}
          <Button asChild variant={nodes.length === 0 && !isLoading && !error ? "default" : "outline"}>
            <Link href="/nodes/new">Add a node</Link>
          </Button>
        </div>
      </div>

      <section aria-label="Nodes" className="rounded-lg border bg-card text-card-foreground">
        {isLoading ? (
          <div className="space-y-4 p-5" aria-busy="true">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-20 w-full" />
            ))}
          </div>
        ) : error ? (
          <div className="p-8" role="alert">
            <p className="font-medium">The nodes could not be loaded.</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error).message}</p>
            <Button className="mt-4" variant="outline" onClick={() => refetch()}>
              Try again
            </Button>
          </div>
        ) : nodes.length === 0 ? (
          <div className="p-8">
            <p className="font-medium">No node is connected yet.</p>
            <p className="mt-1 text-sm text-muted-foreground">A node is a server that runs VMs. Add one and it appears here with its load and the room left on it.</p>
          </div>
        ) : shown.length === 0 ? (
          <div className="p-8">
            <p className="font-medium">No node matches &quot;{query}&quot;.</p>
            <Button className="mt-3" size="sm" variant="outline" onClick={() => setQuery("")}>
              Show all nodes
            </Button>
          </div>
        ) : (
          <ul className="divide-y divide-hair">
            {shown.map((n) => (
              <NodeRow key={n.id} n={n} onRemove={() => remove(n)} />
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
