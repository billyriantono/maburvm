import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { QuotaStatus, SetQuotaRequest, UserQuota } from '@/types/quota'

// useMyQuota returns the current user's limits and usage, or with accountId
// those of a team account whose usage their role covers.
export function useMyQuota(accountId?: string, enabled = true) {
  return useQuery<QuotaStatus>({
    queryKey: ['quota', accountId ?? 'me'],
    queryFn: async () => (await api.get<QuotaStatus>('/api/v1/quota', accountId ? { params: { account_id: accountId } } : undefined)).data.data,
    enabled,
  })
}

// What customers may do on their own; the operator sets it in system settings.
export interface SelfService {
  order: boolean
  clone: boolean
  extra_disk: boolean
}

export function useSelfService() {
  return useQuery<SelfService>({
    queryKey: ['self-service'],
    queryFn: async () => (await api.get<SelfService>('/api/v1/self-service')).data.data,
  })
}

// useUserQuota returns a specific user's limits and usage (admin only).
export function useUserQuota(userId: string) {
  return useQuery<QuotaStatus>({
    queryKey: ['quota', 'user', userId],
    enabled: !!userId,
    queryFn: async () => (await api.get<QuotaStatus>(`/api/v1/users/${userId}/quota`)).data.data,
  })
}

// useSetUserQuota updates a user's quota (admin only).
export function useSetUserQuota() {
  const queryClient = useQueryClient()
  return useMutation<UserQuota, Error, { userId: string; data: SetQuotaRequest }>({
    mutationFn: async ({ userId, data }) =>
      (await api.put<UserQuota>(`/api/v1/users/${userId}/quota`, data)).data.data,
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: ['quota', 'user', vars.userId] })
    },
  })
}
