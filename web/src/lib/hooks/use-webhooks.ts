import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { BillingDelivery } from '@/lib/hooks/use-billing-deliveries'

// An outbound webhook the operator added (System settings, API).
export interface WebhookEndpoint {
  id: string
  name: string
  url: string
  /** Event names, or "*" for every event including ones added later. */
  events: string[]
  /** A signing secret is saved; the secret itself is never shown. */
  has_secret: boolean
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface WebhookEventGroup {
  group: string
  events: { event: string; label: string }[]
}

/** Creates or changes a webhook. secret: undefined keeps the saved one, "" removes it. */
export interface WebhookEndpointInput {
  name: string
  url: string
  secret?: string
  events: string[]
  enabled: boolean
}

export const ALL_WEBHOOK_EVENTS = '*'

const KEY = ['webhooks']

export function useWebhooks(enabled = true) {
  return useQuery<{ endpoints: WebhookEndpoint[]; catalog: WebhookEventGroup[] }>({
    queryKey: KEY,
    enabled,
    queryFn: async () => {
      const body = (await api.get<WebhookEndpoint[]>('/api/v1/webhooks')).data as { data: WebhookEndpoint[]; events?: WebhookEventGroup[] }
      return { endpoints: body.data ?? [], catalog: body.events ?? [] }
    },
  })
}

export function useSaveWebhook() {
  const queryClient = useQueryClient()
  return useMutation<WebhookEndpoint, Error, { id?: string; input: WebhookEndpointInput }>({
    mutationFn: async ({ id, input }) =>
      (id ? await api.put<WebhookEndpoint>(`/api/v1/webhooks/${id}`, input) : await api.post<WebhookEndpoint>('/api/v1/webhooks', input)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

export function useDeleteWebhook() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/webhooks/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

/** Sends a test event now and answers how it went. */
export function useTestWebhook() {
  const queryClient = useQueryClient()
  return useMutation<BillingDelivery, Error, string>({
    mutationFn: async (id) => (await api.post<BillingDelivery>(`/api/v1/webhooks/${id}/test`)).data.data,
    onSuccess: (_d, id) => queryClient.invalidateQueries({ queryKey: ['deliveries', `/api/v1/webhooks/${id}/deliveries`] }),
  })
}
