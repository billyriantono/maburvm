import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, apiClient } from '@/lib/api-client'

// --- Maintenance mode ---
export interface RunningJob {
  id: number
  kind: string
  subject: string
  attempted_at: string
}

export interface Maintenance {
  enabled: boolean
  message: string
  since?: string
  by?: string
  hold_jobs: boolean
  running_jobs?: RunningJob[]
  can_switch: boolean
}

export function useMaintenance(enabled = true) {
  return useQuery<Maintenance>({
    queryKey: ['maintenance'],
    queryFn: async () => (await api.get<Maintenance>('/api/v1/maintenance')).data.data,
    refetchInterval: 30_000,
    enabled,
  })
}

export function useSetMaintenance() {
  const qc = useQueryClient()
  return useMutation<Maintenance, Error, { enabled: boolean; message: string; hold_jobs: boolean }>({
    mutationFn: async (body) => (await api.put<Maintenance>('/api/v1/maintenance', body)).data.data,
    onSuccess: (m) => qc.setQueryData(['maintenance'], m),
  })
}

// --- Update notice ---
export interface UpdateStatus {
  enabled: boolean
  repository: string
  checked_at: string | null
  latest: { tag: string; name: string; url: string; published_at: string; notes: string } | null
  security: boolean
  error?: string
  dismissed_tag?: string
  current: string
  update_available: boolean | null
  notice: boolean
}

interface UpdateView {
  status: UpdateStatus
  can_manage: boolean
}

export function useUpdateStatus(enabled = true) {
  return useQuery<UpdateView>({
    queryKey: ['system-update'],
    queryFn: async () => (await api.get<UpdateView>('/api/v1/system/update')).data.data,
    refetchInterval: 6 * 60 * 60_000,
    enabled,
  })
}

function useUpdateMutation<T>(fn: (body: T) => Promise<UpdateView>) {
  const qc = useQueryClient()
  return useMutation<UpdateView, Error, T>({
    mutationFn: fn,
    onSuccess: (v) => qc.setQueryData(['system-update'], v),
  })
}

export const useSaveUpdateSettings = () =>
  useUpdateMutation<{ enabled: boolean; repository: string }>(async (body) => (await api.put<UpdateView>('/api/v1/system/update', body)).data.data)
export const useCheckUpdate = () =>
  useUpdateMutation<void>(async () => (await api.post<UpdateView>('/api/v1/system/update/check')).data.data)
export const useDismissUpdate = () =>
  useUpdateMutation<string>(async (tag) => (await api.post<UpdateView>('/api/v1/system/update/dismiss', { tag })).data.data)

// --- Logs ---
export interface RequestLogEntry {
  time: string
  method: string
  path: string
  route: string
  status: number
  latency_ms: number
  user_id?: string
  email?: string
  api_key_id?: string
  ip: string
  request_id?: string
}

export interface RequestFilter {
  method: string
  status: string
  q: string
  min_ms: string
  page: number
}

export function useRequestLog(f: RequestFilter) {
  return useQuery<{ requests: RequestLogEntry[]; total: number; page: number; page_size: number; kept: number }>({
    queryKey: ['logs', 'requests', f],
    queryFn: async () => (await api.get('/api/v1/logs/requests', { params: { ...f, page_size: 50 } })).data.data as never,
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  })
}

export interface IPAllocationEvent {
  id: string
  address: string
  vm_id: string | null
  vm_hostname: string
  user_email: string
  node_name: string
  kind: string
  assigned_at: string
  released_at: string | null
  estimated: boolean
}

export function useIPAllocations(f: { q: string; state: string; page: number }) {
  return useQuery<{ events: IPAllocationEvent[]; total: number; page: number; page_size: number }>({
    queryKey: ['logs', 'ip', f],
    queryFn: async () => (await api.get('/api/v1/logs/ip-allocations', { params: { ...f, page_size: 50 } })).data.data as never,
    placeholderData: keepPreviousData,
  })
}

