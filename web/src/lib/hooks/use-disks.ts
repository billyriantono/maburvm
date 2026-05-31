import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

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
