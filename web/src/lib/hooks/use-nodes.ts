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
