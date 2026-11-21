import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

// One event the panel sent, or is still trying to send, to the billing
// system's webhook.
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
}

export interface BillingDeliveryPage {
  deliveries: BillingDelivery[]
  total: number
  page: number
  page_size: number
}

export function useBillingDeliveries(failedOnly: boolean, page: number) {
  return useQuery<BillingDeliveryPage>({
    queryKey: ['billing-deliveries', failedOnly, page],
    queryFn: async () =>
      (await api.get<BillingDeliveryPage>(`/api/v1/billing/deliveries?page=${page}&page_size=10${failedOnly ? '&status=failed' : ''}`)).data.data,
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
}

export function useRetryBillingDelivery() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.post(`/api/v1/billing/deliveries/${id}/retry`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['billing-deliveries'] }),
  })
}
