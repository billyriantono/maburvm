import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

export interface BandwidthUsage {
  vm_id: string
  node_id: string
  period_start: string
  period_end: string
  rx_bytes: number
  tx_bytes: number
  total_bytes: number
  used_gb: number
  quota_gb: number
  usage_percent: number
  exceeded: boolean
  blocked_at?: string
}

export interface BandwidthHistoryResponse {
  vm_id: string
  history: BandwidthUsage[]
}

export function useVMBandwidth(vmId: string) {
  return useQuery<BandwidthUsage>({
    queryKey: ['vm-bandwidth', vmId],
    queryFn: async () => {
      const { data } = await api.get<BandwidthUsage>(`/api/v1/vms/${vmId}/bandwidth`)
      return data.data
    },
    enabled: !!vmId,
    refetchInterval: 30000, // Refresh every 30s (matches heartbeat interval)
  })
}

export function useVMBandwidthHistory(vmId: string) {
  return useQuery<BandwidthHistoryResponse>({
    queryKey: ['vm-bandwidth-history', vmId],
    queryFn: async () => {
      const { data } = await api.get<BandwidthHistoryResponse>(`/api/v1/vms/${vmId}/bandwidth/history`)
      return data.data
    },
    enabled: !!vmId,
  })
}

// useSetBandwidthQuota sets a VM's monthly bandwidth quota in GB (0 = unlimited).
export function useSetBandwidthQuota(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, number>({
    mutationFn: async (quotaGB) => {
      await api.put(`/api/v1/vms/${vmId}/bandwidth/quota`, { quota_gb: quotaGB })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vm-bandwidth', vmId] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', vmId] })
    },
  })
}

// Helper to format bytes to human-readable
export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`
}
