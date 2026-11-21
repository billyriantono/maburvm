import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import { accountParams, useActingAccount } from '@/lib/acting-account'
import type { SSHKey, CreateSSHKeyRequest, GenerateSSHKeyRequest, GeneratedSSHKey } from '@/types/ssh-key'

/** accountId lists another account's in place of the page's acting account. */
export function useSSHKeys(accountId?: string, enabled = true) {
  const acting = useActingAccount()
  const account = accountId ?? acting
  return useQuery<SSHKey[]>({
    enabled,
    queryKey: ['ssh-keys', 'list', account ?? 'own'],
    queryFn: async () => {
      const response = await api.get<SSHKey[]>('/api/v1/ssh-keys', accountParams(account))
      return response.data.data
    },
  })
}

export function useCreateSSHKey() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<SSHKey, Error, CreateSSHKeyRequest>({
    mutationFn: async (data) => {
      const response = await api.post<SSHKey>('/api/v1/ssh-keys', data, accountParams(account))
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys', 'list'] })
    },
  })
}

export function useGenerateSSHKey() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<GeneratedSSHKey, Error, GenerateSSHKeyRequest>({
    mutationFn: async (data) => {
      const response = await api.post<GeneratedSSHKey>('/api/v1/ssh-keys/generate', data, accountParams(account))
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys', 'list'] })
    },
  })
}

export function useDeleteSSHKey() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/ssh-keys/${id}`, accountParams(account))
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys', 'list'] })
    },
  })
}
