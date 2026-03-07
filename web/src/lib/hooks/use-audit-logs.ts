import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { AuditLog, AuditLogFilters, PaginatedResponse } from '@/types'

export function useAuditLogs(filters: AuditLogFilters = {}) {
  const {
    user_id,
    action,
    resource_type,
    start_date,
    end_date,
    page = 1,
    pageSize = 20,
  } = filters

  return useQuery<PaginatedResponse<AuditLog>>({
    queryKey: [
      'audit-logs',
      'list',
      { user_id, action, resource_type, start_date, end_date, page, pageSize },
    ],
    queryFn: () =>
      api.get<PaginatedResponse<AuditLog>>('/api/v1/audit-logs', {
        user_id,
        action,
        resource_type,
        start_date,
        end_date,
        page,
        page_size: pageSize,
      }),
  })
}
