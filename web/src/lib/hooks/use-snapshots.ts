import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Snapshot, CreateSnapshotRequest } from '@/types'

export function useSnapshots(vmId: string) {
  return useQuery<Snapshot[]>({
    queryKey: ['snapshots', 'list', vmId],
    queryFn: () => api.get<Snapshot[]>(`/api/v1/vms/${vmId}/snapshots`),
    enabled: !!vmId,
  })
}

export function useCreateSnapshot(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<Snapshot, Error, CreateSnapshotRequest>({
    mutationFn: (data) =>
      api.post<Snapshot>(`/api/v1/vms/${vmId}/snapshots`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['snapshots', 'list', vmId],
      })
    },
  })
}

export function useRestoreSnapshot(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (snapshotId) =>
      api.post(`/api/v1/vms/${vmId}/snapshots/${snapshotId}/restore`),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['snapshots', 'list', vmId],
      })
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', vmId] })
    },
  })
}

export function useDeleteSnapshot(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (snapshotId) =>
      api.delete(`/api/v1/vms/${vmId}/snapshots/${snapshotId}`),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['snapshots', 'list', vmId],
      })
    },
  })
}
