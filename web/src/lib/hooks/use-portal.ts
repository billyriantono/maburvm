import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { CustomerPortal, IPRequest } from '@/types/team'

/** What the customer area offers: links, published ISOs, address requests, graph history. */
export function usePortal() {
  return useQuery<CustomerPortal>({
    queryKey: ['portal'],
    queryFn: async () => (await api.get<CustomerPortal>('/api/v1/portal')).data.data,
    staleTime: 60_000,
  })
}

export function useVMIPRequests(vmId: string, enabled = true) {
  return useQuery<IPRequest[]>({
    queryKey: ['ip-requests', 'vm', vmId],
    queryFn: async () => (await api.get<IPRequest[]>(`/api/v1/vms/${vmId}/ip-requests`)).data.data,
    enabled: !!vmId && enabled,
  })
}

export function useRequestIP(vmId: string) {
  const qc = useQueryClient()
  return useMutation<{ data: IPRequest; warning?: string }, Error, { family: 'ipv4' | 'ipv6'; reason: string }>({
    mutationFn: async (v) => (await api.post<IPRequest>(`/api/v1/vms/${vmId}/ip-requests`, v)).data as unknown as { data: IPRequest; warning?: string },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ip-requests'] })
      qc.invalidateQueries({ queryKey: ['vms', 'detail', vmId] })
    },
  })
}

export function useCancelIPRequest(vmId: string) {
  const qc = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/vms/${vmId}/ip-requests/${id}`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ip-requests'] }),
  })
}

export interface IPRequestQueue {
  data: IPRequest[]
  total: number
  limit: number
  offset: number
}

/** The staff queue of address requests. */
export function useIPRequestQueue(status: string, page: number, pageSize = 25) {
  return useQuery<IPRequestQueue>({
    queryKey: ['ip-requests', 'queue', status, page],
    queryFn: async () => (await api.get<IPRequest[]>('/api/v1/ip-requests', {
      params: { status: status || undefined, limit: pageSize, offset: (page - 1) * pageSize },
    })).data as unknown as IPRequestQueue,
  })
}

export function useDecideIPRequest() {
  const qc = useQueryClient()
  return useMutation<{ data: IPRequest; warning?: string }, Error, { id: string; approve: boolean; note?: string }>({
    mutationFn: async ({ id, approve, note }) =>
      (await api.post<IPRequest>(`/api/v1/ip-requests/${id}/${approve ? 'approve' : 'reject'}`, approve ? {} : { note })).data as unknown as { data: IPRequest; warning?: string },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ip-requests'] }),
  })
}
