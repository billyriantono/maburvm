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
    user_id: string | null
    action: string
    resource_type: string
    resource_id: string | null
    ip_address: string
    details: Record<string, unknown>
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
