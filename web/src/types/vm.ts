// VMStatus matches the Go VMStatus type
export type VMStatus = 'running' | 'stopped' | 'suspended' | 'creating' | 'error';

// Resources represents CPU, RAM, and Disk resources for a VM
export interface Resources {
  cpu: number;
  ram: number; // MB
  disk: number; // GB
  iops?: number;
  swap?: number; // MB
}

// VM represents a virtual machine in the system
export interface VM {
  id: string;
  user_id: string;
  node_id: string;
  node_name?: string;
  hostname: string;
  os_template_id: string;
  resources: Resources;
  status: VMStatus;
  source_migration?: string;
  vnc_port?: number;
  rescue_mode?: boolean;
  console_enabled?: boolean;
  created_at: string;
  updated_at: string;
}

// VMMetricSample is one persisted historical point (matches the Go model's JSON).
export interface VMMetricSample {
  id: number;
  vm_id: string;
  cpu_usage: number;
  memory_usage: number;
  memory_used_bytes: number;
  disk_read_bytes_per_sec: number;
  disk_write_bytes_per_sec: number;
  network_rx_bytes_per_sec: number;
  network_tx_bytes_per_sec: number;
  recorded_at: string;
}

// VMMetrics represents VM performance metrics
export interface VMMetrics {
  cpu_percent: number;
  memory_used: number;
  memory_total: number;
  memory_used_percent: number;
  disk_read_bytes_per_sec: number;
  disk_write_bytes_per_sec: number;
  network_rx_bytes_per_sec: number;
  network_tx_bytes_per_sec: number;
}

// CreateVMRequest for creating new VMs
export interface CreateVMRequest {
  hostname: string;
  os_template_id: string;
  node_id?: string;
  plan_id?: string;        // derive resources from a VPS plan (flavor)
  resources: Resources;
  ip_pool_id?: string;     // allocate an IP from this managed pool
  requested_ip?: string;   // specific IP within the pool (optional)
  bandwidth_mbps?: number; // network rate cap
  vlan_id?: number;        // 802.1Q VLAN tag
  cpu_model?: string;      // guest CPU model; empty → node default (kvm64, migratable)
  user_data?: string;      // first-boot script/recipe (cloud-init), run once per instance
  managed_network_id?: string; // attach to a private VPC / managed network instead of a pool
  recipe_id?: string;      // first-boot recipe to inject as user_data (ignored if user_data set)
}

// UpdateVMRequest for updating VMs
export interface UpdateVMRequest {
  hostname?: string;
  resources?: Partial<Resources>;
}

// VMStartRequest for starting a VM
export interface VMStartRequest {
  vnc_enabled?: boolean;
}

// VMStartResponse after starting a VM
export interface VMStartResponse {
  vnc_port?: number;
  vnc_password?: string;
}
