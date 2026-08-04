import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { AllocateFloatingIPRequest, IPAddress, NATMode } from '@/types'

const KEY = ['floating-ips']

export function useFloatingIPs() {
  return useQuery<IPAddress[]>({
    queryKey: KEY,
    queryFn: async () => {
      const response = await api.get<IPAddress[]>('/api/v1/floating-ips')
      return response.data.data ?? []
    },
  })
}

export function useAllocateFloatingIP() {
  const queryClient = useQueryClient()
  return useMutation<IPAddress, Error, AllocateFloatingIPRequest>({
    mutationFn: async (data) => {
      const response = await api.post<IPAddress>('/api/v1/floating-ips', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: KEY })
      queryClient.invalidateQueries({ queryKey: ['ipam'] })
    },
  })
}

// Attaching an already-attached floating IP moves it to the new VM; nat_mode may
// be omitted to let the panel pick (inbound for a VM that already has a public
// address, full for one on a private address).
export function useAttachFloatingIP() {
  const queryClient = useQueryClient()
  return useMutation<IPAddress, Error, { id: string; vm_id: string; nat_mode?: NATMode }>({
    mutationFn: async ({ id, ...body }) => {
      const response = await api.post<IPAddress>(`/api/v1/floating-ips/${id}/attach`, body)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

export function useDetachFloatingIP() {
  const queryClient = useQueryClient()
  return useMutation<IPAddress, Error, string>({
    mutationFn: async (id) => {
      const response = await api.post<IPAddress>(`/api/v1/floating-ips/${id}/detach`)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

// Releasing returns the address to its pool as an ordinary allocatable IP. The
// API refuses while it is still attached to a VM.
export function useReleaseFloatingIP() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/floating-ips/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: KEY })
      queryClient.invalidateQueries({ queryKey: ['ipam'] })
    },
  })
}
