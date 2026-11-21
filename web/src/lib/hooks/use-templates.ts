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
import type { TemplateRefreshResult } from '@/types/template'
import { arrivedOffset, uploadChunks } from '@/lib/chunked-upload'

interface TemplateListParams {
  type?: string
}

export function useTemplates(params: TemplateListParams & { includeNodes?: boolean } = {}, enabled = true) {
  const { type, includeNodes } = params

  return useQuery<OSTemplate[]>({
    queryKey: ['templates', 'list', { type, includeNodes }],
    queryFn: async () => {
      const response = await api.get<OSTemplate[]>('/api/v1/templates', {
        params: { ...(type ? { type } : {}), ...(includeNodes ? { include_nodes: 'true' } : {}) },
      })
      return response.data.data ?? []
    },
    enabled,
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

// useSyncTemplate asks every active node to fetch the template image now,
// instead of on the first VM that needs it.
export function useSyncTemplate() {
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.post(`/api/v1/templates/${id}/sync`, {})
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

// useRefreshTemplate reads the template's checksum list again and, when the
// image changed, has every node fetch it. force fetches it anyway.
export function useRefreshTemplate() {
  const queryClient = useQueryClient()
  return useMutation<TemplateRefreshResult, Error, { id: string; force?: boolean }>({
    mutationFn: async ({ id, force }) => {
      const response = await api.post<TemplateRefreshResult>(`/api/v1/templates/${id}/refresh`, { force: !!force })
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates'] })
    },
  })
}

// useUploadISO sends a file to the panel in chunks, then adds it as an ISO,
// which the panel copies to every node.
export function useUploadISO() {
  const queryClient = useQueryClient()
  return useMutation<OSTemplate, Error, { file: File; sha256?: string; osType?: string; onProgress?: (sent: number) => void }>({
    mutationFn: async ({ file, sha256, osType, onProgress }) => {
      const start = await api.post<{ upload_id: string; chunk_bytes: number }>('/api/v1/isos/uploads', {
        name: file.name, size: file.size, sha256: sha256?.trim() || undefined, os_type: osType,
      })
      const { upload_id, chunk_bytes } = start.data.data
      try {
        await uploadChunks(file.size, chunk_bytes, async (offset, end) => {
          try {
            await api.put(`/api/v1/isos/uploads/${upload_id}?offset=${offset}`, file.slice(offset, end), {
              headers: { 'Content-Type': 'application/octet-stream' },
            })
          } catch (err) {
            // An answer lost after the chunk arrived: the panel is past it.
            const at = arrivedOffset(err)
            if (at === undefined || at < end) throw err
          }
          onProgress?.(end)
        })
      } catch (err) {
        await api.delete(`/api/v1/isos/uploads/${upload_id}`).catch(() => undefined)
        throw err
      }
      const done = await api.post<OSTemplate>(`/api/v1/isos/uploads/${upload_id}/finish`)
      return done.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['templates'] })
    },
  })
}

// useDriversISO is the ISO put in a Windows install's second drive.
export function useDriversISO(enabled = true) {
  return useQuery<{ drivers_iso_template_id: string }>({
    queryKey: ['isos', 'drivers'],
    queryFn: async () => {
      const response = await api.get<{ drivers_iso_template_id: string }>('/api/v1/isos/drivers')
      return response.data.data
    },
    enabled,
  })
}

export function useSetDriversISO() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (templateId) => {
      await api.put('/api/v1/isos/drivers', { drivers_iso_template_id: templateId })
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['isos', 'drivers'] }),
  })
}
