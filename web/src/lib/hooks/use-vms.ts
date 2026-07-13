import { useEffect } from 'react'
import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  VM,
  VMMetrics,
  VMMetricSample,
  VMOperation,
  CreateVMRequest,
  UpdateVMRequest,
  PaginatedResponse,
} from '@/types'

interface VMListParams {
  page?: number
  pageSize?: number
  userId?: string
  nodeId?: string
  status?: string;
}

// useVMOperation polls the VM's latest multi-step operation (e.g. a delete),
// auto-refreshing while it is still running so the UI shows live progress. Stops
// polling once the operation completes or fails.
export function useVMOperation(vmId: string | null, enabled: boolean) {
  return useQuery<VMOperation | null>({
    queryKey: ['vm-operation', vmId],
    queryFn: async () => {
      const response = await api.get<VMOperation | null>(`/api/v1/vms/${vmId}/operation`)
      return response.data.data
    },
    enabled: !!vmId && enabled,
    refetchInterval: (query) => {
      const op = query.state.data as VMOperation | null | undefined
      // Keep polling while running, or while no op row exists yet (worker may not
      // have created it in the split second after the delete was enqueued).
      return !op || op.status === 'running' ? 1500 : false
    },
  })
}

export function useVMs(params: VMListParams = {}) {
  const { page = 1, pageSize = 20, userId, nodeId, status } = params

  return useQuery<PaginatedResponse<VM>>({
    queryKey: ['vms', 'list', { page, pageSize, userId, nodeId, status }],
    queryFn: async () => {
      const response = await api.get<PaginatedResponse<VM>>('/api/v1/vms', {
        params: {
          page,
          page_size: pageSize,
          user_id: userId,
          node_id: nodeId,
          status,
        },
      })
      return response.data as unknown as PaginatedResponse<VM>
    },
  })
}

// useVMStatusStream subscribes to the live VM-status SSE feed and refreshes the
// VM list (and any open VM detail) the moment a status changes — pushing updates
// faster than the periodic poll, which stays as a fallback. Reconnection is
// handled by the browser's EventSource automatically.
export function useVMStatusStream() {
  const queryClient = useQueryClient()

  useEffect(() => {
    if (typeof window === 'undefined' || typeof EventSource === 'undefined') return

    const source = new EventSource('/api/vm-events')
    source.onmessage = (event) => {
      try {
        const message = JSON.parse(event.data)
        if (message?.type === 'vm_status' || message?.type === 'vm_list') {
          queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
          queryClient.invalidateQueries({ queryKey: ['vms', 'detail'] })
        }
      } catch {
        // ignore non-JSON frames (e.g. keepalive comments)
      }
    }
    // On error the browser auto-reconnects; nothing to do here.

    return () => source.close()
  }, [queryClient])
}

export function useVM(id: string, options?: UseQueryOptions<VM>) {
  return useQuery<VM>({
    queryKey: ['vms', 'detail', id],
    queryFn: async () => {
      const response = await api.get<VM>(`/api/v1/vms/${id}`)
      return response.data.data
    },
    enabled: !!id,
    ...options,
  })
}

// CreateVMResult is the create response: the new VM's identity plus a one-time
// root_password when the server generated one (no password/SSH key supplied).
export interface CreateVMResult {
  id: string
  hostname: string
  status: string
  node_id: string
  job_id: number
  vnc_port?: number
  created_at: string
  root_password?: string
}

