import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  Backup,
  BackupOverview,
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
    // A backup runs on its node after the request returns: poll while one is
    // queued or running, or the row reads "In progress" until a reload.
    refetchInterval: (query) =>
      query.state.data?.some((b) => b.status === 'pending' || b.status === 'in_progress') ? 5000 : false,
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

export function useRestoreBackup(vmId: string) {
  return useMutation<void, Error, string>({
    mutationFn: async (backupId) => {
      await api.post(`/api/v1/vms/${vmId}/backups/${backupId}/restore`)
    },
  })
}

// Admin only: the backup becomes a new VM on the chosen node.
export function useRestoreBackupAsNew(vmId: string) {
  return useMutation<{ vm: { id: string; hostname: string } }, Error, { backupId: string; node_id: string; hostname?: string }>({
    mutationFn: async ({ backupId, ...body }) => {
      const response = await api.post<{ vm: { id: string; hostname: string } }>(
        `/api/v1/vms/${vmId}/backups/${backupId}/restore-new`,
        body
      )
      return response.data.data
    },
  })
}

export function useDeleteBackup(vmId: string) {
  const queryClient = useQueryClient()

  // Resolves to the panel's own words: a backup whose file the panel could not
  // reach is removed from the list, and the message says the file was left.
  return useMutation<string, Error, string>({
    mutationFn: async (backupId) => {
      const response = await api.delete(`/api/v1/vms/${vmId}/backups/${backupId}`)
      return response.data.message || 'Backup deleted'
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['backups', 'list', vmId] })
    },
  })
}

export interface BackupOverviewFilter {
  node_id?: string
  state?: string
  q?: string
  stale_days?: number
}

// The fleet backup page (staff). Refreshed while backups run.
export function useBackupOverview(filter: BackupOverviewFilter) {
  return useQuery<BackupOverview>({
    queryKey: ['backups', 'overview', filter],
    queryFn: async () => {
      const params = new URLSearchParams()
      for (const [k, v] of Object.entries(filter)) {
        if (v !== undefined && v !== '') params.set(k, String(v))
      }
      const response = await api.get<BackupOverview>(`/api/v1/backups/overview?${params}`)
      return response.data.data
    },
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })
}
