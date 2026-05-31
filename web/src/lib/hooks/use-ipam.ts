import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  AllocateIPAddressRequest,
  CreateIPAddressRequest,
  CreateIPPoolRequest,
  IPAddress,
  IPPool,
} from '@/types'

export function useIPPools() {
  return useQuery<IPPool[]>({
    queryKey: ['ipam', 'pools'],
    queryFn: async () => {
      const response = await api.get<IPPool[]>('/api/v1/ip-pools')
      return response.data.data
    },
  })
}

export function useIPPool(id?: string) {
  return useQuery<IPPool>({
    queryKey: ['ipam', 'pools', id],
    queryFn: async () => {
      const response = await api.get<IPPool>(`/api/v1/ip-pools/${id}`)
      return response.data.data
    },
    enabled: !!id,
  })
}

export function useCreateIPPool() {
  const queryClient = useQueryClient()

  return useMutation<IPPool, Error, CreateIPPoolRequest>({
    mutationFn: async (data) => {
      const response = await api.post<IPPool>('/api/v1/ip-pools', data)
      return response.data.data
    },
    onSuccess: (pool) => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'pools'] })
      queryClient.setQueryData(['ipam', 'pools', pool.id], pool)
    },
  })
}

export function useDeleteIPPool() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/ip-pools/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'pools'] })
    },
  })
}

export function useIPAddresses(poolId?: string) {
  return useQuery<IPAddress[]>({
    queryKey: ['ipam', 'addresses', poolId],
    queryFn: async () => {
      const response = await api.get<IPAddress[]>(`/api/v1/ip-pools/${poolId}/addresses`)
      return response.data.data
    },
    enabled: !!poolId,
  })
}

export function useAddIPAddress(poolId?: string) {
  const queryClient = useQueryClient()

  return useMutation<IPAddress, Error, CreateIPAddressRequest>({
    mutationFn: async (data) => {
      const response = await api.post<IPAddress>(`/api/v1/ip-pools/${poolId}/addresses`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'addresses', poolId] })
    },
  })
}

export function useAllocateIPAddress(poolId?: string) {
  const queryClient = useQueryClient()

  return useMutation<IPAddress, Error, AllocateIPAddressRequest>({
    mutationFn: async (data) => {
      const response = await api.post<IPAddress>(`/api/v1/ip-pools/${poolId}/allocate`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'addresses', poolId] })
    },
  })
}

export function useReleaseIPAddress(poolId?: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (addressId) => {
      await api.post(`/api/v1/ip-pools/addresses/${addressId}/release`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'addresses', poolId] })
    },
  })
}

// useSetRDNS sets or clears the reverse-DNS (PTR) hostname for an address.
export function useSetRDNS(poolId?: string) {
  const queryClient = useQueryClient()

  return useMutation<IPAddress, Error, { addressId: string; rdns: string }>({
    mutationFn: async ({ addressId, rdns }) => {
      const response = await api.put<IPAddress>(`/api/v1/ip-pools/addresses/${addressId}/rdns`, { rdns })
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'addresses', poolId] })
    },
  })
}

// useImportRDNS pulls existing PTR records from the nameserver into a pool's
// addresses (read-only adoption; never pushes back). Returns the count imported.
export function useImportRDNS() {
  const queryClient = useQueryClient()
  return useMutation<number, Error, string>({
    mutationFn: async (poolId) => {
      const response = await api.post<{ imported: number }>(`/api/v1/ip-pools/${poolId}/rdns-import`)
      return (response.data.data as unknown as { imported: number }).imported
    },
    onSuccess: (_count, poolId) => {
      queryClient.invalidateQueries({ queryKey: ['ipam', 'addresses', poolId] })
    },
  })
}

// downloadReverseZone fetches a pool's PTR zone fragment and saves it as a file.
export async function downloadReverseZone(poolId: string, poolName: string) {
  const response = await api.get<string>(`/api/v1/ip-pools/${poolId}/rdns-zone`)
  const text = response.data as unknown as string
  const blob = new Blob([text], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${poolName || 'pool'}-rdns.zone`
  a.click()
  URL.revokeObjectURL(url)
}
