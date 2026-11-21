import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { DiskResizeResult, VMDiskMove } from '@/types/storage'

// VMDisk is an extra data disk attached to a VM (not the primary boot disk).
export interface VMDisk {
  id: string
  vm_id: string
  device: string // virtio target, e.g. vdb
  size_gb: number
  path: string
  created_at: string
}

export function useVMDisks(vmId: string) {
  return useQuery<VMDisk[]>({
    queryKey: ['vm-disks', vmId],
    queryFn: async () => {
      const { data } = await api.get<VMDisk[]>(`/api/v1/vms/${vmId}/disks`)
      return data.data ?? []
    },
    enabled: !!vmId,
  })
}

export function useAttachDisk(vmId: string) {
  const qc = useQueryClient()
  return useMutation<VMDisk, Error, number>({
    mutationFn: async (sizeGB) => {
      const { data } = await api.post(`/api/v1/vms/${vmId}/disks`, { size_gb: sizeGB })
      return data.data as VMDisk
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['vm-disks', vmId] }),
  })
}

export function useDetachDisk(vmId: string) {
  const qc = useQueryClient()
  return useMutation<void, Error, { device: string; deleteVolume: boolean }>({
    mutationFn: async ({ device, deleteVolume }) => {
      await api.delete(`/api/v1/vms/${vmId}/disks/${device}?delete_volume=${deleteVolume}`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['vm-disks', vmId] }),
  })
}

// useResizeDisk grows one disk: vda (the system disk) or an extra one. The VM
// may be running; the guest grows its partition and filesystem itself.
export function useResizeDisk(vmId: string) {
  const qc = useQueryClient()
  return useMutation<DiskResizeResult, Error, { device: string; sizeGB: number }>({
    mutationFn: async ({ device, sizeGB }) => {
      const { data } = await api.post<DiskResizeResult>(`/api/v1/vms/${vmId}/disks/${device}/resize`, { size_gb: sizeGB })
      return data.data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['vm-disks', vmId] })
      qc.invalidateQueries({ queryKey: ['vms', 'detail', vmId] })
    },
  })
}

// useDiskMoves lists a VM's disk moves (staff), polling while one runs.
export function useDiskMoves(vmId: string, enabled = true) {
  return useQuery<VMDiskMove[]>({
    queryKey: ['vm-disk-moves', vmId],
    queryFn: async () => {
      const { data } = await api.get<VMDiskMove[]>(`/api/v1/vms/${vmId}/disk-moves`)
      return data.data ?? []
    },
    enabled: !!vmId && enabled,
    refetchInterval: (q) => (q.state.data ?? []).some((m) => m.status === 'queued' || m.status === 'running') ? 5000 : false,
  })
}

// useMoveDisk moves one disk to another storage pool of the VM's node.
export function useMoveDisk(vmId: string) {
  const qc = useQueryClient()
  return useMutation<VMDiskMove, Error, { device: string; destPoolId: string }>({
    mutationFn: async ({ device, destPoolId }) => {
      const { data } = await api.post<VMDiskMove>(`/api/v1/vms/${vmId}/disks/${device}/move`, { dest_pool_id: destPoolId })
      return data.data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['vm-disk-moves', vmId] }),
  })
}

export function useCancelDiskMove(vmId: string) {
  const qc = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(`/api/v1/vms/${vmId}/disk-moves/cancel`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['vm-disk-moves', vmId] }),
  })
}
