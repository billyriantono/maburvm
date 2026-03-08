// NodeStatus matches the Go NodeStatus type
export type NodeStatus = 'active' | 'maintenance' | 'offline';

// Node represents a compute node in the system
export interface Node {
  id: string;
  name: string;
  ip_address: string;
  status: NodeStatus;
  created_at: string;
  updated_at: string;
}

// NodeMetrics holds system metrics for a node
export interface NodeMetrics {
  cpu_percent: number;
  memory_used: number;
  memory_total: number;
  memory_used_percent: number;
  disk_used: number;
  disk_total: number;
  disk_used_percent: number;
  network_rx_bytes_per_sec: number;
  network_tx_bytes_per_sec: number;
  disk_read_bytes_per_sec: number;
  disk_write_bytes_per_sec: number;
  running_vm_count: number;
  available_cpus: number;
  available_memory_mb: number;
  available_disk_gb: number;
  load_avg: number[];
}

// CreateNodeRequest for registering new nodes
export interface CreateNodeRequest {
  name: string;
  ip_address: string;
}

// UpdateNodeRequest for updating nodes
export interface UpdateNodeRequest {
  name?: string;
  status?: NodeStatus;
}
