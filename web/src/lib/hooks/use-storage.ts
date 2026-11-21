import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { CreateStoragePoolRequest, CreateStorageVolumeRequest, StoragePool, StorageVolume } from '@/types'
import type { CephCluster, CephClusterRequest, CreateSharedPoolRequest, OrphanDisk, SharedPool } from '@/types/storage'

// Calls that ask nodes (a Ceph cluster on each, or a shared pool's members)
// wait on them; the panel bounds each node, not the whole call. 180 s is the
// web proxy's own limit (next.config proxyTimeout).
const askNodes = { timeout: 180_000 }

// Ceph clusters: existing clusters staff register and enable on nodes. A
// change also changes the RBD pools' members, so every storage query is
// refreshed.
export function useCephClusters() {
  return useQuery<CephCluster[]>({
    queryKey: ['storage', 'ceph-clusters'],
    queryFn: async () => {
      const response = await api.get<CephCluster[]>('/api/v1/storage/ceph-clusters')
      return response.data.data ?? []
    },
    // Health is read on the nodes about once a minute.
    refetchInterval: 60_000,
  })
}

export type CephClusterChange =
  | { op: 'create'; cluster: CephClusterRequest }
  | { op: 'update'; id: string; cluster: CephClusterRequest }
  | { op: 'delete'; id: string }
  | { op: 'enable-node'; id: string; nodeId: string }
  | { op: 'disable-node'; id: string; nodeId: string }

export function useChangeCephCluster() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, CephClusterChange>({
    mutationFn: async (c) => {
      const base = '/api/v1/storage/ceph-clusters'
      if (c.op === 'create') await api.post(base, c.cluster)
      else if (c.op === 'update') await api.put(`${base}/${c.id}`, c.cluster, askNodes)
      else if (c.op === 'delete') await api.delete(`${base}/${c.id}`)
      else if (c.op === 'enable-node') await api.post(`${base}/${c.id}/nodes`, { node_id: c.nodeId }, askNodes)
      else await api.delete(`${base}/${c.id}/nodes/${c.nodeId}`, askNodes)
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['storage'] })
    },
  })
}

// useCheckCephCluster asks every node the cluster is enabled on, now; the
// answer maps node ids to what is wrong ("" passes).
export function useCheckCephCluster() {
  const queryClient = useQueryClient()
  return useMutation<Record<string, string>, Error, string>({
    mutationFn: async (id) => {
      const response = await api.post<Record<string, string>>(`/api/v1/storage/ceph-clusters/${id}/check`, undefined, askNodes)
      return response.data.data ?? {}
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['storage'] })
    },
  })
}

// Shared pools: storage several nodes mount. Every change also changes the
// member rows in the pool list.
export function useSharedPools() {
  return useQuery<SharedPool[]>({
    queryKey: ['storage', 'shared-pools'],
    queryFn: async () => {
      const response = await api.get<SharedPool[]>('/api/v1/storage/shared-pools')
      return response.data.data ?? []
    },
  })
}

export type SharedPoolChange =
  | { op: 'create'; pool: CreateSharedPoolRequest }
  | { op: 'delete'; id: string }
  | { op: 'add-node'; id: string; nodeId: string }
  | { op: 'remove-node'; id: string; nodeId: string }

export function useChangeSharedPool() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, SharedPoolChange>({
    mutationFn: async (c) => {
      const base = '/api/v1/storage/shared-pools'
      if (c.op === 'create') await api.post(base, c.pool, askNodes)
      else if (c.op === 'delete') await api.delete(`${base}/${c.id}`)
      else if (c.op === 'add-node') await api.post(`${base}/${c.id}/members`, { node_id: c.nodeId })
      else await api.delete(`${base}/${c.id}/members/${c.nodeId}`)
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['storage'] })
    },
  })
}

// useCheckSharedPool proves every member against the pool now; the answer
// maps node ids to what is wrong ("" passes).
export function useCheckSharedPool() {
  const queryClient = useQueryClient()
  return useMutation<Record<string, string>, Error, string>({
    mutationFn: async (id) => {
      const response = await api.post<Record<string, string>>(`/api/v1/storage/shared-pools/${id}/check`, undefined, askNodes)
      return response.data.data ?? {}
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['storage'] })
    },
  })
}

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

// useSetPoolTier labels a pool with a storage tier ("" clears it).
export function useSetPoolTier() {
  const queryClient = useQueryClient()
  return useMutation<StoragePool, Error, { id: string; tier: string }>({
    mutationFn: async ({ id, tier }) => {
      const response = await api.put<StoragePool>(`/api/v1/storage/pools/${id}/tier`, { tier })
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['storage'] })
    },
  })
}

// useStorageTiers lists the tiers pools are labelled with, for pickers.
export function useStorageTiers(enabled = true) {
  return useQuery<string[]>({
    queryKey: ['storage', 'tiers'],
    queryFn: async () => {
      const response = await api.get<string[]>('/api/v1/storage/tiers')
      return response.data.data ?? []
    },
    enabled,
  })
}

// useOrphanDisks asks a node for the disks in its pools nothing uses. Asked on
// purpose only: the node reads every pool to answer.
export function useOrphanDisks(nodeId: string, enabled: boolean) {
  return useQuery<OrphanDisk[]>({
    queryKey: ['storage', 'orphans', nodeId],
    queryFn: async () => {
      const response = await api.get<OrphanDisk[]>('/api/v1/storage/orphans', { params: { node_id: nodeId } })
      return response.data.data ?? []
    },
    enabled: !!nodeId && enabled,
    staleTime: Infinity,
  })
}

export function useDeleteOrphanDisk() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { disk: OrphanDisk; allowForeign: boolean }>({
    mutationFn: async ({ disk, allowForeign }) => {
      await api.post('/api/v1/storage/orphans/delete', {
        node_id: disk.node_id, path: disk.path, size_bytes: disk.size_bytes, modified_unix: disk.modified_unix,
        confirm: disk.path, allow_foreign: allowForeign,
      })
    },
    onSuccess: (_, { disk }) => {
      queryClient.invalidateQueries({ queryKey: ['storage', 'orphans', disk.node_id] })
    },
  })
}
