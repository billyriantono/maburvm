import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { VPC, CreateVPCRequest } from '@/types'

const KEY = ['vpcs']

export function useVPCs() {
  return useQuery<VPC[]>({
    queryKey: KEY,
    queryFn: async () => {
      const response = await api.get<VPC[]>('/api/v1/vpcs')
      return response.data.data ?? []
    },
  })
}

export function useCreateVPC() {
  const queryClient = useQueryClient()
  return useMutation<VPC, Error, CreateVPCRequest>({
    mutationFn: async (data) => {
      const response = await api.post<VPC>('/api/v1/vpcs', data)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

// Deleting is refused while VMs are still in the network, so the error surfaced
// here is meaningful rather than a generic failure.
export function useDeleteVPC() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vpcs/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}
