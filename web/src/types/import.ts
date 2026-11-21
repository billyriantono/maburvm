// The import of a whole installation from the panel an operator moves from.

export interface ImportOptions {
  default_template_id: string
  /** Source server id to node id, or "-" to leave its VMs out. */
  node_map?: Record<string, string>
  /** Source plan id to panel plan id, or "new". */
  plan_map?: Record<string, string>
  /** Source VM id to the billing system's service reference. */
  billing_refs?: Record<string, string>
}

export interface ImportSource {
  id: string
  kind: string
  name: string
  base_url: string
  tls_fingerprint: string
  options: ImportOptions
  snapshot_at?: string | null
  created_at: string
  updated_at: string
  has_credentials: boolean
  imported: Record<string, number>
}

export interface ImportSourceInput {
  name: string
  base_url: string
  api_key: string
  api_pass: string
  tls_fingerprint: string
}

export interface SourceTestResult {
  ok: boolean
  servers: number
  message: string
  fingerprint?: string
}

export type ImportKind = "pool" | "plan" | "user" | "vm"
export type ImportAction = "create" | "link" | "update" | "exists" | "blocked"

export interface ImportVMDetail {
  owner_key: string
  owner_label: string
  plan_key?: string
  plan_label?: string
  node_id?: string
  node_name?: string
  domain_uuid?: string
  hostname: string
  template_id?: string
  template_label?: string
  ips?: string[]
  billing_ref?: string
  suspended?: boolean
}

export interface ImportPoolDetail {
  cidr: string
  family: string
  gateway?: string
  dns?: string[]
  node_ids?: string[]
  total: number
  in_use: number
}

export interface ImportPlanDetail {
  name: string
  cpu: number
  ram_mb: number
  disk_gb: number
  bandwidth_mbps: number
  data_quota_gb: number
}

export interface ImportItem {
  kind: ImportKind
  key: string
  label: string
  detail?: string
  action: ImportAction
  target_id?: string
  target_label?: string
  conflicts?: string[]
  notes?: string[]
  vm?: ImportVMDetail
  pool?: ImportPoolDetail
  plan?: ImportPlanDetail
}

export interface ImportServerRow {
  id: string
  name: string
  ip: string
  node_id?: string
  node_name?: string
  matched: "auto" | "chosen" | "skipped" | ""
  vms: number
  scan_error?: string
}

export interface ImportChoice {
  id: string
  label: string
}

export interface ImportPlan {
  source_id: string
  fetched_at: string
  options: ImportOptions
  servers: ImportServerRow[] | null
  items: ImportItem[]
  choices: { nodes: ImportChoice[] | null; templates: ImportChoice[] | null; plans: ImportChoice[] | null }
  notes?: string[]
  pending_invites: number
  ready: boolean
  reading: boolean
  read_error?: string
  read_started_at?: string
}

export interface ImportItemRef {
  kind: ImportKind
  key: string
}

export interface ImportApplyResult {
  kind: ImportKind
  key: string
  label: string
  status: "created" | "linked" | "updated" | "skipped" | "failed"
  target_id?: string
  message?: string
}
