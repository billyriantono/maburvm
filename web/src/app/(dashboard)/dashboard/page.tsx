'use client'

import Link from 'next/link'
import {
  Plus, Trash2, Play, Square, RotateCw, RefreshCw, Pause, Camera,
  HardDriveDownload, KeyRound, Pencil, Activity as ActivityIcon,
} from 'lucide-react'
import { useDashboardStats } from '@/lib/hooks/use-dashboard'
import { useNodes, useNodeMetricsHistory } from '@/lib/hooks/use-nodes'
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

type IconType = typeof Plus
type Tone = 'green' | 'red' | 'amber' | 'blue' | 'violet' | 'muted'

// Static class strings per tone (Tailwind can't see dynamically-built class names,
// so these must be full literals to be generated).
const TONES: Record<Tone, { bg: string; text: string }> = {
  green: { bg: 'bg-emerald-500/10', text: 'text-emerald-500' },
  red: { bg: 'bg-red-500/10', text: 'text-red-500' },
  amber: { bg: 'bg-amber-500/10', text: 'text-amber-500' },
  blue: { bg: 'bg-blue-500/10', text: 'text-blue-500' },
  violet: { bg: 'bg-violet-500/10', text: 'text-violet-500' },
  muted: { bg: 'bg-muted', text: 'text-muted-foreground' },
}

// Map an audit action to a human verb, an icon, and a tone. Covers the common VM
// lifecycle actions; anything else falls back to a prettified label.
const ACTION_META: Record<string, { verb: string; icon: IconType; tone: Tone }> = {
  'vm.create': { verb: 'Created', icon: Plus, tone: 'green' },
  'vm.delete': { verb: 'Deleted', icon: Trash2, tone: 'red' },
  'vm.start': { verb: 'Started', icon: Play, tone: 'green' },
  'vm.stop': { verb: 'Stopped', icon: Square, tone: 'amber' },
  'vm.restart': { verb: 'Restarted', icon: RotateCw, tone: 'blue' },
  'vm.reboot': { verb: 'Rebooted', icon: RotateCw, tone: 'blue' },
  'vm.reset': { verb: 'Reset', icon: RotateCw, tone: 'blue' },
  'vm.suspend': { verb: 'Suspended', icon: Pause, tone: 'amber' },
  'vm.resume': { verb: 'Resumed', icon: Play, tone: 'green' },
  'vm.rebuild': { verb: 'Rebuilt', icon: RefreshCw, tone: 'blue' },
  'vm.snapshot': { verb: 'Snapshotted', icon: Camera, tone: 'violet' },
  'vm.reset-password': { verb: 'Reset password for', icon: KeyRound, tone: 'amber' },
  'vm.update': { verb: 'Updated', icon: Pencil, tone: 'blue' },
  'image.create': { verb: 'Captured image of', icon: HardDriveDownload, tone: 'violet' },
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
}): { icon: IconType; tone: Tone; label: string; target: string } {
  const meta = ACTION_META[a.action.toLowerCase()]
  const resourceLabel = RESOURCE_LABEL[a.resource_type] ?? a.resource_type.replace(/_/g, ' ')
  // Prefer a real name; fall back to a short id so it's never empty.
  const target = a.resource_name || (a.resource_id ? `${a.resource_id.slice(0, 8)}…` : '')

  if (meta) {
    return { icon: meta.icon, tone: meta.tone, label: `${meta.verb} ${resourceLabel}`, target }
  }
  // Fallback: "vm.foo" -> "Foo vm"
  const verb = a.action.includes('.') ? a.action.split('.').slice(1).join(' ') : a.action
  return { icon: ActivityIcon, tone: 'muted', label: `${titleCase(verb)} ${resourceLabel}`, target }
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
        <span className="text-[10px] font-medium text-muted-foreground">last 60m</span>
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
  const { data: nodes } = useNodes()
  const list = nodes ?? []
  if (list.length === 0) return null

  return (
    <div className="mb-6">
      <h2 className="text-lg font-semibold tracking-tight mb-3">Bandwidth by node</h2>
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {list.map((node) => (
          <NodeBandwidthCard key={node.id} nodeId={node.id} name={node.name} />
        ))}
      </div>
    </div>
  )
}

