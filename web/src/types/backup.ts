export type BackupStatus = 'pending' | 'in_progress' | 'completed' | 'failed'
export type BackupType = 'manual' | 'scheduled'
export type BackupScheduleStatus = 'active' | 'paused' | 'disabled'

export interface Backup {
  id: string
  vm_id: string
  storage_provider: string
  storage_path: string
  backup_type: BackupType
  status: BackupStatus
  size: number
  compression: 'gzip' | 'zstd' | 'none'
  checksum?: string
  error_message?: string
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
}

export interface BackupRetentionPolicy {
  keep_last?: number
  keep_daily?: number
  keep_weekly?: number
  keep_monthly?: number
}

export interface BackupSchedule {
  id: string
  vm_id: string
  schedule: string
  status: BackupScheduleStatus
  storage_provider: string
  compression: 'gzip' | 'zstd' | 'none'
  retention_policy: BackupRetentionPolicy
  next_run_at?: string
  last_run_at?: string
  last_backup_id?: string
  created_at: string
  updated_at: string
}

export interface CreateBackupScheduleRequest {
  vm_id: string
  schedule: string
  storage_provider: string
  compression?: 'gzip' | 'zstd' | 'none'
  retention_policy?: BackupRetentionPolicy
}

export interface UpdateBackupScheduleRequest {
  schedule?: string
  status?: BackupScheduleStatus
  storage_provider?: string
  compression?: 'gzip' | 'zstd' | 'none'
  retention_policy?: BackupRetentionPolicy
}
