// StoragePool represents a storage pool on a node
export interface StoragePool {
  id: string;
  name: string;
  // "dir", "lvm" (path is a volume group), "lvmthin" (path is "vg/thinpool") or
  // "rbd" (a node's part of an RBD pool; path is "pool" or "pool/namespace").
  type: string;
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
  // Set on a node's member of a shared pool; the Storage page shows those
  // once, under the shared pool.
  shared_pool_id?: string;
  node_id: string;
  node?: {
    id: string;
    name: string;
  };
  created_at: string;
  updated_at: string;
}

// SharedPool is storage several nodes see at the same path: an NFS export the
// nodes' agents mount, a directory the operator mounts on each node, or (kind
// rbd) an RBD pool of a registered Ceph cluster.
export interface SharedPool {
  id: string;
  name: string;
  kind: 'nfs' | 'mounted' | 'rbd';
  path: string;
  nfs_server: string;
  nfs_export: string;
  mount_options: string;
  // Kind rbd: the cluster, and the RBD pool and namespace in it.
  ceph_cluster_id?: string;
  rbd_pool: string;
  rbd_namespace: string;
  // Capacity of the storage, counted once however many nodes mount it.
  total_space: number;
  used_space: number;
  available_space: number;
  allocated_space?: number;
  allocation_limit: number;
  members: SharedPoolMember[];
  created_at: string;
}

export interface SharedPoolMember {
  // The node's own storage pool for the shared pool.
  pool_id: string;
  node_id: string;
  node_name: string;
  status: 'online' | 'offline' | 'degraded';
  status_note: string;
  is_primary: boolean;
  // The storage tier of the node's row; "" for none.
  tier: string;
}

export interface CreateSharedPoolRequest {
  name: string;
  kind: 'nfs' | 'mounted' | 'rbd';
  path?: string;
  nfs_server?: string;
  nfs_export?: string;
  mount_options?: string;
  ceph_cluster_id?: string;
  rbd_pool?: string;
  rbd_namespace?: string;
}

// CephCluster is an existing Ceph cluster staff registered. Its key is
// write-only: no answer carries it.
export interface CephCluster {
  id: string;
  name: string;
  monitors: string; // comma-separated host or host:port
  client_id: string;
  fsid: string;
  // As the last node to answer reported them. health is "" before any has.
  health: '' | 'HEALTH_OK' | 'HEALTH_WARN' | 'HEALTH_ERR';
  health_summary: string;
  total_bytes: number;
  used_bytes: number;
  available_bytes: number;
  checked_at: string | null;
  nodes: CephClusterNode[];
  pools: CephClusterPool[];
}

export interface CephClusterNode {
  node_id: string;
  node_name: string;
  // ok; unreachable (the node cannot reach the cluster); mismatch (it reaches
  // another cluster); degraded (something on the node is wrong).
  status: 'ok' | 'unreachable' | 'mismatch' | 'degraded';
  status_note: string;
  checked_at: string | null;
}

// One RBD pool of the cluster (a shared pool of kind rbd), measured once.
export interface CephClusterPool {
  id: string;
  name: string;
  rbd_pool: string;
  rbd_namespace: string;
  total_space: number;
  used_space: number;
  available_space: number;
}

export interface CephClusterRequest {
  name: string;
  monitors: string;
  client_id: string;
  // Empty on a change keeps the stored key.
  client_key: string;
  fsid?: string;
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
