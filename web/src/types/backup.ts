// BackupStatus matches the Go BackupStatus type
export type BackupStatus = 'pending' | 'in_progress' | 'completed' | 'failed';

// BackupType matches the Go BackupType type
export type BackupType = 'manual' | 'scheduled';

// BackupScheduleStatus matches the Go type
export type BackupScheduleStatus = 'active' | 'paused' | 'disabled';

// BackupRetentionPolicy for backup retention settings
export interface BackupRetentionPolicy {
  keep_last?: number;
  keep_daily?: number;
  keep_weekly?: number;
  keep_monthly?: number;
}

// Backup represents a VM backup
export interface Backup {
  id: string;
  vm_id: string;
  storage_provider: string;
  storage_path: string;
  backup_type: BackupType;
  status: BackupStatus;
  size: number;
  compression: 'gzip' | 'zstd' | 'none';
  checksum?: string;
  error_message?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
  updated_at: string;
}

// BackupSchedule represents a scheduled backup configuration
export interface BackupSchedule {
  id: string;
  vm_id: string;
  schedule: string; // Cron expression
  status: BackupScheduleStatus;
  storage_provider: string;
  compression: 'gzip' | 'zstd' | 'none';
  retention_policy: BackupRetentionPolicy;
  next_run_at?: string;
  last_run_at?: string;
  last_backup_id?: string;
  created_at: string;
  updated_at: string;
}

// CreateBackupRequest for creating backups
export interface CreateBackupRequest {
  vm_id: string;
  storage_provider: string;
  compression?: 'gzip' | 'zstd' | 'none';
}

// CreateBackupScheduleRequest for creating backup schedules
export interface CreateBackupScheduleRequest {
  vm_id: string;
  schedule: string;
  storage_provider: string;
  compression?: 'gzip' | 'zstd' | 'none';
  retention_policy?: BackupRetentionPolicy;
}

// UpdateBackupScheduleRequest for updating backup schedules
export interface UpdateBackupScheduleRequest {
  schedule?: string;
  status?: BackupScheduleStatus;
  storage_provider?: string;
  compression?: 'gzip' | 'zstd' | 'none';
  retention_policy?: BackupRetentionPolicy;
}
