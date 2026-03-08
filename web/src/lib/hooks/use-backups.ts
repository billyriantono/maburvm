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
    queryFn: async () => {
      const response = await api.get<Backup[]>(`/api/v1/vms/${vmId}/backups`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

export function useBackupSchedules(vmId: string) {
  return useQuery<BackupSchedule[]>({
    queryKey: ['backups', 'schedules', vmId],
    queryFn: async () => {
      const response = await api.get<BackupSchedule[]>(`/api/v1/vms/${vmId}/backup-schedules`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

export function useCreateBackupSchedule(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<BackupSchedule, Error, CreateBackupScheduleRequest>({
    mutationFn: async (data) => {
      const response = await api.post<BackupSchedule>(`/api/v1/vms/${vmId}/backup-schedules`, data)
      return response.data.data
    },
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
    mutationFn: async ({ scheduleId, data }) => {
      const response = await api.put<BackupSchedule>(
        `/api/v1/vms/${vmId}/backup-schedules/${scheduleId}`,
        data
      )
      return response.data.data
    },
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
    mutationFn: async (scheduleId) => {
      await api.delete(`/api/v1/vms/${vmId}/backup-schedules/${scheduleId}`)
    },
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
    mutationFn: async () => {
      const response = await api.post<Backup>(`/api/v1/vms/${vmId}/backups`)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['backups', 'list', vmId] })
    },
  })
}

export function useDeleteBackup(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (backupId) => {
      await api.delete(`/api/v1/vms/${vmId}/backups/${backupId}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['backups', 'list', vmId] })
    },
  })
}
