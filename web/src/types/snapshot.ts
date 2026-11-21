// SnapshotStatus matches the Go SnapshotStatus type
export type SnapshotStatus = 'pending' | 'completed' | 'failed';

// Snapshot represents a VM snapshot
export interface Snapshot {
  id: string;
  vm_id: string;
  name: string;
  disk_path: string;
  status: SnapshotStatus;
  created_at: string;
  updated_at: string;
  // Taken without the VM's memory.
  disk_only?: boolean;
  // Taken, and removed in time, by the VM's snapshot schedule.
  scheduled?: boolean;
}

// CreateSnapshotRequest for creating snapshots. disk_only takes a running VM's
// disks without its memory and without pausing it; quiesce freezes its
// filesystems for that through its guest agent.
export interface CreateSnapshotRequest {
  vm_id: string;
  name: string;
  disk_only?: boolean;
  quiesce?: boolean;
}

// SnapshotSchedule takes a snapshot every interval_hours, keeping the newest
// `keep` of those it took.
export interface SnapshotSchedule {
  id: string;
  vm_id: string;
  interval_hours: number;
  keep: number;
  disk_only: boolean;
  quiesce: boolean;
  enabled: boolean;
  next_run_at: string;
  last_run_at: string | null;
  last_error: string;
}

export interface SnapshotScheduleRequest {
  interval_hours: number;
  keep: number;
  disk_only: boolean;
  quiesce: boolean;
  enabled?: boolean;
}

// RestoreSnapshotRequest for restoring snapshots
export interface RestoreSnapshotRequest {
  snapshot_id: string;
}
