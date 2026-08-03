import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Image, CreateImageRequest } from '@/types'

export type { Image, CreateImageRequest }

export function useImages() {
  return useQuery<Image[]>({
    queryKey: ['images', 'list'],
    queryFn: async () => {
      const response = await api.get<Image[]>('/api/v1/images')
      return response.data.data
    },
    // Image capture is async (starts "pending"). Poll while any capture is still
    // running so status flips to available/failed without a manual reload.
    refetchInterval: (query) => {
      const images = query.state.data as Image[] | undefined
      return images?.some((img) => img.status === 'pending') ? 5000 : false
    },
  })
}

export function useCreateImage() {
  const queryClient = useQueryClient()
  return useMutation<Image, Error, CreateImageRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Image>('/api/v1/images', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['images', 'list'] })
    },
  })
}

export function useDeleteImage() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/images/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['images', 'list'] })
    },
  })
}
