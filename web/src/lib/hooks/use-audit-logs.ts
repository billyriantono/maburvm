import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { AuditLog, AuditLogFilter, PaginatedResponse } from '@/types'

// useVMActivity fetches the audit_logs entries for a single VM, newest-first.
export function useVMActivity(vmId: string) {
  return useQuery<AuditLog[]>({
    queryKey: ['vm-activity', vmId],
    queryFn: async () => {
      const response = await api.get<AuditLog[]>(`/api/v1/vms/${vmId}/activity`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

interface AuditLogParams extends AuditLogFilter {
  page?: number
  pageSize?: number
}

export function useAuditLogs(filters: AuditLogParams = {}) {
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
    queryFn: async () => {
      const response = await api.get<PaginatedResponse<AuditLog>>('/api/v1/audit-logs', {
        params: {
          user_id,
          action,
          resource_type,
          start_date,
          end_date,
          page,
          page_size: pageSize,
        },
      })
      return response.data.data
    },
  })
}
