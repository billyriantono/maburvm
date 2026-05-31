import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { QuotaStatus, SetQuotaRequest, UserQuota } from '@/types/quota'

// useMyQuota returns the current user's limits and usage.
export function useMyQuota() {
  return useQuery<QuotaStatus>({
    queryKey: ['quota', 'me'],
    queryFn: async () => (await api.get<QuotaStatus>('/api/v1/quota')).data.data,
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
