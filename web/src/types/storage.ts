// StoragePool represents a storage pool on a node
export interface StoragePool {
  id: string;
  name: string;
  type: string; // "dir", "lvm" (path is a volume group) or "lvmthin" (path is "vg/thinpool")
  status: 'online' | 'offline' | 'degraded';
  total_space: number; // bytes
  used_space: number; // bytes
  available_space: number; // bytes
  path: string;
  file_format: 'raw' | 'qcow2';
  alert_threshold: number; // 0-100
  overcommit: number; // bytes, 0 = disabled
  is_primary: boolean;
  // A label for placement (nvme, hdd); plans naming it put disks only here.
  tier?: string;
  // Why the node cannot use the pool; empty when it can.
  status_note?: string;
  // A thin pool's metadata use, 0-100. Absent for other pools.
  metadata_percent?: number;
  // Virtual disk size promised on the pool, in bytes. Absent when not known.
  allocated_space?: number;
  // The most the disk overcommit ratio allows, in bytes. 0 = no limit set.
  allocation_limit?: number;
  node_id: string;
  node?: {
    id: string;
    name: string;
  };
  created_at: string;
  updated_at: string;
}

// StorageVolume represents a storage volume (disk image) in a pool
export interface StorageVolume {
  id: string;
  name: string;
  pool_id: string;
  vm_id?: string;
  size: number; // bytes
  format: string;
  path: string;
  created_at: string;
  updated_at: string;
}

// CreateStoragePoolRequest for creating storage pools
export interface CreateStoragePoolRequest {
  name: string;
  type: "dir" | "lvm" | "lvmthin";
  path: string;
  node_id: string;
  alert_threshold: number;
}

// CreateStorageVolumeRequest for creating storage volumes
export interface CreateStorageVolumeRequest {
  name: string;
  pool_id: string;
  size: number;
  format?: string;
}

// OrphanDisk is a disk file or logical volume on a node that no VM there and
// no panel record uses.
export interface OrphanDisk {
  node_id: string;
  path: string;
  kind: 'file' | 'lv';
  size_bytes: number;
  modified_unix: number;
  reason: string;
  pool: string;
  // Made by MaburVM (its name, or a volume's tag).
  maburvm: boolean;
  vm_id?: string;
  vm_hostname?: string;
  vm_deleted: boolean;
}

// VMDiskMove is one move of a VM's disk to another pool of its node.
export interface VMDiskMove {
  id: string;
  vm_id: string;
  target: string;
  size_gb: number;
  dest_pool_id: string;
  source_path: string;
  dest_path: string;
  live: boolean;
  status: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
  phase: string;
  total_bytes: number;
  done_bytes: number;
  error: string;
  note: string;
  created_at: string;
  finished_at?: string | null;
}

export interface DiskResizeResult {
  device: string;
  size_gb: number;
  live: boolean;
  queued: boolean;
  note: string;
}
