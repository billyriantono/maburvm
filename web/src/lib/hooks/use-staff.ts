import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { AccountSession, PermissionArea, SignInEvent, StaffRole } from '@/types'

export interface StaffRolesData {
  areas: PermissionArea[]
  roles: StaffRole[]
  /** Whether the signed-in account may change roles. */
  can_manage: boolean
}

export function useStaffRoles(enabled = true) {
  return useQuery<StaffRolesData>({
    queryKey: ['staff-roles'],
    queryFn: async () => (await api.get<StaffRolesData>('/api/v1/staff-roles')).data.data,
    enabled,
  })
}

export interface StaffRoleInput {
  id?: string
  name: string
  description: string
  permissions: string[]
}

export function useSaveStaffRole() {
  const qc = useQueryClient()
  return useMutation<StaffRole, Error, StaffRoleInput>({
    mutationFn: async ({ id, ...body }) =>
      (id ? await api.put<StaffRole>(`/api/v1/staff-roles/${id}`, body) : await api.post<StaffRole>('/api/v1/staff-roles', body)).data.data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['staff-roles'] }),
  })
}

export function useDeleteStaffRole() {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/staff-roles/${id}`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['staff-roles'] }),
  })
}

export function useAssignStaffRole(userId: string) {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (role) => {
      await api.put(`/api/v1/users/${userId}/staff-role`, { role })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      qc.invalidateQueries({ queryKey: ['staff-roles'] })
    },
  })
}

/** Opens the panel as the customer sees it. The browser is sent there after. */
export function useImpersonate(userId: string) {
  return useMutation<{ redirect: string }, Error, void>({
    mutationFn: async () => (await api.post<{ redirect: string }>(`/api/v1/users/${userId}/impersonate`)).data.data,
  })
}

/** Ends a view as a customer and returns to the staff session. */
export function useEndImpersonation() {
  return useMutation<{ redirect: string }, Error, void>({
    mutationFn: async () => (await api.post<{ redirect: string }>('/api/v1/auth/impersonation/end')).data.data,
  })
}

// Sessions and sign-in history: one's own (userId undefined) or, for staff,
// another account's.
const base = (userId?: string) => (userId ? `/api/v1/users/${userId}` : '/api/v1/settings')

export function useAccountSessions(userId?: string) {
  return useQuery<AccountSession[]>({
    queryKey: ['sessions', userId ?? 'me'],
    queryFn: async () => (await api.get<AccountSession[]>(`${base(userId)}/sessions`)).data.data,
  })
}

export function useSignInHistory(userId?: string) {
  return useQuery<SignInEvent[]>({
    queryKey: ['sign-ins', userId ?? 'me'],
    queryFn: async () => (await api.get<SignInEvent[]>(`${base(userId)}/sign-ins`)).data.data,
  })
}

export function useRevokeSession(userId?: string) {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (sessionId) => {
      await api.delete(`${base(userId)}/sessions/${sessionId}`)
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ['sessions', userId ?? 'me'] }),
  })
}

/** One's own: every session but this one. Staff: every session of the account. */
export function useRevokeSessions(userId?: string) {
  const qc = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(userId ? `${base(userId)}/sessions/revoke-all` : `${base()}/sessions/revoke-others`)
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ['sessions', userId ?? 'me'] }),
  })
}