export default function DashboardPage() {
  const { data: stats, isLoading, error } = useDashboardStats()

  if (isLoading) {
    return (
      <div className="max-w-6xl mx-auto">
        <h1 className="text-3xl font-bold tracking-tight mb-2">
          Dashboard
        </h1>
        <p className="text-muted-foreground text-sm mb-8">
          Loading...
        </p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-6xl mx-auto">
        <h1 className="text-3xl font-bold tracking-tight mb-2">
          Dashboard
        </h1>
        <p className="text-destructive text-sm mb-8">
          Failed to load dashboard data. Please try again.
        </p>
      </div>
    )
  }

  const utilization = stats ? Math.round(stats.utilization) : 0

  return (
    <div className="max-w-6xl mx-auto">
      <h1 className="text-3xl font-bold tracking-tight mb-2">
        Dashboard
      </h1>
      <p className="text-muted-foreground text-sm mb-8">
        Welcome to MaburVM
      </p>

      {/* Quick Stats */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Total VMs</span>
            <span className="w-2 h-2 rounded-full bg-emerald-500"></span>
          </div>
          <p className="text-4xl font-bold">{stats?.vms.total ?? 0}</p>
          <p className="text-xs text-muted-foreground mt-1">{stats?.vms.running ?? 0} running</p>
        </div>

        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Active Nodes</span>
            <span className="w-2 h-2 rounded-full bg-primary"></span>
          </div>
          <p className="text-4xl font-bold">{stats?.nodes.active ?? 0}</p>
          <p className="text-xs text-muted-foreground mt-1">of {stats?.nodes.total ?? 0} total</p>
        </div>

        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Running</span>
            <span className="w-2 h-2 rounded-full bg-primary"></span>
          </div>
          <p className="text-4xl font-bold">{stats?.vms.running ?? 0}</p>
          <p className="text-xs text-muted-foreground mt-1">{utilization}% utilization</p>
        </div>

        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Alerts</span>
            <span className="w-2 h-2 rounded-full bg-destructive"></span>
          </div>
          <p className="text-4xl font-bold">{stats?.alerts ?? 0}</p>
          <p className="text-xs text-muted-foreground mt-1">
            {(stats?.alerts ?? 0) > 0 ? 'requires attention' : 'all clear'}
          </p>
        </div>
      </div>

      {/* Per-node bandwidth. Small multiples rather than one combined chart:
          the question is "which node is carrying what", and stacking several
          nodes onto shared axes answers a different one while making each
          node's own shape harder to read. */}
      <NodeBandwidthSection />

      {/* Main Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Recent Activity */}
        <div className="lg:col-span-2 rounded-lg border bg-card text-card-foreground shadow-sm p-6">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-lg font-semibold tracking-tight">Recent Activity</h2>
            <Link href="/audit-logs" className="text-xs font-medium text-primary hover:underline">
              View all
            </Link>
          </div>
          <div className="space-y-1">
            {stats?.recent_activity && stats.recent_activity.length > 0 ? (
              stats.recent_activity.map((activity) => {
                const { icon: Icon, tone, label, target } = describeActivity(activity)
                return (
                  <div key={activity.id} className="flex items-start gap-3 py-2.5 border-b last:border-0">
                    <div className={`mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full ${TONES[tone].bg}`}>
                      <Icon className={`h-3.5 w-3.5 ${TONES[tone].text}`} />
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm leading-snug">
                        <span className="font-medium">{label}</span>
                        {target && <span className="font-semibold text-foreground"> {target}</span>}
                      </p>
                      <p className="text-xs text-muted-foreground" title={formatExact(activity.created_at)}>
                        {activity.actor} • {formatTimeAgo(activity.created_at)}
                      </p>
                    </div>
                  </div>
                )
              })
            ) : (
              <p className="text-sm text-muted-foreground">No recent activity</p>
            )}
          </div>
        </div>

        {/* Quick Actions */}
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-6">
          <h2 className="text-lg font-semibold tracking-tight mb-4">
            Quick Actions
          </h2>
          <div className="space-y-3">
            <Link href="/vms/new" className="block w-full p-3 text-left rounded-md border bg-background hover:bg-muted transition-colors">
              <span className="text-sm font-medium">+ New VM</span>
            </Link>
            <Link href="/nodes/new" className="block w-full p-3 text-left rounded-md border bg-background hover:bg-muted transition-colors">
              <span className="text-sm font-medium">Add Node</span>
            </Link>
            <Link href="/templates/new" className="block w-full p-3 text-left rounded-md border bg-background hover:bg-muted transition-colors">
              <span className="text-sm font-medium">Create Template</span>
            </Link>
            <Link href="/networks" className="block w-full p-3 text-left rounded-md border bg-background hover:bg-muted transition-colors">
              <span className="text-sm font-medium">View Network</span>
            </Link>
          </div>
        </div>
      </div>
    </div>
  )
}
