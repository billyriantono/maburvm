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
  VMAction,
  PaginatedResponse,
} from '@/types'

interface VMListParams {
  page?: number
  pageSize?: number
  userId?: string
  nodeId?: string
  status?: string
  templateId?: string
}

export function useVMs(params: VMListParams = {}) {
  const { page = 1, pageSize = 20, userId, nodeId, status } = params

  return useQuery<PaginatedResponse<VM>>({
    queryKey: ['vms', 'list', { page, pageSize, userId, nodeId, status }],
    queryFn: () =>
      api.get<PaginatedResponse<VM>>('/api/v1/vms', {
        page,
        page_size: pageSize,
        user_id: userId,
        node_id: nodeId,
        status,
      }),
  })
}

export function useVM(id: string, options?: UseQueryOptions<VM>) {
  return useQuery<VM>({
    queryKey: ['vms', 'detail', id],
    queryFn: () => api.get<VM>(`/api/v1/vms/${id}`),
    enabled: !!id,
    ...options,
  })
}

export function useCreateVM() {
  const queryClient = useQueryClient()

  return useMutation<VM, Error, CreateVMRequest>({
    mutationFn: (data) => api.post<VM>('/api/v1/vms', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useUpdateVM(id: string) {
  const queryClient = useQueryClient()

  return useMutation<VM, Error, UpdateVMRequest>({
    mutationFn: (data) => api.put<VM>(`/api/v1/vms/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useDeleteVM() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (id) => api.delete(`/api/v1/vms/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useVMAction(id: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, VMAction>({
    mutationFn: (action) =>
      api.post(`/api/v1/vms/${id}/actions`, { action }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useVMMetrics(id: string) {
  return useQuery<VMMetrics>({
    queryKey: ['vms', 'metrics', id],
    queryFn: () => api.get<VMMetrics>(`/api/v1/vms/${id}/metrics`),
    enabled: !!id,
    refetchInterval: 5000,
  })
}
