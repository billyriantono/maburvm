'use client'

import { useDismissSetup, useSetup } from "@/lib/hooks/use-ops"
import { useBranding } from "@/lib/branding/provider"
import Link from 'next/link'
import { useDashboardStats, useDashboardOverview, type DashboardOverview, type DashboardNode } from '@/lib/hooks/use-dashboard'
import { useNodes, useNodeMetricsHistory } from '@/lib/hooks/use-nodes'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Meter } from '@/components/ui/meter'
import { StatusBadge } from '@/components/status-badge'
import { BandwidthChart } from '@/components/ui/bandwidth-chart'

function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMin = Math.floor(diffMs / 60000)
  const diffHour = Math.floor(diffMs / 3600000)
  const diffDay = Math.floor(diffMs / 86400000)

  if (diffMin < 1) return 'just now'
  if (diffMin < 60) return `${diffMin} min ago`
  if (diffHour < 24) return `${diffHour} hour${diffHour > 1 ? 's' : ''} ago`
  return `${diffDay} day${diffDay > 1 ? 's' : ''} ago`
}

// Full timestamp for the hover title, so relative time is never the only info.
function formatExact(dateStr: string): string {
  return new Date(dateStr).toLocaleString()
}

// An audit action as a verb. Covers the common VM lifecycle actions; anything
// else falls back to a prettified label.
const ACTION_VERB: Record<string, string> = {
  'vm.create': 'Created',
  'vm.delete': 'Deleted',
  'vm.start': 'Started',
  'vm.stop': 'Stopped',
  'vm.restart': 'Restarted',
  'vm.reboot': 'Rebooted',
  'vm.reset': 'Reset',
  'vm.suspend': 'Suspended',
  'vm.resume': 'Resumed',
  'vm.rebuild': 'Rebuilt',
  'vm.snapshot': 'Snapshotted',
  'vm.reset-password': 'Reset password for',
  'vm.update': 'Updated',
  'image.create': 'Captured image of',
}

const RESOURCE_LABEL: Record<string, string> = {
  vm: 'VM', node: 'node', image: 'image', backup: 'backup',
  snapshot: 'snapshot', user: 'user', network: 'network',
}

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

// Build the display parts for one activity entry.
function describeActivity(a: {
  action: string; resource_type: string; resource_name: string; resource_id: string
}): { label: string; target: string } {
  const resourceLabel = RESOURCE_LABEL[a.resource_type] ?? a.resource_type.replace(/_/g, ' ')
  // Prefer a real name; fall back to a short id so it's never empty.
  const target = a.resource_name || (a.resource_id ? a.resource_id.slice(0, 8) : '')
  const known = ACTION_VERB[a.action.toLowerCase()]
  if (known) return { label: `${known} ${resourceLabel}`, target }
  // Fallback: "vm.foo" -> "Foo vm"
  const verb = a.action.includes('.') ? a.action.split('.').slice(1).join(' ') : a.action
  return { label: `${titleCase(verb)} ${resourceLabel}`, target }
}

