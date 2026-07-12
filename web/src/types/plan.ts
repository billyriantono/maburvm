// Plan is a VPS flavor: a named bundle of resources.
export type OverQuotaPolicy = 'throttle' | 'overage' | 'suspend'

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
  description?: string
  is_active?: boolean
}
