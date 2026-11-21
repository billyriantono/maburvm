// Customer teams: members let into an account, projects that group its VMs,
// and the customer area's settings.

export type MemberRole = 'admin' | 'technical' | 'billing' | 'readonly'

/** What each role may do, in the words the team page shows. */
export const memberRoles: { value: MemberRole; label: string; detail: string }[] = [
  { value: 'admin', label: 'Admin', detail: 'Everything the owner does, including the team and projects.' },
  { value: 'technical', label: 'Technical', detail: 'Runs the servers: power, console, reinstall, backups, networking, ordering.' },
  { value: 'billing', label: 'Billing', detail: 'Usage and billing only. Does not see the servers.' },
  { value: 'readonly', label: 'Read-only', detail: 'Sees the servers and the account\'s resources, changes nothing.' },
]

export interface AccountMember {
  id: string
  account_id: string
  /** Empty while the invitation is open. */
  user_id: string | null
  email: string
  name: string
  role: MemberRole
  /** Both empty: every server of the account. */
  vm_ids: string[]
  project_ids: string[]
  invite_expires_at?: string
  accepted_at?: string
  created_at: string
}

export interface MemberInput {
  account_id?: string
  email?: string
  role: MemberRole
  vm_ids: string[]
  project_ids: string[]
}

/** One account the signed-in person works in. */
export interface Membership {
  id: string
  account_id: string
  account_email: string
  account_name: string
  role: MemberRole
  vm_ids: string[]
  project_ids: string[]
  /** view and resources: the account's own images, floating IPs, VPCs, firewall groups, SSH keys and recipes. */
  can: ('operate' | 'manage' | 'billing' | 'view' | 'resources')[]
}

export interface Memberships {
  memberships: Membership[]
  /** This account was made by accepting an invitation and orders nothing of its own. */
  member_only: boolean
}

export interface Project {
  id: string
  account_id: string
  name: string
  description: string
  vm_count: number
}

export interface InvitationView {
  email: string
  account_name: string
  role: MemberRole
  role_words: string
  has_account: boolean
}

export interface CustomerISO {
  id: string
  name: string
  description: string
}

export type IPRequestMode = 'off' | 'approval' | 'auto'

/** What the customer area offers. */
export interface CustomerPortal {
  support_url: string
  support_email: string
  billing_url: string
  status_url: string
  isos: CustomerISO[]
  iso_url_allowed: boolean
  ip_requests: IPRequestMode
  graph_days: number
  /** How far back graphs go at all, in daily averages. */
  history_days?: number
}

/** The saved "portal" section of the system settings, as staff edit it. */
export interface PortalSettings {
  billingUrl: string
  statusUrl: string
  isos: { id?: string; name: string; url: string; description: string }[]
  isoUrlAllowed: boolean | null
  ipRequests: '' | IPRequestMode
  graphHistoryDays: number
}

export type IPRequestStatus = 'pending' | 'granting' | 'granted' | 'rejected' | 'canceled'

export interface IPRequest {
  id: string
  vm_id: string
  user_id: string
  family: 'ipv4' | 'ipv6'
  reason: string
  status: IPRequestStatus
  address: string
  note: string
  decided_at?: string
  created_at: string
  hostname?: string
  email?: string
}
