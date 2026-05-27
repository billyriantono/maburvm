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
        <h1 className="text-3xl font-black uppercase tracking-tight text-black mb-2">
          Dashboard
        </h1>
        <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mb-8">
          Loading...
        </p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-6xl mx-auto">
        <h1 className="text-3xl font-black uppercase tracking-tight text-black mb-2">
          Dashboard
        </h1>
        <p className="text-red-500 font-medium text-sm mb-8">
          Failed to load dashboard data. Please try again.
        </p>
      </div>
    )
  }

  const utilization = stats ? Math.round(stats.utilization) : 0

  return (
    <div className="max-w-6xl mx-auto">
      <h1 className="text-3xl font-black uppercase tracking-tight text-black mb-2">
        Dashboard
      </h1>
      <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mb-8">
        Welcome to MaburVM
      </p>
      
      {/* Quick Stats */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total VMs</span>
            <span className="w-3 h-3 bg-success"></span>
          </div>
          <p className="text-4xl font-black text-black">{stats?.vms.total ?? 0}</p>
          <p className="text-xs text-gray-500 mt-1">{stats?.vms.running ?? 0} running</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Active Nodes</span>
            <span className="w-3 h-3 bg-primary"></span>
          </div>
          <p className="text-4xl font-black text-black">{stats?.nodes.active ?? 0}</p>
          <p className="text-xs text-gray-500 mt-1">of {stats?.nodes.total ?? 0} total</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Running</span>
            <span className="w-3 h-3 bg-secondary"></span>
          </div>
          <p className="text-4xl font-black text-black">{stats?.vms.running ?? 0}</p>
          <p className="text-xs text-gray-500 mt-1">{utilization}% utilization</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Alerts</span>
            <span className="w-3 h-3 bg-danger"></span>
          </div>
          <p className="text-4xl font-black text-black">{stats?.alerts ?? 0}</p>
          <p className="text-xs text-gray-500 mt-1">
            {(stats?.alerts ?? 0) > 0 ? 'requires attention' : 'all clear'}
          </p>
        </div>
      </div>
      
      {/* Main Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Recent Activity */}
        <div className="lg:col-span-2 bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
            Recent Activity
          </h2>
          <div className="space-y-4">
            {stats?.recent_activity && stats.recent_activity.length > 0 ? (
              stats.recent_activity.map((activity) => (
                <div key={activity.id} className="flex items-start gap-4 pb-4 border-b border-black last:border-0">
                  <div className="w-2 h-2 bg-black mt-2 shrink-0"></div>
                  <div className="flex-1">
                    <p className="text-sm font-bold">{formatAction(activity)}</p>
                    <p className="text-xs text-gray-500">
                      <span className="font-medium">{activity.user_id ? 'user' : 'system'}</span> • {formatTimeAgo(activity.created_at)}
                    </p>
                  </div>
                </div>
              ))
            ) : (
              <p className="text-sm text-gray-500">No recent activity</p>
            )}
          </div>
        </div>
        
        {/* Quick Actions */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
            Quick Actions
          </h2>
          <div className="space-y-3">
            <Link href="/vms/new" className="block w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">+ New VM</span>
            </Link>
            <Link href="/nodes/new" className="block w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">Add Node</span>
            </Link>
            <Link href="/templates/new" className="block w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">Create Template</span>
            </Link>
            <Link href="/networks" className="block w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">View Network</span>
            </Link>
          </div>
        </div>
      </div>
    </div>
  )
}
