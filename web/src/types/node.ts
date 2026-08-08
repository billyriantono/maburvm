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
  // Connection tracking table. A zero max means the node could not read it —
  // unknown, not empty — so the UI must not render that as 0% healthy.
  conntrack_count: number;
  conntrack_max: number;
}

// NodeMetricSample is one persisted historical point (matches the Go model's JSON).
export interface NodeMetricSample {
  id: number;
  node_id: string;
  cpu_usage: number;
  memory_usage: number;
  disk_usage: number;
  network_rx_bytes_per_sec: number;
  network_tx_bytes_per_sec: number;
  vm_count: number;
  status: string;
  conntrack_count: number;
  conntrack_max: number;
  recorded_at: string;
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

// GuestConnection is how fast one guest NIC on one node is opening new outbound
// connections, and whether it has been cut off the network.
//
// Keyed on MAC rather than VM id: the guests worth catching are frequently ones
// the panel does not manage — those have an empty vm_id, which is itself the
// signal — and an abusive guest may be running a spoofed or duplicated address.
export interface GuestConnection {
  id: number;
  node_id: string;
  node_name: string;
  mac: string;
  vm_id: string;
  vm_hostname: string;
  interface_name: string;
  syn_total: number;
  syn_rate: number;
  peak_rate: number;
  quarantined: boolean;
  quarantine_reason: string;
  first_flagged_at: string | null;
  last_seen_at: string;
}
