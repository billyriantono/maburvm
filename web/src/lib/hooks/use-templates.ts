import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  OSTemplate,
  CreateTemplateRequest,
  UpdateTemplateRequest,
} from '@/types'

export function useTemplates() {
  return useQuery<OSTemplate[]>({
    queryKey: ['templates', 'list'],
    queryFn: async () => {
      const response = await api.get<OSTemplate[]>('/api/v1/templates')
      return response.data.data
    },
  })
}

export function useTemplate(
  id: string,
  options?: UseQueryOptions<OSTemplate>
) {
  return useQuery<OSTemplate>({
    queryKey: ['templates', 'detail', id],
    queryFn: async () => {
      const response = await api.get<OSTemplate>(`/api/v1/templates/${id}`)
      return response.data.data
    },
    enabled: !!id,
    ...options,
  })
}

export function useCreateTemplate() {
  const queryClient = useQueryClient()

  return useMutation<OSTemplate, Error, CreateTemplateRequest>({
    mutationFn: async (data) => {
      const response = await api.post<OSTemplate>('/api/v1/templates', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}

export function useUpdateTemplate(id: string) {
  const queryClient = useQueryClient()

  return useMutation<OSTemplate, Error, UpdateTemplateRequest>({
    mutationFn: async (data) => {
      const response = await api.put<OSTemplate>(`/api/v1/templates/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}

export function useDeleteTemplate() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/templates/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}
