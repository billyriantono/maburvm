// UserQuota caps a user's total allocatable resources. 0 = unlimited.
export interface UserQuota {
  user_id: string
  max_vms: number
  max_vcpu: number
  max_ram_mb: number
  max_disk_gb: number
  created_at: string
  updated_at: string
}

// QuotaUsage is the user's current consumption across all their VMs.
export interface QuotaUsage {
  vms: number
  vcpu: number
  ram_mb: number
  disk_gb: number
}

export interface QuotaStatus {
  quota: UserQuota
  usage: QuotaUsage
}

export interface SetQuotaRequest {
  max_vms: number
  max_vcpu: number
  max_ram_mb: number
  max_disk_gb: number
}
