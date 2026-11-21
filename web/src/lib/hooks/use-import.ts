import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  ImportApplyResult,
  ImportItemRef,
  ImportOptions,
  ImportPlan,
  ImportSource,
  ImportSourceInput,
  SourceTestResult,
} from '@/types/import'

const base = '/api/v1/import/sources'

export function useImportSources() {
  return useQuery<ImportSource[]>({
    queryKey: ['import-sources'],
    queryFn: async () => (await api.get<ImportSource[]>(base)).data.data,
  })
}

export function useSaveImportSource() {
  const qc = useQueryClient()
  return useMutation<ImportSource, Error, ImportSourceInput & { id?: string }>({
    mutationFn: async ({ id, ...body }) =>
      (id ? await api.put<ImportSource>(`${base}/${id}`, body) : await api.post<ImportSource>(base, body)).data.data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['import-sources'] }),
  })
}

export function useDeleteImportSource() {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`${base}/${id}`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['import-sources'] }),
  })
}

export function useTestImportSource() {
  return useMutation<SourceTestResult, Error, string>({
    mutationFn: async (id) => (await api.post<SourceTestResult>(`${base}/${id}/test`)).data.data,
  })
}

/** The preview of a source. It polls while the source is being read. */
export function useImportPreview(id: string) {
  return useQuery<ImportPlan>({
    queryKey: ['import-preview', id],
    queryFn: async () => (await api.get<ImportPlan>(`${base}/${id}/preview`)).data.data,
    enabled: !!id,
    refetchInterval: (q) => (q.state.data?.reading ? 2000 : false),
  })
}

/** Keeps new mapping choices, or starts a new read of the source. */
export function useUpdateImportPreview(id: string) {
  const qc = useQueryClient()
  return useMutation<ImportPlan, Error, { refresh?: boolean; options?: ImportOptions }>({
    mutationFn: async (body) => (await api.post<ImportPlan>(`${base}/${id}/preview`, body)).data.data,
    onSuccess: (plan) => {
      qc.setQueryData(['import-preview', id], plan)
      qc.invalidateQueries({ queryKey: ['import-sources'] })
    },
  })
}

/** Imports one batch of preview items. Long batches get more time than a page load. */
export async function applyImportItems(id: string, items: ImportItemRef[]): Promise<ImportApplyResult[]> {
  const res = await api.post<{ results: ImportApplyResult[] }>(`${base}/${id}/apply`, { items }, { timeout: 120000 })
  return res.data.data.results
}

export function useSendImportInvites(id: string) {
  const qc = useQueryClient()
  return useMutation<number, Error, void>({
    mutationFn: async () => (await api.post<{ sent: number }>(`${base}/${id}/invites`)).data.data.sent,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['import-preview', id] }),
  })
}
