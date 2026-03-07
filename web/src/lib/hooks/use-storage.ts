import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { StoragePool } from '@/types'

export function useStoragePools() {
  return useQuery<StoragePool[]>({
    queryKey: ['storage', 'pools'],
    queryFn: () => api.get<StoragePool[]>('/api/v1/storage/pools'),
  })
}

export function useStoragePool(id: string) {
  return useQuery<StoragePool>({
    queryKey: ['storage', 'pools', id],
    queryFn: () => api.get<StoragePool>(`/api/v1/storage/pools/${id}`),
    enabled: !!id,
  })
}