// formatRate renders a bytes-per-second figure at a sensible magnitude.
function formatRate(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return "0 B/s"
  const units = ["B/s", "KB/s", "MB/s", "GB/s"]
  let value = bytesPerSec
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

// NodeBandwidthCard is one node's inbound/outbound trend.
function NodeBandwidthCard({ nodeId, name }: { nodeId: string; name: string }) {
  const { data: samples, isLoading } = useNodeMetricsHistory(nodeId, 60)

  const points = (samples ?? []).map((s) => ({
    rx: s.network_rx_bytes_per_sec ?? 0,
    tx: s.network_tx_bytes_per_sec ?? 0,
  }))

  return (
    <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-semibold truncate" title={name}>{name}</h3>
      </div>
      {isLoading ? (
        <div className="h-[96px] flex items-center justify-center text-xs text-muted-foreground">
          Loading…
        </div>
      ) : (
        <BandwidthChart data={points} format={formatRate} />
      )}
    </div>
  )
}

// NodeBandwidthSection shows every node side by side.
function NodeBandwidthSection() {
  const { data: nodes, error, refetch } = useNodes()
  if (error) {
    return (
      <div className="mb-6 flex items-center gap-3 text-sm text-destructive">
        <span>Could not load node bandwidth: {(error as Error).message}</span>
        <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
      </div>
    )
  }
  const list = nodes ?? []
  if (list.length === 0) return null

  return (
    <div className="mb-6">
      <h2 className="text-base font-semibold mb-3">Traffic by node, last hour</h2>
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {list.map((node) => (
          <NodeBandwidthCard key={node.id} nodeId={node.id} name={node.name} />
        ))}
      </div>
    </div>
  )
}

// Shown until the setup list is finished or the admin hides it.
function SetupReminder() {
  const { data } = useSetup()
  const dismiss = useDismissSetup()
  if (!data || data.dismissed) return null
  const next = data.steps.find((s) => !s.done)
  if (!next) return null
  const done = data.steps.filter((s) => s.done).length
  return (
    <div className="mb-8 flex flex-wrap items-center gap-x-6 gap-y-3 rounded-lg border bg-card p-4 shadow-sm">
      <div className="min-w-0 flex-1 basis-64">
        <p className="font-medium">Setup: {done} of {data.steps.length} done. Next: {next.title.toLowerCase()}.</p>
        <p className="text-sm text-muted-foreground mt-0.5">{next.why}</p>
      </div>
      <div className="flex items-center gap-2">
        <Link href={next.href}><Button size="sm">Do this now</Button></Link>
        <Link href="/setup"><Button size="sm" variant="outline">See the list</Button></Link>
        <Button size="sm" variant="ghost" onClick={() => dismiss.mutate(true)} disabled={dismiss.isPending}>Hide</Button>
      </div>
    </div>
  )
}

const GIB = 1024 ** 3

function since(iso: string): string {
  const min = Math.floor((Date.now() - new Date(iso).getTime()) / 60_000)
  if (min < 60) return `${Math.max(min, 1)} min`
  const h = Math.floor(min / 60)
  return h < 48 ? `${h} h` : `${Math.floor(h / 24)} days`
}

interface Attention { key: string; bad: boolean; text: string; href: string; go: string }

/** A node counts as silent when it is meant to be up but has sent nothing for 5 minutes. */
function silentFor(n: DashboardNode): string | null {
  if (n.status !== 'active') return null
  if (!n.reported_at) return 'has never reported'
  return Date.now() - new Date(n.reported_at).getTime() > 5 * 60_000 ? `has not reported for ${since(n.reported_at)}` : null
}

/** Everything an operator should look at, most serious first. Built only from
 *  what the panel has recorded; an empty list means none of these is true. */
function attentionList(o: DashboardOverview): Attention[] {
  const rows: Attention[] = []
  for (const n of o.nodes) {
    const href = `/nodes/${n.id}`
    if (n.status === 'offline') rows.push({ key: `off-${n.id}`, bad: true, text: `Node ${n.name} is offline${n.vm_count ? `, with ${n.vm_count} ${n.vm_count === 1 ? 'VM' : 'VMs'} on it` : ''}.`, href, go: 'Open node' })
    else if (n.status === 'maintenance') rows.push({ key: `mnt-${n.id}`, bad: false, text: `Node ${n.name} is in maintenance.`, href, go: 'Open node' })
    const silent = silentFor(n)
    if (silent) rows.push({ key: `sil-${n.id}`, bad: true, text: `Node ${n.name} ${silent}.`, href, go: 'Open node' })
    if (n.conntrack_count && n.conntrack_max && n.conntrack_count / n.conntrack_max >= 0.8)
      rows.push({ key: `ct-${n.id}`, bad: true, text: `Node ${n.name}'s connection table is ${Math.round((n.conntrack_count / n.conntrack_max) * 100)}% full. A VM on it may be flooding.`, href: '/abuse', go: 'Open abuse' })
  }
  for (const v of o.vms_stuck)
    rows.push(v.status === 'error'
      ? { key: `err-${v.id}`, bad: true, text: `${v.hostname} failed ${since(v.updated_at)} ago.`, href: `/vms/${v.id}?tab=logs`, go: 'Open VM' }
      : { key: `stk-${v.id}`, bad: false, text: `${v.hostname} has been ${v.status} for ${since(v.updated_at)}.`, href: `/vms/${v.id}`, go: 'Open VM' })
  for (const v of o.vms_backup_failed)
    rows.push({ key: `bk-${v.id}`, bad: true, text: `A backup of ${v.hostname} failed ${since(v.updated_at)} ago.`, href: `/vms/${v.id}?tab=backups`, go: 'Open backups' })
  for (const s of o.storage_over_alert)
    rows.push({ key: `st-${s.node}-${s.name}`, bad: false, text: `Storage ${s.name} on ${s.node} is ${Math.round((s.used_bytes / s.total_bytes) * 100)}% full; its alert level is ${s.alert_threshold}%.`, href: '/storage', go: 'Open storage' })
  for (const p of o.address_pools) {
    const free = p.total - p.used
    if (p.total > 0 && p.used / p.total >= 0.9)
      rows.push({ key: `ip-${p.id}`, bad: free === 0, text: free === 0 ? `Address range ${p.name} is full. New VMs cannot get an address from it.` : `Address range ${p.name} has ${free} of ${p.total} addresses left.`, href: '/ip-pools', go: 'Open ranges' })
  }
  for (const v of o.vms_over_transfer)
    rows.push({ key: `tr-${v.id}`, bad: false, text: `${v.hostname} is over its monthly transfer limit.`, href: `/vms/${v.id}?tab=network`, go: 'Open VM' })
  if (o.failed_jobs_24h > 0)
    rows.push({ key: 'jobs', bad: false, text: `${o.failed_jobs_24h} background ${o.failed_jobs_24h === 1 ? 'job' : 'jobs'} gave up in the last 24 hours.`, href: '/settings/queue', go: 'Open job queue' })
  return rows.sort((a, b) => Number(b.bad) - Number(a.bad))
}

// One node: what it has been promised to VMs against what it physically has.
function NodeRow({ n }: { n: DashboardNode }) {
  const ramTotal = n.memory_total_bytes / GIB
  const ramPromised = n.ram_allocated_mb / 1024
  const ramRatio = ramTotal > 0 ? ramPromised / ramTotal : null
  const diskShare = n.disk_total_bytes > 0 ? (n.disk_used_bytes / n.disk_total_bytes) * 100 : null
  const reporting = n.status === 'active' && !silentFor(n)
  return (
    <li className="grid grid-cols-2 gap-x-6 gap-y-4 px-5 py-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,0.7fr)_minmax(0,1.3fr)_minmax(0,1.3fr)_minmax(0,1fr)] lg:items-center">
      <div className="col-span-2 min-w-0 lg:col-span-1">
        <Link href={`/nodes/${n.id}`} className="break-all text-base font-semibold underline-offset-4 hover:underline [font-stretch:112%]">{n.name}</Link>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
          <StatusBadge variant="lamp" className="text-xs" status={n.status} />
          {n.region && <span className="text-xs text-muted-foreground">{n.region}</span>}
          {n.creation_paused && <span className="text-xs font-medium text-muted-foreground">Closed to new VMs</span>}
        </div>
      </div>
      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">VMs</p>
        <p className="text-sm"><span className="font-semibold">{n.running}</span> running of {n.vm_count}</p>
        <p className="text-xs text-muted-foreground">{n.vcpu_allocated} vCPU promised</p>
      </div>
      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">Memory promised to VMs</p>
        {ramRatio === null ? (
          <p className="text-sm text-muted-foreground">{ramPromised.toFixed(0)} GB; this node has not reported its memory</p>
        ) : (
          <>
            <p className="text-sm"><span className="font-semibold">{ramPromised.toFixed(0)} GB</span> of {ramTotal.toFixed(0)} GB installed{ramRatio > 1 ? ` (${ramRatio.toFixed(1)} times)` : ''}</p>
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
            <p className="text-sm"><span className="font-semibold">{(n.disk_used_bytes / GIB).toFixed(0)} GB</span> of {(n.disk_total_bytes / GIB).toFixed(0)} GB ({Math.round(diskShare)}%)</p>
            <Meter percent={diskShare} over={diskShare >= 90} />
          </>
        )}
      </div>
      <div className="col-span-2 min-w-0 lg:col-span-1">
        <p className="text-xs text-muted-foreground">Load now</p>
        {reporting && n.cpu_usage !== null && n.memory_usage !== null ? (
          <p className="text-sm">CPU <span className="font-semibold">{Math.round(n.cpu_usage)}%</span>, memory <span className="font-semibold">{Math.round(n.memory_usage)}%</span></p>
        ) : (
          <p className="text-sm text-muted-foreground">No current reading</p>
        )}
      </div>
    </li>
  )
}

export default function DashboardPage() {
  const { branding } = useBranding()
  const { data: stats } = useDashboardStats()
  const { data: o, isLoading, error, refetch } = useDashboardOverview()

  const head = (sub: React.ReactNode) => (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold">{branding.name} today</h1>
        <p className="mt-1 text-sm text-muted-foreground">{sub}</p>
      </div>
      <Button asChild variant="outline"><Link href="/vms/new">New VM</Link></Button>
    </div>
  )

  if (isLoading) {
    return (
      <div className="mx-auto max-w-6xl" aria-busy="true">
        {head('Loading the fleet')}
        <div className="space-y-4">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-24 w-full" />)}</div>
      </div>
    )
  }
  if (error || !o) {
    return (
      <div className="mx-auto max-w-6xl" role="alert">
        {head('The fleet overview could not be loaded.')}
        <p className="mb-4 text-sm text-muted-foreground">{(error as Error)?.message}</p>
        <Button variant="outline" onClick={() => refetch()}>Try again</Button>
      </div>
    )
  }

  const t = o.totals
  const needs = attentionList(o)
  const summary = t.vms === 0
    ? `No VMs yet, on ${o.nodes.length} ${o.nodes.length === 1 ? 'node' : 'nodes'}.`
    : `${t.vms} ${t.vms === 1 ? 'VM' : 'VMs'} on ${o.nodes.length} ${o.nodes.length === 1 ? 'node' : 'nodes'}: ${t.running} running, ${t.stopped} stopped${t.error ? `, ${t.error} failed` : ''}${t.suspended ? `, ${t.suspended} suspended` : ''}. ${t.customers} ${t.customers === 1 ? 'customer' : 'customers'}, ${t.new_7d} new ${t.new_7d === 1 ? 'VM' : 'VMs'} this week.`

  return (
    <div className="mx-auto max-w-6xl">
      {head(summary)}

      <SetupReminder />

      <section aria-labelledby="needs-attention" className="mb-6 rounded-lg border bg-card text-card-foreground">
        <h2 id="needs-attention" className="border-b border-hair px-5 py-3 text-sm font-semibold">
          {needs.length ? `Needs attention (${needs.length})` : 'Needs attention'}
        </h2>
        {needs.length === 0 ? (
          <p className="px-5 py-4 text-sm text-muted-foreground">
            Nothing right now. Every node is reporting, no VM has failed or stalled, no backup or job failed in the last 24 hours, and no storage or address range is nearly full.
          </p>
        ) : (
          <ul className="divide-y divide-hair">
            {needs.map((n) => (
              <li key={n.key} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3 text-sm">
                <span className={`h-2.5 w-2.5 shrink-0 rounded-full ${n.bad ? 'bg-destructive' : 'bg-warning'}`} aria-hidden="true" />
                <span className="min-w-0 flex-1 basis-64">{n.text}</span>
                <Link href={n.href} className="font-semibold underline underline-offset-4">{n.go}</Link>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-labelledby="nodes-capacity" className="mb-6 rounded-lg border bg-card text-card-foreground">
        <h2 id="nodes-capacity" className="border-b border-hair px-5 py-3 text-sm font-semibold">Room on each node</h2>
        {o.nodes.length === 0 ? (
          <div className="px-5 py-4 text-sm">
            <p className="text-muted-foreground">No node is connected yet, so no VM can be created.</p>
            <Button asChild className="mt-3" size="sm"><Link href="/nodes/new">Add a node</Link></Button>
          </div>
        ) : (
          <ul className="divide-y divide-hair">{o.nodes.map((n) => <NodeRow key={n.id} n={n} />)}</ul>
        )}
      </section>

      {o.address_pools.length > 0 && (
        <section aria-labelledby="address-ranges" className="mb-6 rounded-lg border bg-card text-card-foreground">
          <h2 id="address-ranges" className="border-b border-hair px-5 py-3 text-sm font-semibold">IPv4 addresses</h2>
          <ul className="grid gap-x-8 gap-y-4 px-5 py-4 sm:grid-cols-2 lg:grid-cols-3">
            {o.address_pools.map((p) => (
              <li key={p.id} className="min-w-0">
                <p className="truncate text-xs text-muted-foreground">{p.name}</p>
                {p.total === 0 ? (
                  <p className="text-sm text-muted-foreground">No addresses in this range</p>
                ) : (
                  <>
                    <p className="text-sm"><span className="font-semibold">{p.total - p.used}</span> free of {p.total}</p>
                    <Meter percent={(p.used / p.total) * 100} over={p.used / p.total >= 0.9} />
                  </>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      {/* Per-node traffic as small multiples: the question is which node is
          carrying what, and one shared chart would hide each node's own shape. */}
      <NodeBandwidthSection />

      <section aria-labelledby="recent-activity" className="rounded-lg border bg-card text-card-foreground">
        <div className="flex items-center justify-between border-b border-hair px-5 py-3">
          <h2 id="recent-activity" className="text-sm font-semibold">Recent activity</h2>
          <Link href="/audit-logs" className="text-sm font-semibold underline underline-offset-4">Audit log</Link>
        </div>
        {stats?.recent_activity && stats.recent_activity.length > 0 ? (
          <ul className="divide-y divide-hair">
            {stats.recent_activity.map((activity) => {
              const { label, target } = describeActivity(activity)
              return (
                <li key={activity.id} className="flex flex-wrap items-baseline gap-x-4 gap-y-0.5 px-5 py-2.5 text-sm">
                  <span className="min-w-0 flex-1 basis-56">{label}{target && <span className="font-semibold"> {target}</span>}</span>
                  <span className="text-xs text-muted-foreground" title={formatExact(activity.created_at)}>{activity.actor}, {formatTimeAgo(activity.created_at)}</span>
                </li>
              )
            })}
          </ul>
        ) : (
          <p className="px-5 py-4 text-sm text-muted-foreground">Nothing recorded yet. Actions on VMs appear here as they happen.</p>
        )}
      </section>
    </div>
  )
}
