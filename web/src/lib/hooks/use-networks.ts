import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  Network,
  ManagedNetwork,
  CreateManagedNetworkRequest,
  PortForward,
  FirewallRule,
  CreatePortForwardRequest,
  CreateFirewallRuleRequest,
} from '@/types'

// useNetworks lists administrator-defined managed networks (bridge/NAT/isolated).
export function useNetworks() {
  return useQuery<ManagedNetwork[]>({
    queryKey: ['networks', 'list'],
    queryFn: async () => {
      const response = await api.get<ManagedNetwork[]>('/api/v1/networks')
      return response.data.data
    },
  })
}

export function useCreateNetwork() {
  const queryClient = useQueryClient()
  return useMutation<ManagedNetwork, Error, CreateManagedNetworkRequest>({
    mutationFn: async (data) => {
      const response = await api.post<ManagedNetwork>('/api/v1/networks', data)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['networks', 'list'] }),
  })
}

export function useDeleteNetwork() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/networks/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['networks', 'list'] }),
  })
}

export function useVMNetworks(vmId: string) {
  return useQuery<Network[]>({
    queryKey: ['networks', 'vm', vmId],
    queryFn: async () => {
      const response = await api.get<Network[]>(`/api/v1/vms/${vmId}/networks`)
      return response.data.data
    },
    enabled: !!vmId,
  })
}

// useSetVMBandwidth sets the speed limit (Mbps) of a single VM network interface.
// 0 = unlimited, max 10000 (10 Gbps). Backed by PUT
// /api/v1/vms/:id/networks/:network_id/bandwidth, which re-applies the tc limit
// on the hypervisor. Ownership is enforced server-side, so clients may call it
// for their own VMs.
export function useSetVMBandwidth(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { networkId: string; bandwidthMbps: number }>({
    mutationFn: async ({ networkId, bandwidthMbps }) => {
      await api.put(`/api/v1/vms/${vmId}/networks/${networkId}/bandwidth`, {
        bandwidth_limit: bandwidthMbps,
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['networks', 'vm', vmId] })
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
