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
  TemplateType,
} from '@/types'

interface TemplateListParams {
  type?: TemplateType
}

export function useTemplates(params: TemplateListParams = {}) {
  const { type } = params

  return useQuery<OSTemplate[]>({
    queryKey: ['templates', 'list', { type }],
    queryFn: () =>
      api.get<OSTemplate[]>('/api/v1/templates', { type }),
  })
}

export function useTemplate(
  id: string,
  options?: UseQueryOptions<OSTemplate>
) {
  return useQuery<OSTemplate>({
    queryKey: ['templates', 'detail', id],
    queryFn: () => api.get<OSTemplate>(`/api/v1/templates/${id}`),
    enabled: !!id,
    ...options,
  })
}

export function useCreateTemplate() {
  const queryClient = useQueryClient()

  return useMutation<OSTemplate, Error, CreateTemplateRequest>({
    mutationFn: (data) => api.post<OSTemplate>('/api/v1/templates', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}

export function useUpdateTemplate(id: string) {
  const queryClient = useQueryClient()

  return useMutation<OSTemplate, Error, UpdateTemplateRequest>({
    mutationFn: (data) =>
      api.put<OSTemplate>(`/api/v1/templates/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}

export function useDeleteTemplate() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (id) => api.delete(`/api/v1/templates/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}

export function useSyncTemplate(id: string) {
  const queryClient = useQueryClient()

  return useMutation<OSTemplate, Error, void>({
    mutationFn: () =>
      api.post<OSTemplate>(`/api/v1/templates/${id}/sync`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['templates', 'list'] })
    },
  })
}
