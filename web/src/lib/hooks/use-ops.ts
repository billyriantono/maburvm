import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, apiClient } from '@/lib/api-client'

// --- Security centre ---
export interface SecurityCheck {
  id: string
  title: string
  status: 'ok' | 'warn' | 'fail'
  detail: string
  fix?: string
}

export function useSecurityChecks() {
  return useQuery<SecurityCheck[]>({
    queryKey: ['security', 'checks'],
    queryFn: async () => (await api.get<SecurityCheck[]>('/api/v1/security/checks')).data.data,
  })
}

// --- Temporary support access ---
export interface SupportGrant {
  id: string
  email: string
  note: string
  expires_at: string
  created_at: string
}

export function useSupportGrants() {
  return useQuery<SupportGrant[]>({
    queryKey: ['security', 'support'],
    queryFn: async () => (await api.get<SupportGrant[]>('/api/v1/security/support-access')).data.data,
    refetchInterval: 60_000,
  })
}

export function useGrantSupport() {
  const queryClient = useQueryClient()
  return useMutation<{ grant: SupportGrant; password: string }, Error, { hours: number; note: string }>({
    mutationFn: async (body) =>
      (await api.post<{ grant: SupportGrant; password: string }>('/api/v1/security/support-access', body)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

export function useEndSupport() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/security/support-access/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

// --- Setup checklist ---
export interface SetupStep {
  id: string
  title: string
  why: string
  href: string
  done: boolean
}

export function useSetup() {
  return useQuery<{ steps: SetupStep[]; dismissed: boolean }>({
    queryKey: ['setup'],
    queryFn: async () => (await api.get<{ steps: SetupStep[]; dismissed: boolean }>('/api/v1/setup')).data.data,
  })
}

export function useDismissSetup() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, boolean>({
    mutationFn: async (dismissed) => {
      await api.put('/api/v1/setup/dismissed', { dismissed })
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['setup'] }),
  })
}

// --- Data export ---
// Fetched with the session and handed to the browser as a file: a plain link
// would not carry the sign-in.
export async function downloadExport(kind: string) {
  const res = await apiClient.get(`/api/v1/export/${kind}`, { responseType: 'blob' })
  const name = /filename="([^"]+)"/.exec(String(res.headers['content-disposition'] ?? ''))?.[1] ?? `${kind}.csv`
  const url = URL.createObjectURL(res.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
  return name
}

// --- Job queue ---
export interface QueueJob {
  id: number
  kind: string
  queue: string
  state: string
  attempt: number
  max_attempts: number
  created_at: string
  finalized_at?: string
  subject: string
  last_error: string
}

export interface QueueData {
  counts: { state: string; count: number }[]
  jobs: QueueJob[]
  workers: Record<string, number>
  saved_workers: Record<string, number>
}

export function useQueue(state: string) {
  return useQuery<QueueData>({
    queryKey: ['queue', state],
    queryFn: async () => (await api.get<QueueData>('/api/v1/queue', { params: state ? { state } : {} })).data.data,
    refetchInterval: 5000,
  })
}

export function useQueueJobAction() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { id: number; action: 'retry' | 'cancel' }>({
    mutationFn: async ({ id, action }) => {
      await api.post(`/api/v1/queue/jobs/${id}/${action}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['queue'] }),
  })
}

export function useSaveQueueWorkers() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, Record<string, number>>({
    mutationFn: async (workers) => {
      await api.put('/api/v1/queue/workers', workers)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['queue'] }),
  })
}
