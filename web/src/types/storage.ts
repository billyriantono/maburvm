export interface StoragePool {
  id: string
  name: string
  type: string
  status: 'online' | 'offline' | 'degraded'
  total_space: number
  used_space: number
  available_space: number
  path: string
  node_id: string
  created_at: string
  updated_at: string
}

export interface StorageVolume {
  id: string
  name: string
  pool_id: string
  vm_id?: string
  size: number
  format: string
  path: string
  created_at: string
  updated_at: string
}
