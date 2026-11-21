import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, type ApiResponse } from '@/lib/api-client'
import type { APIKey, CreateAPIKeyRequest, CreatedAPIKey } from '@/types/api-key'

/** The caller's keys, and whether the panel accepts API keys at all (Settings, System, API). */
export function useAPIKeys() {
  return useQuery<{ keys: APIKey[]; apiEnabled: boolean }>({
    queryKey: ['api-keys', 'list'],
    queryFn: async () => {
      const response = await api.get<APIKey[]>('/api/v1/api-keys')
      const body = response.data as ApiResponse<APIKey[]> & { api_enabled?: boolean }
      return { keys: body.data, apiEnabled: body.api_enabled !== false }
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
