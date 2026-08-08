import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { CreateStoragePoolRequest, CreateStorageVolumeRequest, StoragePool, StorageVolume } from '@/types'

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
  return useMutation<StoragePool, Error, CreateStoragePoolRequest>({
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
  return useMutation<StoragePool, Error, { id: string; total_space: number }>({
    mutationFn: async ({ id, total_space }) => {
      const response = await api.put<StoragePool>(`/api/v1/storage/pools/${id}`, { total_space })
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'pools'] })
    },
  })
}

// Promoting a pool is how an operator says "new VMs go here". Without one, the
// node falls back to its root filesystem, which is where a full disk takes down
// libvirt and the agent rather than merely failing the next order.
export function useSetPrimaryPool() {
  const queryClient = useQueryClient()
  return useMutation<StoragePool, Error, string>({
    mutationFn: async (id) => {
      const response = await api.put<StoragePool>(`/api/v1/storage/pools/${id}`, {
        is_primary: true,
      })
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

// useStorageVolumes lists the volumes in a pool.
export function useStorageVolumes(poolId: string) {
  return useQuery<StorageVolume[]>({
    queryKey: ['storage', 'volumes', poolId],
    queryFn: async () => {
      const response = await api.get<StorageVolume[]>(`/api/v1/storage/pools/${poolId}/volumes`)
      return response.data.data
    },
    enabled: !!poolId,
  })
}

// useCreateStorageVolume provisions a real volume (qcow2/raw) on the pool's node.
export function useCreateStorageVolume(poolId: string) {
  const queryClient = useQueryClient()
  return useMutation<StorageVolume, Error, CreateStorageVolumeRequest>({
    mutationFn: async (data) => {
      const response = await api.post<StorageVolume>(`/api/v1/storage/pools/${poolId}/volumes`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'volumes', poolId] })
    },
  })
}

// useDeleteStorageVolume removes a volume (and its disk image) from a pool.
export function useDeleteStorageVolume(poolId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (volumeId) => {
      await api.delete(`/api/v1/storage/pools/${poolId}/volumes/${volumeId}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'volumes', poolId] })
    },
  })
}
