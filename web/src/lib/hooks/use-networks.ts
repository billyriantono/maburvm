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
  FirewallEnforcement,
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

// useSetVMRDNS sets or clears the reverse DNS (PTR) of an address the VM holds.
// The VM's owner may call it; for them the server first checks that the
// hostname already resolves to that address.
export function useSetVMRDNS(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<{ ip_address: string; rdns: string }, Error, { ipAddress: string; rdns: string }>({
    mutationFn: async ({ ipAddress, rdns }) => {
      try {
        const response = await api.put<{ ip_address: string; rdns: string }>(`/api/v1/vms/${vmId}/rdns`, {
          ip_address: ipAddress,
          rdns,
        })
        return response.data.data
      } catch (err) {
        // Surface the server's sentence ("x must have a DNS record pointing to
        // y first") rather than "Request failed with status code 422".
        const message = (err as { response?: { data?: { message?: string } } }).response?.data?.message
        throw new Error(message || (err as Error).message)
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['networks', 'vm', vmId] }),
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

export function useSetAntiSpoofing(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { networkId: string; enabled: boolean }>({
    mutationFn: async ({ networkId, enabled }) => {
      await api.put(`/api/v1/vms/${vmId}/networks/${networkId}/anti-spoofing`, { enabled })
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['networks'] }),
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

// useFirewallStatus says whether the VM's node is applying firewall rules at
// all, as the node last reported. The rule list cannot tell.
export function useFirewallStatus(vmId: string) {
  return useQuery<{ enforcement: FirewallEnforcement }>({
    queryKey: ['networks', 'firewall-status', vmId],
    queryFn: async () => {
      const response = await api.get<{ enforcement: FirewallEnforcement }>(`/api/v1/vms/${vmId}/firewall-status`)
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

// useSetFirewallDefault sets what the VM's firewall does with inbound traffic
// no rule matched.
export function useSetFirewallDefault(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, 'drop' | 'accept'>({
    mutationFn: async (value) => {
      await api.put(`/api/v1/vms/${vmId}/firewall-default`, { default: value })
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['vms', 'detail', vmId] }),
  })
}

// useSetSMTPBlocked blocks or allows the VM's outbound mail (administrators).
export function useSetSMTPBlocked(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, boolean>({
    mutationFn: async (blocked) => {
      await api.put(`/api/v1/vms/${vmId}/smtp`, { blocked })
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['vms', 'detail', vmId] }),
  })
}

// useDismissGuestNetworkNote clears the note about an address change the
// guest did not take, once it has been dealt with.
export function useDismissGuestNetworkNote(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.delete(`/api/v1/vms/${vmId}/guest-network-note`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['vms', 'detail', vmId] }),
  })
}

// IPAssignment is one holding of an address (or IPv6 prefix) by a VM.
export interface IPAssignment {
  id: string
  address: string
  vm_id: string | null
  vm_hostname: string
  user_id: string | null
  user_email: string
  node_id: string | null
  kind: 'direct' | 'floating' | 'ipv6_prefix'
  assigned_at: string
  released_at: string | null
  /** Recorded when the history began: the start is the best known date. */
  estimated: boolean
}

// useIPHistory looks up who held an address, optionally at one moment (an
// RFC 3339 time). Disabled until an address is given.
export function useIPHistory(address: string, at: string) {
  return useQuery<IPAssignment[]>({
    queryKey: ['ip-history', address, at],
    queryFn: async () => {
      const params = new URLSearchParams({ address })
      if (at) params.set('at', at)
      const response = await api.get<IPAssignment[]>(`/api/v1/ip-history?${params.toString()}`)
      return response.data.data
    },
    enabled: !!address,
    retry: false,
  })
}
