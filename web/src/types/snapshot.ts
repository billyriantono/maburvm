export type SnapshotStatus = 'pending' | 'completed' | 'failed'

export interface Snapshot {
  id: string
  vm_id: string
  name: string
  disk_path: string
  status: SnapshotStatus
  created_at: string
  updated_at: string
}

export interface CreateSnapshotRequest {
  vm_id: string
  name: string
}

export interface RestoreSnapshotRequest {
  snapshot_id: string
}