// --- Reports ---
export interface CapacityPoint {
  day: string
  vms: number
  vcpu_allocated: number
  vcpu_total: number
  ram_allocated_mb: number
  ram_total_mb: number
  disk_allocated_gb: number
  disk_total_gb: number
  disk_used_gb: number
  cpu_usage: number
  memory_usage: number
}

export interface CapacitySeries {
  id: string
  name: string
  points: CapacityPoint[]
}

export function useCapacityReport(scope: string, days: number) {
  return useQuery<{ scope: string; days: number; series: CapacitySeries[] }>({
    queryKey: ['reports', 'capacity', scope, days],
    queryFn: async () => (await api.get('/api/v1/reports/capacity', { params: { scope, days } })).data.data as never,
  })
}

export interface CustomerUsage {
  user_id: string
  email: string
  name: string
  vms: number
  vcpu: number
  ram_mb: number
  disk_gb: number
  transfer_bytes: number
}

export function useUsageReport(month: string) {
  return useQuery<{ month: string; customers: CustomerUsage[] }>({
    queryKey: ['reports', 'usage', month],
    queryFn: async () => (await api.get('/api/v1/reports/usage', { params: { month } })).data.data as never,
  })
}

/** Fetches a CSV with the session and hands it to the browser as a file. */
export async function downloadCSV(path: string, params: Record<string, string | number>) {
  const res = await apiClient.get(path, { params: { ...params, format: 'csv' }, responseType: 'blob' })
  const name = /filename="([^"]+)"/.exec(String(res.headers['content-disposition'] ?? ''))?.[1] ?? 'report.csv'
  const url = URL.createObjectURL(res.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// --- Announcements ---
export type AudienceType = 'all' | 'nodes' | 'regions' | 'plans' | 'vms'

export interface Announcement {
  id: string
  title: string
  body: string
  kind: 'service' | 'marketing'
  audience: { type: AudienceType; ids?: string[] }
  starts_at: string
  ends_at: string | null
  emailed_at: string | null
  email_queued: number
  created_at: string
}

export interface AnnouncementInput {
  title: string
  body: string
  kind: 'service' | 'marketing'
  audience: { type: AudienceType; ids?: string[] }
  starts_at?: string | null
  ends_at?: string | null
}

export function useAnnouncements() {
  return useQuery<Announcement[]>({
    queryKey: ['announcements'],
    queryFn: async () => (await api.get<Announcement[]>('/api/v1/announcements')).data.data,
  })
}

// What a customer is shown: never who else an announcement is for.
export type CustomerAnnouncement = Pick<Announcement, 'id' | 'title' | 'body' | 'kind' | 'starts_at' | 'ends_at'>

export function useCurrentAnnouncements(enabled = true) {
  return useQuery<CustomerAnnouncement[]>({
    queryKey: ['announcements', 'current'],
    queryFn: async () => (await api.get<CustomerAnnouncement[]>('/api/v1/announcements/current')).data.data,
    refetchInterval: 5 * 60_000,
    enabled,
  })
}

export function useSaveAnnouncement() {
  const qc = useQueryClient()
  return useMutation<Announcement, Error, { id?: string; input: AnnouncementInput }>({
    mutationFn: async ({ id, input }) =>
      (id ? await api.put<Announcement>(`/api/v1/announcements/${id}`, input) : await api.post<Announcement>('/api/v1/announcements', input)).data.data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['announcements'] }),
  })
}

