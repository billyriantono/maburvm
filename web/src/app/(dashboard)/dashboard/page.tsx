'use client'

import Link from 'next/link'
import { useDashboardStats } from '@/lib/hooks/use-dashboard'

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

function formatAction(log: { action: string; resource_type: string; resource_id: string | null }): string {
  const action = log.action.toLowerCase()
  const resource = log.resource_type.replace(/_/g, ' ').toLowerCase()
  const id = log.resource_id ? ` '${log.resource_id.slice(0, 8)}...'` : ''
  return `${action} ${resource}${id}`
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

      {/* Main Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Recent Activity */}
        <div className="lg:col-span-2 rounded-lg border bg-card text-card-foreground shadow-sm p-6">
          <h2 className="text-lg font-semibold tracking-tight mb-4">
            Recent Activity
          </h2>
          <div className="space-y-4">
            {stats?.recent_activity && stats.recent_activity.length > 0 ? (
              stats.recent_activity.map((activity) => (
                <div key={activity.id} className="flex items-start gap-4 pb-4 border-b last:border-0">
                  <div className="w-2 h-2 rounded-full bg-muted-foreground mt-2 shrink-0"></div>
                  <div className="flex-1">
                    <p className="text-sm font-medium">{formatAction(activity)}</p>
                    <p className="text-xs text-muted-foreground">
                      <span className="font-medium">{activity.user_id ? 'user' : 'system'}</span> • {formatTimeAgo(activity.created_at)}
                    </p>
                  </div>
                </div>
              ))
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
