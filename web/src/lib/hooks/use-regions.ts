import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Region, CreateRegionRequest } from '@/types'

const KEY = ['regions']

// The API returns the flag glyph with each region, so no client maps country
// codes to icons or ships an icon set of its own.
export function useRegions() {
  return useQuery<Region[]>({
    queryKey: KEY,
    queryFn: async () => {
      const response = await api.get<Region[]>('/api/v1/regions')
      return response.data.data ?? []
    },
  })
}

export function useCreateRegion() {
  const queryClient = useQueryClient()
  return useMutation<Region, Error, CreateRegionRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Region>('/api/v1/regions', data)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

export function useUpdateRegion() {
  const queryClient = useQueryClient()
  return useMutation<Region, Error, { id: string } & Partial<CreateRegionRequest>>({
    mutationFn: async ({ id, ...data }) => {
      const response = await api.put<Region>(`/api/v1/regions/${id}`, data)
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

// Deleting is refused while nodes are still assigned, which is what stops a
// region disappearing out from under running machines.
export function useDeleteRegion() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/regions/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

export function useAssignNodeToRegion() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { regionId: string; nodeId: string }>({
    mutationFn: async ({ regionId, nodeId }) => {
      await api.post(`/api/v1/regions/${regionId}/nodes/${nodeId}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: KEY })
      queryClient.invalidateQueries({ queryKey: ['nodes'] })
    },
  })
}
