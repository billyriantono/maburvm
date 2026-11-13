import { useMutation } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Branding } from '@/lib/branding/core'

// Saving is admin-only; reading is public and comes with the page itself.
export function useSaveBranding() {
  return useMutation<Branding, Error, Branding>({
    mutationFn: async (data) => (await api.put<Branding>('/api/v1/settings/system/branding', data)).data.data,
  })
}
