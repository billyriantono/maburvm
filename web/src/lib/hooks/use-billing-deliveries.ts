import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

// One event the panel sent, or is still trying to send, to the billing
// system's webhook or to one of the operator's outbound webhooks.
export interface BillingDelivery {
  id: string
  event: string
  /** The JSON exactly as it was sent. */
  body: string
  status: 'pending' | 'retrying' | 'delivered' | 'failed'
  attempts: number
  response_code: number
  last_error: string
  resource_id: string
  created_at: string
  last_attempt_at: string | null
  delivered_at: string | null
  endpoint_id?: string
}

export interface BillingDeliveryPage {
  deliveries: BillingDelivery[]
  total: number
  page: number
  page_size: number
}

/** The billing webhook's log. */
export const BILLING_DELIVERIES = '/api/v1/billing/deliveries'

/** The delivery log at path (the billing webhook's, or an outbound webhook's). */
export function useDeliveries(path: string, failedOnly: boolean, page: number) {
  return useQuery<BillingDeliveryPage>({
    queryKey: ['deliveries', path, failedOnly, page],
    queryFn: async () =>
      (await api.get<BillingDeliveryPage>(`${path}?page=${page}&page_size=10${failedOnly ? '&status=failed' : ''}`)).data.data,
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
}

export function useRetryDelivery(path: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.post(`${path}/${id}/retry`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['deliveries', path] }),
  })
}
