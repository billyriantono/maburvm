import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { StoragePool } from '@/types'

export function useStoragePools() {
  return useQuery<StoragePool[]>({
    queryKey: ['storage', 'pools'],
    queryFn: async () => {
      const response = await api.get<StoragePool[]>('/api/v1/storage/pools')
      return response.data.data
    },
  })
}

export function useStoragePool(id: string) {
  return useQuery<StoragePool>({
    queryKey: ['storage', 'pools', id],
    queryFn: async () => {
      const response = await api.get<StoragePool>(`/api/v1/storage/pools/${id}`)
      return response.data.data
    },
    enabled: !!id,
  })
}

export function useCreateStoragePool() {
  const queryClient = useQueryClient()
  return useMutation<StoragePool, Error, { name: string; path: string; pool_type: string; node_id: string; total_bytes: number }>({
    mutationFn: async (data) => {
      const response = await api.post<StoragePool>('/api/v1/storage/pools', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'pools'] })
    },
  })
}

export function useResizeStoragePool() {
  const queryClient = useQueryClient()
  return useMutation<StoragePool, Error, { id: string; total_bytes: number }>({
    mutationFn: async ({ id, total_bytes }) => {
      const response = await api.put<StoragePool>(`/api/v1/storage/pools/${id}`, { total_bytes })
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'pools'] })
    },
  })
}

export function useDeleteStoragePool() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/storage/pools/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'pools'] })
    },
  })
}
