export type NodeStatus = 'active' | 'maintenance' | 'offline'

export interface Node {
  id: string
  name: string
  ip_address: string
  status: NodeStatus
  created_at: string
  updated_at: string
}

export interface NodeMetrics {
  id: string
  name: string
  cpu_usage: number
  memory_usage: number
  disk_usage: number
  vm_count: number
  status: NodeStatus
}

export interface CreateNodeRequest {
  name: string
  ip_address: string
}

export interface UpdateNodeRequest {
  name?: string
  ip_address?: string
  status?: NodeStatus
}
