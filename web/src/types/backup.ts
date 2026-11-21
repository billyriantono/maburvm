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
  // Backups older than this many days are deleted; the newest is always kept.
  max_age_days?: number;
}

// BackupDisk is one disk of a backup; the first is the primary disk.
export interface BackupDisk {
  target: string;
  key: string;
  size: number;
  checksum: string;
  virtual_size?: number;
}

// How a backup's copy was taken; '' for backups made before it was recorded.
export type BackupConsistency = 'stopped' | 'frozen' | 'crash' | 'live' | '';

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
  // Every disk of the backup; empty for a backup of the primary disk only.
  disks?: BackupDisk[] | null;
  consistency?: BackupConsistency;
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
  vm_id?: string;
  schedule: string;
  storage_provider?: string;
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
