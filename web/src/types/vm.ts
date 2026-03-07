export type VMStatus = 'running' | 'stopped' | 'suspended' | 'creating' | 'error'

export interface Resources {
  cpu: number
  ram: number
  disk: number
  iops?: number
  swap?: number
}

export interface VM {
  id: string
  user_id: string
  node_id: string
  hostname: string
  os_template_id: string
  resources: Resources
  status: VMStatus
  source_migration?: string
  vnc_port?: number
  created_at: string
  updated_at: string
}

export interface VMMetrics {
  id: string
  hostname: string
  status: VMStatus
  cpu_usage: number
  memory_usage: number
  disk_usage: number
  network_in: number
  network_out: number
}

export type VMAction = 'start' | 'stop' | 'restart' | 'rebuild' | 'suspend' | 'resume'

export interface CreateVMRequest {
  hostname: string
  node_id: string
  os_template_id: string
  resources: Resources
}

export interface UpdateVMRequest {
  hostname?: string
  resources?: Resources
}

export interface VMActionRequest {
  action: VMAction
}
