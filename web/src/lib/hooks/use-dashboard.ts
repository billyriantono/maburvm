import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

export interface DashboardStats {
  vms: {
    total: number
    running: number
    stopped: number
    error: number
  }
  nodes: {
    total: number
    active: number
  }
  utilization: number
  alerts: number
  recent_activity: {
    id: string
    action: string
    actor: string
    resource_type: string
    resource_name: string
    resource_id: string
    created_at: string
  }[]
}

export function useDashboardStats() {
  return useQuery<DashboardStats>({
    queryKey: ['dashboard', 'stats'],
    queryFn: async () => {
      const response = await api.get<DashboardStats>('/api/v1/dashboard/stats')
      return response.data.data
    },
    refetchInterval: 30000,
  })
}

// --- Fleet overview (staff) ---

export interface DashboardVMRef {
  id: string
  hostname: string
  status: string
  updated_at: string
}

/** One node's allocation against what it physically has. "Allocated" is what
 *  VMs were promised, not what they are using right now. */
export interface DashboardNode {
  id: string
  name: string
  status: 'active' | 'maintenance' | 'offline'
  creation_paused: boolean
  region: string
  memory_total_bytes: number
  vm_count: number
  running: number
  vcpu_allocated: number
  ram_allocated_mb: number
  disk_allocated_gb: number
  disk_total_bytes: number
  disk_used_bytes: number
  cpu_usage: number | null
  memory_usage: number | null
  reported_at: string | null
  conntrack_count: number | null
  conntrack_max: number | null
  ip: string
  net_rx_bytes_per_sec: number | null
  net_tx_bytes_per_sec: number | null
}

export interface DashboardOverview {
  totals: { vms: number; running: number; stopped: number; error: number; suspended: number; new_7d: number; customers: number }
  nodes: DashboardNode[]
  address_pools: { id: string; name: string; total: number; used: number }[]
  storage_over_alert: { name: string; node: string; total_bytes: number; used_bytes: number; alert_threshold: number }[]
  vms_stuck: DashboardVMRef[]
  vms_over_transfer: DashboardVMRef[]
  vms_backup_failed: DashboardVMRef[]
  failed_jobs_24h: number
}

export function useDashboardOverview() {
  return useQuery<DashboardOverview>({
    queryKey: ['dashboard', 'overview'],
    queryFn: async () => {
      const response = await api.get<DashboardOverview>('/api/v1/dashboard/overview')
      return response.data.data
    },
    refetchInterval: 30000,
  })
}

// --- The signed-in person's own VMs, with their health ---

export interface MyVM {
  id: string
  hostname: string
  status: string
  suspended: boolean
  cpu: number
  ram_mb: number
  disk_gb: number
  region_name: string
  region_country: string
  ip: string | null
  /** null when nothing has been recorded this month. */
  transfer_bytes: number | null
  /** 0 means no limit. */
  transfer_quota_gb: number
  transfer_exceeded: boolean | null
  transfer_period_end: string | null
  last_backup_status: string | null
  last_backup_at: string | null
  /** CPU % over the last hour, one point a minute, oldest first. Empty while off. */
  cpu_series: number[]
  memory_now: number | null
}

export function useMyDashboard() {
  return useQuery<{ vms: MyVM[] }>({
    // Under the VM list key, which the live status stream refreshes when a VM changes state.
    queryKey: ['vms', 'list', 'dashboard-mine'],
    queryFn: async () => {
      const response = await api.get<{ vms: MyVM[] }>('/api/v1/dashboard/mine')
      return response.data.data
    },
    refetchInterval: 30000,
  })
}
