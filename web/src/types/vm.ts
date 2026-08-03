// VMStatus matches the Go VMStatus type
export type VMStatus = 'running' | 'stopped' | 'suspended' | 'creating' | 'deleting' | 'error';
export type VMNodeStatus = 'active' | 'maintenance' | 'offline' | '';

// Resources represents CPU, RAM, and Disk resources for a VM
export interface Resources {
  cpu: number;
  ram: number; // MB
  disk: number; // GB
  iops?: number;
  swap?: number; // MB
}

// VMOperation tracks a multi-step VM operation (e.g. delete) for the progress UI.
export interface VMOperation {
  id: string;
  vm_id: string;
  operation: string;
  status: 'running' | 'completed' | 'failed';
  current_step: number;
  total_steps: number;
  step_label: string;
  error: string;
  started_at: string;
  updated_at: string;
  completed_at?: string;
}

// VM represents a virtual machine in the system
export interface VM {
  id: string;
  user_id: string;
  node_id: string;
  node_name?: string;
  node_status?: VMNodeStatus;
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
  os_template_id?: string;    // optional when source_image_id is set (derived server-side)
  source_image_id?: string;   // seed the new VM's disk from a saved image
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
  password?: string;       // root password to inject (min 8 chars); omit to auto-generate
  regenerate_password?: boolean; // generate a root password and return it once
  ssh_key_ids?: string[];  // saved SSH keys to inject into the new guest
  ssh_public_keys?: string[];  // raw public keys pasted at create time
}

// UpdateVMRequest for updating VMs
export interface UpdateVMRequest {
  hostname?: string;
  resources?: Partial<Resources>;
  user_id?: string;
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
