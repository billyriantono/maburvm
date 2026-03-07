export interface AuditLog {
  id: string
  user_id?: string
  action: string
  resource_type?: string
  resource_id?: string
  ip_address?: string
  user_agent?: string
  details: Record<string, unknown>
  before_snapshot?: Record<string, unknown>
  after_snapshot?: Record<string, unknown>
  created_at: string
}

export type AuditAction =
  | 'user_created'
  | 'user_updated'
  | 'user_deleted'
  | 'user_login'
  | 'user_logout'
  | 'vm_created'
  | 'vm_updated'
  | 'vm_deleted'
  | 'vm_started'
  | 'vm_stopped'
  | 'vm_restarted'
  | 'vm_rebuilt'
  | 'vm_suspended'
  | 'vm_resumed'
  | 'node_created'
  | 'node_updated'
  | 'node_deleted'
  | 'backup_created'
  | 'backup_deleted'
  | 'snapshot_created'
  | 'snapshot_restored'
  | 'snapshot_deleted'
  | 'firewall_rule_created'
  | 'firewall_rule_updated'
  | 'firewall_rule_deleted'
  | 'port_forward_created'
  | 'port_forward_deleted'

export interface AuditLogFilters {
  user_id?: string
  action?: string
  resource_type?: string
  start_date?: string
  end_date?: string
  page?: number
  pageSize?: number
}
