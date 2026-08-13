// AuditLog represents an audit log entry
export interface AuditLog {
  id: string;
  user_id?: string;
  action: string;
  resource_type?: string;
  resource_id?: string;
  ip_address?: string;
  user_agent?: string;
  details: Record<string, unknown>;
  before_snapshot?: Record<string, unknown>;
  after_snapshot?: Record<string, unknown>;
  created_at: string;

  /** Resolved for display; the ids above stay authoritative. */
  user_email?: string;
  resource_name?: string;
}

// AuditLogFilter for filtering audit logs
export interface AuditLogFilter {
  user_id?: string;
  action?: string;
  resource_type?: string;
  resource_id?: string;
  start_date?: string;
  end_date?: string;
}
