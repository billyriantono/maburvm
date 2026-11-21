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

/** CPU and disk caps of a VM or plan. 0 in a field is no limit. */
export interface VMLimits {
  /** Each vCPU may use at most this share of one host core (1-100). */
  cpu_cap_percent: number;
  /** CPU priority against other VMs on the node: 100 normal, 0 the node's default. */
  cpu_weight: number;
  /** Disk operations per second: a total, or read and write separately. */
  iops: number;
  read_iops: number;
  write_iops: number;
  /** Disk throughput in MB/s: a total, or read and write separately. */
  mbps: number;
  read_mbps: number;
  write_mbps: number;
}

/** A VM's limits after a change, and whether its node has them. */
export interface VMLimitsResult {
  limits: VMLimits;
  throttle: VMLimits | null;
  effective: VMLimits;
  applied: boolean;
  note: string | null;
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
  /** Where this VM physically runs. Customers reason in regions, not nodes, so
   *  this is what pairs a VM with the private networks and floating IPs it can
   *  actually use. */
  region_id?: string;
  region_name?: string;
  region_country?: string;
  id: string;
  user_id: string;
  node_id: string;
  node_name?: string;
  node_status?: VMNodeStatus;
  /** The node is offline, so `status` is only what the panel last saw. */
  state_unknown?: boolean;
  hostname: string;
  os_template_id: string;
  resources: Resources;
  status: VMStatus;
  source_migration?: string;
  vnc_port?: number;
  rescue_mode?: boolean;
  /** False while the operator has not chosen a rescue image. */
  rescue_available?: boolean;
  /** ISO in the VM's drive as last confirmed by the node: null/absent = not
   *  known, "" = none, otherwise the image URL or path. Detail response only. */
  attached_iso?: string | null;
  console_enabled?: boolean;
  /** Service suspended by billing or staff: the owner can view but not change it. */
  suspended?: boolean;
  /** The plan the VM was ordered on or last changed to. Detail response only. */
  plan_id?: string;
  plan_name?: string;
  /** The billing system's reference for the service that pays for this VM.
   *  Sent to operators only; the owner gets billing_managed. */
  billing_ref?: string;
  billing_managed?: boolean;
  /** False when the owner may not delete the VM themselves (billing manages it). */
  owner_may_delete?: boolean;
  /** The VM's IPv6. With ipv6_prefix it is a routed network of its own and
   *  the guest uses ipv6_address with fe80::1 as gateway; without it,
   *  ipv6_address is one address on a shared subnet and ipv6_gateway is that
   *  subnet's router. All absent when the VM has no IPv6. */
  ipv6_prefix?: string;
  ipv6_address?: string;
  ipv6_gateway?: string;
  /** What the firewall does with inbound traffic no rule matched, once the VM
   *  has rules. A VM without rules is not filtered. Detail response only. */
  firewall_default?: 'drop' | 'accept';
  /** Outbound mail (port 25) is blocked by the operator. Detail response only. */
  smtp_blocked?: boolean;
  /** An address change the guest has not taken, in words; with
   *  guest_network_pending the node applies it by itself later. */
  guest_network_note?: string | null;
  guest_network_pending?: boolean;
  /** CPU and disk caps (detail response only). The throttle and the note on
   *  whether the node has them are sent to operators only. */
  limits?: VMLimits;
  throttle?: VMLimits | null;
  limits_note?: string | null;
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
  ipv6_pool_id?: string;   // also delegate a /64 from this IPv6 pool (needs ip_pool_id)
  requested_ip?: string;   // specific IP within the pool (optional)
  bandwidth_mbps?: number; // network rate cap
  vlan_id?: number;        // 802.1Q VLAN tag
  cpu_model?: string;      // guest CPU model; empty → node default (kvm64, migratable)
  user_data?: string;      // first-boot script/recipe (cloud-init), run once per instance
  managed_network_id?: string; // attach to a private VPC / managed network instead of a pool
  vpc_id?: string;         // place in one of the caller's own private networks; also fixes the node
  region?: string;         // location to order into (id or slug); required for customer orders
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
  /** Operators only. "" clears; absent leaves it as it is. */
  plan_id?: string;
  billing_ref?: string;
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
