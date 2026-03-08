import { useQuery } from '@tanstack/react-query'
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
