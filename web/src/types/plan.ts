import type { VMLimits } from './vm'

// Plan is a VPS flavor: a named bundle of resources.
export type OverQuotaPolicy = 'throttle' | 'overage' | 'suspend'
/** Whether new VMs on the plan may send mail out on port 25. */
export type SMTPPolicy = 'allowed' | 'blocked'
export type PlanNetworkType = 'public' | 'nat'

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
  /** Display price, shown to customers; null = not shown. */
  price_monthly?: number | null
  price_hourly?: number | null
  /** ISO 4217 code, empty when no price is set. */
  currency?: string
  /** Per-VM allowances, read whenever one is used. null = no limit, 0 = none. */
  max_snapshots?: number | null
  max_backups?: number | null
  max_extra_ipv4?: number | null
  /** A VM holds one IPv6 at most, so 0 (none may be added) or 1. */
  max_extra_ipv6?: number | null
  /** Firewall group new VMs get instead of the built-in rules; null keeps those. */
  default_firewall_group_id?: string | null
  /** nat: a private address and a block of nat_ports public ports per VM. */
  network_type?: PlanNetworkType
  nat_ports?: number
  nat_hostnames?: number
  description?: string
  is_active: boolean
  created_at: string
  updated_at: string
}

/** What applying a plan's current settings does, or did, to one VM. */
export interface PlanRolloutVM {
  id: string
  hostname: string
  status: string
  changes: string[]
  /** The new size waits until the VM is stopped; it is never stopped for it. */
  needs_stop: boolean
  notes: string[]
  outcome?: 'applied' | 'partial' | 'failed' | 'up_to_date' | 'skipped'
  errors?: string[]
}

export interface PlanRollout {
  plan_id: string
  plan_name: string
  applied: boolean
  vms: PlanRolloutVM[]
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
  price_monthly?: number | null
  price_hourly?: number | null
  currency?: string
  max_snapshots?: number | null
  max_backups?: number | null
  max_extra_ipv4?: number | null
  max_extra_ipv6?: number | null
  /** "" goes back to the built-in rules; left out keeps the plan's choice. */
  default_firewall_group_id?: string
  network_type?: PlanNetworkType
  nat_ports?: number
  nat_hostnames?: number
  description?: string
  is_active?: boolean
}
