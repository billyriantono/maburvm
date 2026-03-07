import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  Backup,
  BackupSchedule,
  CreateBackupScheduleRequest,
  UpdateBackupScheduleRequest,
} from '@/types'

export function useBackups(vmId: string) {
  return useQuery<Backup[]>({
    queryKey: ['backups', 'list', vmId],
    queryFn: () => api.get<Backup[]>(`/api/v1/vms/${vmId}/backups`),
    enabled: !!vmId,
  })
}

export function useBackupSchedules(vmId: string) {
  return useQuery<BackupSchedule[]>({
    queryKey: ['backups', 'schedules', vmId],
    queryFn: () =>
      api.get<BackupSchedule[]>(`/api/v1/vms/${vmId}/backup-schedules`),
    enabled: !!vmId,
  })
}

export function useCreateBackupSchedule(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<BackupSchedule, Error, CreateBackupScheduleRequest>({
    mutationFn: (data) =>
      api.post<BackupSchedule>(`/api/v1/vms/${vmId}/backup-schedules`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['backups', 'schedules', vmId],
      })
    },
  })
}

export function useUpdateBackupSchedule(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<
    BackupSchedule,
    Error,
    { scheduleId: string; data: UpdateBackupScheduleRequest }
  >({
    mutationFn: ({ scheduleId, data }) =>
      api.put<BackupSchedule>(
        `/api/v1/vms/${vmId}/backup-schedules/${scheduleId}`,
        data
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['backups', 'schedules', vmId],
      })
    },
  })
}

export function useDeleteBackupSchedule(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (scheduleId) =>
      api.delete(`/api/v1/vms/${vmId}/backup-schedules/${scheduleId}`),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['backups', 'schedules', vmId],
      })
    },
  })
}

export function useCreateBackup(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<Backup, Error, void>({
    mutationFn: () => api.post<Backup>(`/api/v1/vms/${vmId}/backups`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['backups', 'list', vmId] })
    },
  })
}

export function useDeleteBackup(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (backupId) =>
      api.delete(`/api/v1/vms/${vmId}/backups/${backupId}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['backups', 'list', vmId] })
    },
  })
}
