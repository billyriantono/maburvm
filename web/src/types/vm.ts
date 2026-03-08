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
  hostname: string;
  os_template_id: string;
  resources: Resources;
  status: VMStatus;
  source_migration?: string;
  vnc_port?: number;
  created_at: string;
  updated_at: string;
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
  resources: Resources;
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
