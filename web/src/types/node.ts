// NodeStatus matches the Go NodeStatus type
export type NodeStatus = 'active' | 'maintenance' | 'offline';

// Node represents a compute node in the system
export interface Node {
  id: string;
  name: string;
  ip_address: string;
  status: NodeStatus;
  /** No new VMs are placed on the node; the ones already there are unaffected. */
  creation_paused?: boolean;
  /** When the node's agent last reported to the panel; absent if it never has. */
  last_report_at?: string | null;
  /** Checks in a row the node has missed. 0 while it is reporting. */
  failed_polls?: number;
  /** What the node has promised to VMs against what it has (detail only). */
  allocation?: NodeAllocation;
  /** No agent has ever answered for the node and no VM is on it, so the
   *  installer may be offered (detail only). */
  installer_allowed?: boolean;
  created_at: string;
  updated_at: string;
}

export type PreflightStatus = "pass" | "warn" | "fail";

/** One line of a node's host checks, in its agent's words. */
export interface PreflightCheck {
  id: string;
  name: string;
  status: PreflightStatus;
  detail: string;
  fix?: string;
}

/** A node's last host check report. checked_at null: never asked. */
export interface NodePreflight {
  /** False when the node's agent is too old to report one. */
  supported: boolean;
  checks: PreflightCheck[];
  checked_at: string | null;
}

/** A node's single-use install command; the token is shown once. */
export interface NodeInstallCommand {
  token: string;
  expires_at: string;
  script_url: string;
  command: string;
}

/** A node's allocation. A limit of 0 is no cap: no ratio set, or the node has
 *  not reported the figure yet (cpu_count or ram_total_mb 0). */
export interface NodeAllocation {
  vcpu_allocated: number;
  cpu_count: number;
  /** The ratio in force: the node's own, else the panel-wide one. */
  vcpu_ratio: number;
  /** The node's own ratio; 0 means the panel-wide one applies. */
  vcpu_ratio_node: number;
  vcpu_limit: number;
  ram_allocated_mb: number;
  ram_total_mb: number;
  ram_ratio: number;
  ram_limit_mb: number;
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
  // Whether the node passes bridged VM traffic through its firewall. "unknown"
  // is a node that has not said (an older agent); never treat it as enforced.
  firewall_enforcement?: FirewallEnforcement;
  // The node address port forwards are published on; empty from an older agent.
  nat_address?: string;
}

export type FirewallEnforcement = "enforced" | "off" | "unknown";

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
  creation_paused?: boolean;
  /** The node's own vCPU overcommit ratio; 0 returns it to the panel-wide one. */
  vcpu_ratio?: number;
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
