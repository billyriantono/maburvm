import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { AbuseEvent, AbusePolicy, GuestConnection } from '@/types'

const KEY = ['abuse-guests']
const EVENTS = ['abuse-events']
const POLICY = ['abuse-policy']

export interface AbuseList {
  guests: GuestConnection[]
  threshold: number
  policy?: AbusePolicy
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
      const body = response.data as unknown as {
        data?: GuestConnection[]
        threshold?: number
        policy?: AbusePolicy
      }
      return { guests: body.data ?? [], threshold: body.threshold ?? 0, policy: body.policy }
    },
    // Abuse is a live condition, not a static record: an operator watching this
    // page needs to see a quarantine take effect without reloading.
    refetchInterval: 15_000,
  })
}

/** The abuse timeline, newest first. */
export function useAbuseEvents(openOnly: boolean) {
  return useQuery<AbuseEvent[]>({
    queryKey: [...EVENTS, openOnly],
    queryFn: async () => {
      const response = await api.get<AbuseEvent[]>(
        `/api/v1/abuse/events?limit=100${openOnly ? '&open=true' : ''}`
      )
      const body = response.data as unknown as { data?: AbuseEvent[] }
      return body.data ?? []
    },
    refetchInterval: 15_000,
  })
}

function useInvalidateAbuse() {
  const queryClient = useQueryClient()
  return () => {
    queryClient.invalidateQueries({ queryKey: KEY })
    queryClient.invalidateQueries({ queryKey: EVENTS })
  }
}

export function useSetQuarantine() {
  const invalidate = useInvalidateAbuse()
  return useMutation<
    void,
    Error,
    { node_id: string; mac: string; quarantined: boolean; reason?: string }
  >({
    mutationFn: async (data) => {
      await api.post('/api/v1/abuse/guests/quarantine', data)
    },
    onSuccess: invalidate,
  })
}

export function useReleaseAbuseEvent() {
  const invalidate = useInvalidateAbuse()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.post(`/api/v1/abuse/events/${id}/release`, {})
    },
    onSuccess: invalidate,
  })
}

export function useNullRoute() {
  const invalidate = useInvalidateAbuse()
  return useMutation<
    void,
    Error,
    { node_id: string; ip: string; minutes: number; reason?: string }
  >({
    mutationFn: async (data) => {
      await api.post('/api/v1/abuse/null-routes', data)
    },
    onSuccess: invalidate,
  })
}

export function useAbusePolicy() {
  return useQuery<AbusePolicy>({
    queryKey: POLICY,
    queryFn: async () => {
      const response = await api.get<AbusePolicy>('/api/v1/abuse/policy')
      return response.data.data as AbusePolicy
    },
  })
}

export function useSaveAbusePolicy() {
  const queryClient = useQueryClient()
  return useMutation<AbusePolicy, Error, AbusePolicy>({
    mutationFn: async (policy) => {
      const response = await api.put<AbusePolicy>('/api/v1/abuse/policy', policy)
      return response.data.data as AbusePolicy
    },
    onSuccess: (policy) => {
      queryClient.setQueryData(POLICY, policy)
      queryClient.invalidateQueries({ queryKey: KEY })
    },
  })
}
