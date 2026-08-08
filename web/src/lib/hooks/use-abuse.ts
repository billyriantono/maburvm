import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { GuestConnection } from '@/types'

const KEY = ['abuse-guests']

export interface AbuseList {
  guests: GuestConnection[]
  threshold: number
}

// Defaults to flagged guests only: on a healthy fleet every guest appears here
// at a rate near zero, and that noise buries the one row worth acting on.
export function useAbuseGuests(showAll = false) {
  return useQuery<AbuseList>({
    queryKey: [...KEY, showAll],
    queryFn: async () => {
      const response = await api.get<GuestConnection[]>(
        `/api/v1/abuse/guests${showAll ? '?all=true' : ''}`
      )
      const body = response.data as unknown as { data?: GuestConnection[]; threshold?: number }
      return { guests: body.data ?? [], threshold: body.threshold ?? 0 }
    },
    // Abuse is a live condition, not a static record: an operator watching this
    // page needs to see a quarantine take effect without reloading.
    refetchInterval: 15_000,
  })
}

export function useSetQuarantine() {
  const queryClient = useQueryClient()
  return useMutation<
    void,
    Error,
    { node_id: string; mac: string; quarantined: boolean; reason?: string }
  >({
    mutationFn: async (data) => {
      await api.post('/api/v1/abuse/guests/quarantine', data)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}