export function useDeleteAnnouncement() {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/announcements/${id}`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['announcements'] }),
  })
}

export interface AnnouncementPreview {
  recipients: number
  sample: string[]
  email: { subject: string; text: string; html: string }
}

export function usePreviewAnnouncement() {
  return useMutation<AnnouncementPreview, Error, AnnouncementInput>({
    mutationFn: async (input) => (await api.post<AnnouncementPreview>('/api/v1/announcements/preview', input)).data.data,
  })
}

export function useEmailAnnouncement() {
  const qc = useQueryClient()
  return useMutation<{ queued: number; per_minute: number }, Error, { id: string; per_minute: number }>({
    mutationFn: async ({ id, per_minute }) =>
      (await api.post<{ queued: number; per_minute: number }>(`/api/v1/announcements/${id}/email`, { per_minute })).data.data,
    onSuccess: () => setTimeout(() => qc.invalidateQueries({ queryKey: ['announcements'] }), 3000),
  })
}

// --- Bulk VM actions ---
export interface BulkResult {
  vm_id: string
  hostname: string
  ok: boolean
  message: string
  changes?: string[]
}

export interface BulkRequest {
  action: string
  vm_ids: string[]
  preview: boolean
  plan_id?: string
  group_id?: string
  node_id?: string
}

export function useBulkVMs() {
  const qc = useQueryClient()
  return useMutation<{ results: BulkResult[]; ok: number; total: number }, Error, BulkRequest>({
    // Several VMs one after another can outlast the usual 30 seconds.
    mutationFn: async (body) => (await api.post('/api/v1/vms/bulk', body, { timeout: 180_000 })).data.data as never,
    onSuccess: (_, body) => {
      if (!body.preview) qc.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// --- Delete lock, tags and staff notes ---
export function useSetVMLock() {
  const qc = useQueryClient()
  return useMutation<{ delete_lock: string }, Error, { vmId: string; locked: boolean }>({
    mutationFn: async ({ vmId, locked }) => (await api.put<{ delete_lock: string }>(`/api/v1/vms/${vmId}/lock`, { locked })).data.data,
    onSuccess: (_, { vmId }) => qc.invalidateQueries({ queryKey: ['vms', 'detail', vmId] }),
  })
}

export function useSetVMTags() {
  const qc = useQueryClient()
  return useMutation<{ tags: string[] }, Error, { vmId: string; tags: string[] }>({
    mutationFn: async ({ vmId, tags }) => (await api.put<{ tags: string[] }>(`/api/v1/vms/${vmId}/tags`, { tags })).data.data,
    onSuccess: (_, { vmId }) => qc.invalidateQueries({ queryKey: ['vms', 'detail', vmId] }),
  })
}

export interface StaffNote {
  id: string
  body: string
  author_email: string
  created_at: string
  updated_at: string
}

const notesPath = (subject: 'vms' | 'users', id: string) => `/api/v1/${subject}/${id}/notes`

export function useStaffNotes(subject: 'vms' | 'users', id: string) {
  return useQuery<StaffNote[]>({
    queryKey: ['notes', subject, id],
    queryFn: async () => (await api.get<StaffNote[]>(notesPath(subject, id))).data.data,
    enabled: !!id,
  })
}

export function useStaffNoteActions(subject: 'vms' | 'users', id: string) {
  const qc = useQueryClient()
  const done = () => qc.invalidateQueries({ queryKey: ['notes', subject, id] })
  return {
    add: useMutation<StaffNote, Error, string>({
      mutationFn: async (body) => (await api.post<StaffNote>(notesPath(subject, id), { body })).data.data,
      onSuccess: done,
    }),
    update: useMutation<StaffNote, Error, { noteId: string; body: string }>({
      mutationFn: async ({ noteId, body }) => (await api.put<StaffNote>(`${notesPath(subject, id)}/${noteId}`, { body })).data.data,
      onSuccess: done,
    }),
    remove: useMutation<void, Error, string>({
      mutationFn: async (noteId) => {
        await api.delete(`${notesPath(subject, id)}/${noteId}`)
      },
      onSuccess: done,
    }),
  }
}

// --- Hypervisor groups ---
export interface NodeGroup {
  name: string
  nodes: string[]
  regions: string[]
  plans: number
}

export function useNodeGroups(enabled = true) {
  return useQuery<NodeGroup[]>({
    queryKey: ['node-groups'],
    queryFn: async () => (await api.get<NodeGroup[]>('/api/v1/node-groups')).data.data,
    enabled,
  })
}

// --- Email preferences (customers) ---
export function useSetMarketing() {
  const qc = useQueryClient()
  return useMutation<{ marketing: boolean }, Error, boolean>({
    mutationFn: async (marketing) => (await api.put<{ marketing: boolean }>('/api/v1/settings/email-preferences', { marketing })).data.data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'profile'] }),
  })
}
