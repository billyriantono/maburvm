import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Snapshot, CreateSnapshotRequest, SnapshotSchedule, SnapshotScheduleRequest } from '@/types'

export function useSnapshots(vmId: string) {
  return useQuery<Snapshot[]>({
    queryKey: ['snapshots', 'list', vmId],
    queryFn: async () => {
      const response = await api.get<Snapshot[]>(`/api/v1/vms/${vmId}/snapshots`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

export function useCreateSnapshot(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<Snapshot, Error, CreateSnapshotRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Snapshot>(`/api/v1/vms/${vmId}/snapshots`, data)
      return response.data.data
    },
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
    mutationFn: async (snapshotId) => {
      await api.post(`/api/v1/vms/${vmId}/snapshots/${snapshotId}/restore`)
    },
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
    mutationFn: async (snapshotId) => {
      await api.delete(`/api/v1/vms/${vmId}/snapshots/${snapshotId}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['snapshots', 'list', vmId],
      })
    },
  })
}

const scheduleKey = (vmId: string) => ['snapshots', 'schedule', vmId]

/** The VM's snapshot schedule; null when it has none. */
export function useSnapshotSchedule(vmId: string) {
  return useQuery<SnapshotSchedule | null>({
    queryKey: scheduleKey(vmId),
    queryFn: async () => {
      const response = await api.get<SnapshotSchedule | null>(`/api/v1/vms/${vmId}/snapshot-schedule`)
      return response.data.data ?? null
    },
    enabled: !!vmId,
  })
}

export function useSetSnapshotSchedule(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<SnapshotSchedule, Error, SnapshotScheduleRequest>({
    mutationFn: async (body) => {
      const response = await api.put<SnapshotSchedule>(`/api/v1/vms/${vmId}/snapshot-schedule`, body)
      return response.data.data
    },
    onSuccess: (data) => queryClient.setQueryData(scheduleKey(vmId), data),
  })
}

export function useDeleteSnapshotSchedule(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.delete(`/api/v1/vms/${vmId}/snapshot-schedule`)
    },
    onSuccess: () => {
      queryClient.setQueryData(scheduleKey(vmId), null)
      queryClient.invalidateQueries({ queryKey: ['snapshots', 'list', vmId] })
    },
  })
}
