import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { APIKey, CreateAPIKeyRequest, CreatedAPIKey } from '@/types/api-key'

export function useAPIKeys() {
  return useQuery<APIKey[]>({
    queryKey: ['api-keys', 'list'],
    queryFn: async () => {
      const response = await api.get<APIKey[]>('/api/v1/api-keys')
      return response.data.data
    },
  })
}

export function useCreateAPIKey() {
  const queryClient = useQueryClient()
  return useMutation<CreatedAPIKey, Error, CreateAPIKeyRequest>({
    mutationFn: async (data) => {
      const response = await api.post<CreatedAPIKey>('/api/v1/api-keys', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['api-keys', 'list'] })
    },
  })
}

export function useRevokeAPIKey() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/api-keys/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['api-keys', 'list'] })
    },
  })
}
