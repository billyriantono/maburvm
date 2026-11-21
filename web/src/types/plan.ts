import type { VMLimits } from './vm'

// Plan is a VPS flavor: a named bundle of resources.
export type OverQuotaPolicy = 'throttle' | 'overage' | 'suspend'
/** Whether new VMs on the plan may send mail out on port 25. */
export type SMTPPolicy = 'allowed' | 'blocked'

export interface Plan {
  id: string
  name: string
  cpu: number
  ram: number // MB
  disk: number // GB
  bandwidth_mbps: number
  data_quota_gb: number // monthly transfer, 0 = unlimited
  over_quota_policy: OverQuotaPolicy
  throttle_speed_mbps: number // speed after quota when policy = throttle
  smtp_policy: SMTPPolicy
  /** CPU and disk caps new VMs on the plan get. */
  limits?: VMLimits
  description?: string
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface CreatePlanRequest {
  name: string
  cpu: number
  ram: number
  disk: number
  bandwidth_mbps?: number
  data_quota_gb?: number
  over_quota_policy?: OverQuotaPolicy
  throttle_speed_mbps?: number
  smtp_policy?: SMTPPolicy
  limits?: VMLimits
  description?: string
  is_active?: boolean
}
