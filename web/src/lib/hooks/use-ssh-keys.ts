import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { SSHKey, CreateSSHKeyRequest } from '@/types/ssh-key'

export function useSSHKeys() {
  return useQuery<SSHKey[]>({
    queryKey: ['ssh-keys', 'list'],
    queryFn: async () => {
      const response = await api.get<SSHKey[]>('/api/v1/ssh-keys')
      return response.data.data
    },
  })
}

export function useCreateSSHKey() {
  const queryClient = useQueryClient()
  return useMutation<SSHKey, Error, CreateSSHKeyRequest>({
    mutationFn: async (data) => {
      const response = await api.post<SSHKey>('/api/v1/ssh-keys', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys', 'list'] })
    },
  })
}

export function useDeleteSSHKey() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/ssh-keys/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys', 'list'] })
    },
  })
}
