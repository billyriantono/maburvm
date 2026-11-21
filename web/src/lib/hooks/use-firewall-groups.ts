import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import { accountParams, useActingAccount } from '@/lib/acting-account'
import type {
  AttachedFirewallGroup,
  CreateFirewallRuleRequest,
  FirewallGroup,
  FirewallGroupDetail,
  FirewallGroupPosition,
  FirewallGroupRule,
  NodeNATSettings,
  VMHostname,
  VMNATInfo,
} from '@/types'

const GROUPS = ['firewall-groups']

/**
 * Groups the caller may see; scope 'global' narrows staff to groups for
 * everyone. accountId names the account whose groups to list in place of the
 * page's acting account (a VM's own account, for a team member).
 */
export function useFirewallGroups(scope?: 'global', enabled = true, accountId?: string) {
  const acting = useActingAccount()
  const account = accountId ?? acting
  return useQuery<FirewallGroup[]>({
    queryKey: [...GROUPS, scope ?? 'all', account ?? 'own'],
    queryFn: async () => {
      const response = await api.get<FirewallGroup[]>('/api/v1/firewall-groups' + (scope ? '?scope=global' : ''), accountParams(account))
      return response.data.data ?? []
    },
    enabled,
  })
}

export function useFirewallGroup(id: string | null) {
  const account = useActingAccount()
  return useQuery<FirewallGroupDetail>({
    queryKey: [...GROUPS, 'detail', id, account ?? 'own'],
    queryFn: async () => {
      const response = await api.get<FirewallGroupDetail>(`/api/v1/firewall-groups/${id}`, accountParams(account))
      return response.data.data
    },
    enabled: !!id,
  })
}

export function useSaveFirewallGroup() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<FirewallGroup, Error, { id?: string; name: string; description: string }>({
    mutationFn: async ({ id, ...body }) => {
      const response = id
        ? await api.put<FirewallGroup>(`/api/v1/firewall-groups/${id}`, body, accountParams(account))
        : await api.post<FirewallGroup>('/api/v1/firewall-groups', body, accountParams(account))
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: GROUPS }),
  })
}

// Refused while the group is on a VM or a plan's default; the error says which.
export function useDeleteFirewallGroup() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/firewall-groups/${id}`, accountParams(account))
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: GROUPS }),
  })
}

export function useAddFirewallGroupRule(groupId: string) {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<FirewallGroupRule, Error, Omit<CreateFirewallRuleRequest, 'vm_id'>>({
    mutationFn: async (rule) => {
      const response = await api.post<FirewallGroupRule>(`/api/v1/firewall-groups/${groupId}/rules`, rule, accountParams(account))
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: GROUPS }),
  })
}

export function useDeleteFirewallGroupRule(groupId: string) {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (ruleId) => {
      await api.delete(`/api/v1/firewall-groups/${groupId}/rules/${ruleId}`, accountParams(account))
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: GROUPS }),
  })
}

const vmGroupsKey = (vmId: string) => ['vms', vmId, 'firewall-groups']

export function useVMFirewallGroups(vmId: string) {
  return useQuery<AttachedFirewallGroup[]>({
    queryKey: vmGroupsKey(vmId),
    queryFn: async () => {
      const response = await api.get<AttachedFirewallGroup[]>(`/api/v1/vms/${vmId}/firewall-groups`)
      return response.data.data ?? []
    },
    enabled: !!vmId,
  })
}

export function useAttachFirewallGroup(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { group_id: string; position: FirewallGroupPosition }>({
    mutationFn: async (body) => {
      await api.post(`/api/v1/vms/${vmId}/firewall-groups`, body)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: vmGroupsKey(vmId) })
      queryClient.invalidateQueries({ queryKey: GROUPS })
    },
  })
}

export function useDetachFirewallGroup(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (groupId) => {
      await api.delete(`/api/v1/vms/${vmId}/firewall-groups/${groupId}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: vmGroupsKey(vmId) })
      queryClient.invalidateQueries({ queryKey: GROUPS })
    },
  })
}

const natKey = (vmId: string) => ['vms', vmId, 'nat']

export function useVMNAT(vmId: string) {
  return useQuery<VMNATInfo>({
    queryKey: natKey(vmId),
    queryFn: async () => {
      const response = await api.get<VMNATInfo>(`/api/v1/vms/${vmId}/nat`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

export function useAddHostname(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<VMHostname, Error, { hostname: string; http_port: number; https_port: number }>({
    mutationFn: async (body) => {
      const response = await api.post<VMHostname>(`/api/v1/vms/${vmId}/hostnames`, body)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: natKey(vmId) }),
  })
}

export function useVerifyHostname(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<VMHostname, Error, string>({
    mutationFn: async (id) => {
      const response = await api.post<VMHostname>(`/api/v1/vms/${vmId}/hostnames/${id}/verify`)
      return response.data.data
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: natKey(vmId) }),
  })
}

export function useRemoveHostname(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vms/${vmId}/hostnames/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: natKey(vmId) }),
  })
}

const nodeNATKey = (nodeId: string) => ['nodes', nodeId, 'nat']

export function useNodeNAT(nodeId: string) {
  return useQuery<NodeNATSettings>({
    queryKey: nodeNATKey(nodeId),
    queryFn: async () => {
      const response = await api.get<NodeNATSettings>(`/api/v1/nodes/${nodeId}/nat`)
      return response.data.data
    },
    enabled: !!nodeId,
  })
}

export function useSetNodeNAT(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation<NodeNATSettings, Error, { port_start: number; port_end: number }>({
    mutationFn: async (body) => {
      const response = await api.put<NodeNATSettings>(`/api/v1/nodes/${nodeId}/nat`, body)
      return response.data.data
    },
    onSuccess: (data) => queryClient.setQueryData(nodeNATKey(nodeId), data),
  })
}
