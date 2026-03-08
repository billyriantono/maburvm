// StoragePool represents a storage pool on a node
export interface StoragePool {
  id: string;
  name: string;
  type: string; // dir, lvm, zfs, etc.
  status: 'online' | 'offline' | 'degraded';
  total_space: number; // bytes
  used_space: number; // bytes
  available_space: number; // bytes
  path: string;
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
  type: string;
  path: string;
  node_id: string;
}

// CreateStorageVolumeRequest for creating storage volumes
export interface CreateStorageVolumeRequest {
  name: string;
  pool_id: string;
  size: number;
  format?: string;
}
