import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import { accountParams, useActingAccount } from '@/lib/acting-account'
import type { VPC, CreateVPCRequest } from '@/types'

const KEY = ['vpcs']

export function useVPCs() {
  const account = useActingAccount()
  return useQuery<VPC[]>({
    queryKey: [...KEY, 'list', account ?? 'own'],
    queryFn: async () => {
      const response = await api.get<VPC[]>('/api/v1/vpcs', accountParams(account))
      return response.data.data ?? []
    },
  })
}

export function useCreateVPC() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<VPC, Error, CreateVPCRequest>({
    mutationFn: async (data) => {
      const response = await api.post<VPC>('/api/v1/vpcs', data, accountParams(account))
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

// Deleting is refused while VMs are still in the network, so the error surfaced
// here is meaningful rather than a generic failure.
export function useDeleteVPC() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vpcs/${id}`, accountParams(account))
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

// Changing a VPC's resolvers reaches its VMs at their next setup (create,
// reinstall, address change).
export function useUpdateVPC() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<VPC, Error, { id: string; nameservers: string }>({
    mutationFn: async ({ id, nameservers }) => (await api.put<VPC>(`/api/v1/vpcs/${id}`, { nameservers }, accountParams(account))).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}
