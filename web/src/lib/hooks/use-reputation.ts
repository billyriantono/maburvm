import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

export interface IPReputation {
  id: number
  address: string
  pool_id: string
  pool_name?: string
  vm_hostname?: string
  assigned: boolean
  /** Blocklists that answered "listed". */
  listings: string[] | null
  /** -1 means never checked — not a clean zero. */
  abuse_score: number
  total_reports: number
  last_reported_at: string | null
  /** Why a check could not be completed. A refused query is not a clean result. */
  check_error: string
  checked_at: string
}

// Defaults to flagged addresses only: a fleet of clean ones buries the handful
// that are not.
export function useIPReputation(showAll = false) {
  return useQuery<IPReputation[]>({
    queryKey: ['ip-reputation', showAll],
    queryFn: async () => {
      const response = await api.get<IPReputation[]>(
        `/api/v1/ip-reputation${showAll ? '?all=true' : ''}`
      )
      return response.data.data ?? []
    },
  })
}

export function useCheckReputationNow() {
  const queryClient = useQueryClient()
  return useMutation<{ checked: number }, Error, void>({
    mutationFn: async () => {
      const response = await api.post<{ checked: number }>('/api/v1/ip-reputation/check', {})
      return response.data.data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ip-reputation'] }),
  })
}
