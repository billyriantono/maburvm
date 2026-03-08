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
}

// CreateSnapshotRequest for creating snapshots
export interface CreateSnapshotRequest {
  vm_id: string;
  name: string;
}

// RestoreSnapshotRequest for restoring snapshots
export interface RestoreSnapshotRequest {
  snapshot_id: string;
}