export function useCreateVM() {
  const queryClient = useQueryClient()

  return useMutation<CreateVMResult, Error, CreateVMRequest>({
    mutationFn: async (data) => {
      const response = await api.post<CreateVMResult>('/api/v1/vms', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useUpdateVM(id: string) {
  const queryClient = useQueryClient()

  return useMutation<VM, Error, UpdateVMRequest>({
    mutationFn: async (data) => {
      const response = await api.put<VM>(`/api/v1/vms/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useDeleteVM() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vms/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

export function useVMAction(id: string) {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (action) => {
      // Backend exposes per-action lifecycle endpoints (/start, /stop, /restart,
      // /force-stop), not a single /actions endpoint.
      await api.post(`/api/v1/vms/${id}/${action}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// Version that accepts vmId + action together (for list pages)
export function useVMActions() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, { vmId: string; action: string }>({
    mutationFn: async ({ vmId, action }) => {
      await api.post(`/api/v1/vms/${vmId}/${action}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// useAttachISO attaches a bootable ISO (by URL) to a VM for install/rescue.
export function useAttachISO(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (isoUrl) => {
      await api.post(`/api/v1/vms/${id}/iso/attach`, { iso_url: isoUrl })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// useDetachISO removes the install/rescue ISO and restores HD boot order.
export function useDetachISO(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(`/api/v1/vms/${id}/iso/detach`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// useRescueVM boots the VM from a rescue ISO. Optional isoUrl overrides the
// server default (RESCUE_ISO_URL).
export function useRescueVM(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string | undefined>({
    mutationFn: async (isoUrl) => {
      await api.post(`/api/v1/vms/${id}/rescue`, isoUrl ? { iso_url: isoUrl } : {})
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// useUnrescueVM detaches the rescue ISO and clears rescue mode.
export function useUnrescueVM(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(`/api/v1/vms/${id}/unrescue`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// RebuildVMInput captures the rebuild options (template + optional root password
// and SSH keys to inject on the fresh disk via cloud-init).
export interface RebuildVMInput {
  template_id?: string
  preserve_ip?: boolean
  password?: string
  regenerate_password?: boolean
  ssh_key_ids?: string[]
}

// useRebuildVM reinstalls a VM from a template, optionally setting a new root
// password and injecting selected SSH keys. Returns root_password when one was
// generated server-side (shown once).
export function useRebuildVM(id: string) {
  const queryClient = useQueryClient()
  return useMutation<{ status: string; job_id: number; root_password?: string }, Error, RebuildVMInput>({
    mutationFn: async (data) => {
      const response = await api.post(`/api/v1/vms/${id}/rebuild`, data)
      return response.data.data as { status: string; job_id: number; root_password?: string }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// useCloneVM clones a stopped VM into a new one (fresh IP/hostname, disk copied
// from the source). dest_node_id targets another node (cross-node clone);
// omitted = same node. Returns the new VM.
export function useCloneVM(id: string) {
  const queryClient = useQueryClient()
  return useMutation<{ vm: VM; job_id: number; status: string }, Error, { hostname?: string; dest_node_id?: string }>({
    mutationFn: async (body) => {
      const response = await api.post(`/api/v1/vms/${id}/clone`, body)
      return response.data.data as { vm: VM; job_id: number; status: string }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// useResetPassword sets a new guest root password. Applied via the qemu guest
// agent on the running VM (cloud images ship qemu-guest-agent), so the VM must
// be running. Returns the enqueued job info.
export function useResetPassword(id: string) {
  const queryClient = useQueryClient()
  return useMutation<{ job_id: number; status: string }, Error, string>({
    mutationFn: async (password) => {
      const response = await api.post(`/api/v1/vms/${id}/reset-password`, { password })
      return response.data.data as { job_id: number; status: string }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// useRegenerateVNCPassword generates a new VNC console password. When the VM is
// running it's applied live via the agent (QEMU monitor); otherwise it takes
// effect on next start. Returns the new password so the UI can surface it once.
export function useRegenerateVNCPassword(id: string) {
  const queryClient = useQueryClient()
  return useMutation<{ vnc_port?: number; vnc_password: string }, Error, void>({
    mutationFn: async () => {
      const response = await api.post(`/api/v1/vms/${id}/vnc/refresh`)
      return response.data.data as { vnc_port?: number; vnc_password: string }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// useSetConsoleEnabled enables or disables VNC console access for a VM.
// Disabling drops any in-flight console session and blocks new tokens.
export function useSetConsoleEnabled(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, boolean>({
    mutationFn: async (enabled) => {
      await api.post(`/api/v1/vms/${id}/console/${enabled ? 'enable' : 'disable'}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
    },
  })
}

// useMigrateVM live-migrates a VM to another node.
export function useMigrateVM(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, { dest_node_id: string; live?: boolean; copy_storage?: boolean }>({
    mutationFn: async (data) => {
      await api.post(`/api/v1/vms/${id}/migrate`, data)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vms', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['vms', 'list'] })
    },
  })
}

// useVMMetricsHistory returns persisted samples for the trailing `minutes` window.
export function useVMMetricsHistory(id: string, minutes = 60) {
  return useQuery<VMMetricSample[]>({
    queryKey: ['vms', 'metrics-history', id, minutes],
    queryFn: async () => {
      const response = await api.get<VMMetricSample[]>(`/api/v1/vms/${id}/metrics/history`, {
        params: { minutes },
      })
      return response.data.data
    },
    enabled: !!id,
    refetchInterval: 30000,
  })
}

export function useVMMetrics(id: string) {
  return useQuery<VMMetrics>({
    queryKey: ['vms', 'metrics', id],
    queryFn: async () => {
      const response = await api.get<VMMetrics>(`/api/v1/vms/${id}/metrics`)
      return response.data.data
    },
    enabled: !!id,
    refetchInterval: 5000,
  })
}
