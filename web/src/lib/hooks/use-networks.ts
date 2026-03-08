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
    queryFn: async () => {
      const response = await api.get<Network[]>('/api/v1/networks')
      return response.data.data
    },
  })
}

export function usePortForwards(vmId: string) {
  return useQuery<PortForward[]>({
    queryKey: ['networks', 'port-forwards', vmId],
    queryFn: async () => {
      const response = await api.get<PortForward[]>(`/api/v1/vms/${vmId}/port-forwards`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

export function useFirewallRules(vmId: string) {
  return useQuery<FirewallRule[]>({
    queryKey: ['networks', 'firewall-rules', vmId],
    queryFn: async () => {
      const response = await api.get<FirewallRule[]>(`/api/v1/vms/${vmId}/firewall-rules`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

export function useCreatePortForward(vmId: string) {
  const queryClient = useQueryClient()

  return useMutation<PortForward, Error, CreatePortForwardRequest>({
    mutationFn: async (data) => {
      const response = await api.post<PortForward>(`/api/v1/vms/${vmId}/port-forwards`, data)
      return response.data.data
    },
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
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vms/${vmId}/port-forwards/${id}`)
    },
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
    mutationFn: async (data) => {
      const response = await api.post<FirewallRule>(`/api/v1/vms/${vmId}/firewall-rules`, data)
      return response.data.data
    },
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
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vms/${vmId}/firewall-rules/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['networks', 'firewall-rules', vmId],
      })
    },
  })
}
