import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { AccountMember, InvitationView, MemberInput, Memberships, Project } from '@/types/team'

const params = (accountId?: string) => (accountId ? { params: { account_id: accountId } } : undefined)

/** The accounts the signed-in customer works in. */
export function useMemberships() {
  return useQuery<Memberships>({
    queryKey: ['team', 'memberships'],
    queryFn: async () => (await api.get<Memberships>('/api/v1/account/memberships')).data.data,
  })
}

export function useMembers(accountId?: string, enabled = true) {
  return useQuery<AccountMember[]>({
    queryKey: ['team', 'members', accountId ?? 'own'],
    queryFn: async () => (await api.get<AccountMember[]>('/api/v1/account/members', params(accountId))).data.data,
    enabled,
  })
}

export function useProjects(accountId?: string, enabled = true) {
  return useQuery<Project[]>({
    queryKey: ['team', 'projects', accountId ?? 'own'],
    queryFn: async () => (await api.get<Project[]>('/api/v1/account/projects', params(accountId))).data.data,
    enabled,
  })
}

function useTeamMutation<V, R = unknown>(fn: (v: V) => Promise<R>) {
  const qc = useQueryClient()
  return useMutation<R, Error, V>({
    mutationFn: fn,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['team'] })
      qc.invalidateQueries({ queryKey: ['vms'] })
    },
  })
}

export function useInviteMember() {
  return useTeamMutation(async (v: MemberInput) => {
    const res = await api.post<AccountMember>('/api/v1/account/members', v)
    return res.data as unknown as { data: AccountMember; warning?: string }
  })
}

export function useUpdateMember() {
  return useTeamMutation(async ({ id, ...v }: MemberInput & { id: string }) =>
    (await api.put<AccountMember>(`/api/v1/account/members/${id}`, v)).data.data)
}

export function useRemoveMember() {
  return useTeamMutation(async ({ id, accountId }: { id: string; accountId?: string }) => {
    await api.delete(`/api/v1/account/members/${id}`, params(accountId))
  })
}

export function useLeaveAccount() {
  return useTeamMutation(async (membershipId: string) => {
    await api.delete(`/api/v1/account/memberships/${membershipId}`)
  })
}

export function useSaveProject() {
  return useTeamMutation(async ({ id, ...v }: { id?: string; account_id?: string; name: string; description: string }) =>
    id ? (await api.put<Project>(`/api/v1/account/projects/${id}`, v)).data.data
      : (await api.post<Project>('/api/v1/account/projects', v)).data.data)
}

export function useDeleteProject() {
  return useTeamMutation(async ({ id, accountId }: { id: string; accountId?: string }) => {
    await api.delete(`/api/v1/account/projects/${id}`, params(accountId))
  })
}

export function useSetVMProject(vmId: string) {
  return useTeamMutation(async (projectId: string) => {
    await api.put(`/api/v1/vms/${vmId}/project`, { project_id: projectId })
  })
}

export function useInvitation(token: string) {
  return useQuery<InvitationView>({
    queryKey: ['invitation', token],
    // The token goes in the body so it stays out of request logs.
    queryFn: async () => (await api.post<InvitationView>('/api/v1/account/invitations/lookup', { token })).data.data,
    enabled: !!token,
    retry: false,
  })
}

export function useAcceptInvitation() {
  return useMutation<{ email: string }, Error, { token: string; name?: string; password?: string }>({
    mutationFn: async (v) => (await api.post<{ email: string }>('/api/v1/account/invitations/accept', v)).data.data,
  })
}

/** A customer's team, projects and memberships, for the admin user page. */
export function useUserTeam(userId: string) {
  return useQuery<{ members: AccountMember[]; projects: Project[]; memberships: Memberships['memberships']; member_only: boolean }>({
    queryKey: ['team', 'staff', userId],
    queryFn: async () => (await api.get<{ members: AccountMember[]; projects: Project[]; memberships: Memberships['memberships']; member_only: boolean }>(`/api/v1/users/${userId}/team`)).data.data,
    enabled: !!userId,
  })
}

export function useSetMemberOnly(userId: string) {
  return useTeamMutation(async (memberOnly: boolean) => {
    await api.put(`/api/v1/users/${userId}/member-only`, { member_only: memberOnly })
  })
}
