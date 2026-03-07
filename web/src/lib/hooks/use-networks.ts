import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  Network,
  PortForward,
  FirewallRule,
  CreatePortForwardRequest,
  CreateFirewallRuleRequest,
} from '@/types'

export function useNetworks() {
  return useQuery<Network[]>({
    queryKey: ['networks', 'list'],
    queryFn: () => api.get<Network[]>('/api/v1/networks'),
  })
}

export function usePortForwards(vmId: string) {
  return useQuery<PortForward[]>({
    queryKey: ['networks', 'port-forwards', vmId],
    queryFn: () =>
      api.get<PortForward[]>(`/api/v1/vms/${vmId}/port-forwards`),
    enabled: !!vmId,
  })
}

export function useFirewallRules(vmId: string) {
  return useQuery<FirewallRule[]>({
    queryKey: ['networks', 'firewall-rules', vmId],
    queryFn: () =>
      api.get<FirewallRule[]>(`/api/v1/vms/${vmId}/firewall-rules`),
    enabled: !!vmId,
  })
}

export function useCreatePortForward(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<PortForward, Error, CreatePortForwardRequest>({
    mutationFn: (data) =>
      api.post<PortForward>(`/api/v1/vms/${vmId}/port-forwards`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['networks', 'port-forwards', vmId],
      })
    },
  })
}

export function useDeletePortForward(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (id) =>
      api.delete(`/api/v1/vms/${vmId}/port-forwards/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['networks', 'port-forwards', vmId],
      })
    },
  })
}

export function useCreateFirewallRule(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<FirewallRule, Error, CreateFirewallRuleRequest>({
    mutationFn: (data) =>
      api.post<FirewallRule>(`/api/v1/vms/${vmId}/firewall-rules`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['networks', 'firewall-rules', vmId],
      })
    },
  })
}

export function useDeleteFirewallRule(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (id) =>
      api.delete(`/api/v1/vms/${vmId}/firewall-rules/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['networks', 'firewall-rules', vmId],
      })
    },
  })
}
