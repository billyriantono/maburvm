import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  Node,
  NodeMetrics,
  NodeMetricSample,
  CreateNodeRequest,
  UpdateNodeRequest,
} from '@/types'

export function useNodes() {
  return useQuery<Node[]>({
    queryKey: ['nodes', 'list'],
    queryFn: async () => {
      const response = await api.get<Node[]>('/api/v1/nodes')
      return response.data.data
    },
  })
}

export function useNode(id: string, options?: UseQueryOptions<Node>) {
  return useQuery<Node>({
    queryKey: ['nodes', 'detail', id],
    queryFn: async () => {
      const response = await api.get<Node>(`/api/v1/nodes/${id}`)
      return response.data.data
    },
    enabled: !!id,
    ...options,
  })
}

export function useNodeMetrics(id: string) {
  return useQuery<NodeMetrics>({
    queryKey: ['nodes', 'metrics', id],
    queryFn: async () => {
      const response = await api.get<NodeMetrics>(`/api/v1/nodes/${id}/metrics`)
      return response.data.data
    },
    enabled: !!id,
    refetchInterval: 10000,
  })
}

// useNodeMetricsHistory returns persisted samples for the trailing `minutes` window.
export function useNodeMetricsHistory(id: string, minutes = 60) {
  return useQuery<NodeMetricSample[]>({
    queryKey: ['nodes', 'metrics-history', id, minutes],
    queryFn: async () => {
      const response = await api.get<NodeMetricSample[]>(`/api/v1/nodes/${id}/metrics/history`, {
        params: { minutes },
      })
      return response.data.data
    },
    enabled: !!id,
    refetchInterval: 30000,
  })
}

export function useCreateNode() {
  const queryClient = useQueryClient()

  return useMutation<Node, Error, CreateNodeRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Node>('/api/v1/nodes', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['nodes', 'list'] })
    },
  })
}

export function useUpdateNode(id: string) {
  const queryClient = useQueryClient()

  return useMutation<Node, Error, UpdateNodeRequest>({
    mutationFn: async (data) => {
      const response = await api.put<Node>(`/api/v1/nodes/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['nodes', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['nodes', 'list'] })
    },
  })
}

export function useDeleteNode() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/nodes/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['nodes', 'list'] })
    },
  })
}

export function useNodeToken(id: string) {
  return useQuery<{ token: string }>({
    queryKey: ['nodes', 'token', id],
    queryFn: async () => {
      const response = await api.get<{ token: string }>(`/api/v1/nodes/${id}/token`)
      return response.data.data
    },
    enabled: false, // Only fetch on demand
  })
}

export function useRegenerateNodeToken(id: string) {
  const queryClient = useQueryClient()

  return useMutation<{ token: string }, Error>({
    mutationFn: async () => {
      const response = await api.post<{ token: string }>(`/api/v1/nodes/${id}/regenerate-token`)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['nodes', 'token', id] })
    },
  })
}

export interface ScannedVM {
  name: string
  uuid: string
  hostname: string
  cpu: number
  memory_mb: number
  vnc_port: number
  status: string
  disks: { source_file: string; format: string; device: string }[]
  networks: { mac_address: string; bridge: string; model: string; ip_address: string }[]
  xml_path: string
  conflicts?: boolean
  conflict_reason?: string
}

export function useScanVMs(nodeId: string) {
  return useQuery<{ node_id: string; total_found: number; vms: ScannedVM[] }>({
    queryKey: ['nodes', 'scan-vms', nodeId],
    queryFn: async () => {
      const response = await api.get<{ node_id: string; total_found: number; vms: ScannedVM[] }>(`/api/v1/nodes/${nodeId}/import/virtualizor/preview`)
      return response.data.data
    },
    enabled: false, // Only fetch on demand
  })
}

export function useSyncNodeVMs(nodeId: string) {
  return useMutation<{ message: string; data: { total: number; updated: number; unchanged: number; skipped: number; errors: number; results: Array<{ uuid: string; name: string; hostname: string; status: string; message?: string }> } }>({
    mutationFn: async () => {
      const response = await api.post<{ total: number; updated: number; unchanged: number; skipped: number; errors: number; results: Array<{ uuid: string; name: string; hostname: string; status: string; message?: string }> }>(`/api/v1/nodes/${nodeId}/import/sync`)
      return { message: response.data.message || 'Sync completed', data: response.data.data }
    },
  })
}

export function useImportVMs(nodeId: string) {
  return useMutation<{ message: string; success_count: number; skipped_count: number; error_count: number }, Error, { vm_uuids: string[]; user_id: string; os_template_id: string }>({
    mutationFn: async (data) => {
      const response = await api.post<{ message: string; success_count: number; skipped_count: number; error_count: number }>(`/api/v1/nodes/${nodeId}/import/virtualizor`, data)
      return response.data.data
    },
  })
}
