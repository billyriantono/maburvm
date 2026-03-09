import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  VM,
  VMMetrics,
  CreateVMRequest,
  UpdateVMRequest,
  PaginatedResponse,
} from '@/types'

interface VMListParams {
  page?: number
  pageSize?: number
  userId?: string
  nodeId?: string
  status?: string;
}

export function useVMs(params: VMListParams = {}) {
  const { page = 1, pageSize = 20, userId, nodeId, status } = params

  return useQuery<PaginatedResponse<VM>>({
    queryKey: ['vms', 'list', { page, pageSize, userId, nodeId, status }],
    queryFn: async () => {
      const response = await api.get<PaginatedResponse<VM>>('/api/v1/vms', {
        params: {
          page,
          page_size: pageSize,
          user_id: userId,
          node_id: nodeId,
          status,
        },
      })
      return response.data.data
    },
  })
}

export function useVM(id: string, options?: UseQueryOptions<VM>) {
  return useQuery<VM>({
    queryKey: ['vms', 'detail', id],
    queryFn: async () => {
      const response = await api.get<VM>(`/api/v1/vms/${id}`)
      return response.data.data
    },
    enabled: !!id,
    ...options,
  })
}

export function useCreateVM() {
  const queryClient = useQueryClient()

  return useMutation<VM, Error, CreateVMRequest>({
    mutationFn: async (data) => {
      const response = await api.post<VM>('/api/v1/vms', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useUpdateVM(id: string) {
  const queryClient = useQueryClient()

  return useMutation<VM, Error, UpdateVMRequest>({
    mutationFn: async (data) => {
      const response = await api.put<VM>(`/api/v1/vms/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useDeleteVM() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vms/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useVMAction(id: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (action) => {
      await api.post(`/api/v1/vms/${id}/actions`, { action })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// Version that accepts vmId + action together (for list pages)
export function useVMActions() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, { vmId: string; action: string }>({
    mutationFn: async ({ vmId, action }) => {
      await api.post(`/api/v1/vms/${vmId}/actions`, { action })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useVMMetrics(id: string) {
  return useQuery<VMMetrics>({
    queryKey: ['vms', 'metrics', id],
    queryFn: async () => {
      const response = await api.get<VMMetrics>(`/api/v1/vms/${id}/metrics`)
      return response.data.data
    },
    enabled: !!id,
    refetchInterval: 5000,
  })
}
