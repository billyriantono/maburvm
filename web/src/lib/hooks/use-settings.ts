import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { User, ChangePasswordRequest, TwoFASetup } from '@/types'

export function useProfile() {
  return useQuery<User>({
    queryKey: ['settings', 'profile'],
    queryFn: () => api.get<User>('/api/v1/settings/profile'),
  })
}

export function useUpdateProfile() {
  const queryClient = useQueryClient()

  return useMutation<
    User,
    Error,
    { name?: string; email?: string }
  >({
    mutationFn: (data) =>
      api.put<User>('/api/v1/settings/profile', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'profile'] })
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}

export function useChangePassword() {
  return useMutation<void, Error, ChangePasswordRequest>({
    mutationFn: (data) => api.post('/api/v1/settings/change-password', data),
  })
}

export function useSetup2FA() {
  return useMutation<TwoFASetup, Error, void>({
    mutationFn: () => api.post<TwoFASetup>('/api/v1/settings/2fa/setup'),
  })
}

export function useVerify2FA() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (code) =>
      api.post('/api/v1/settings/2fa/verify', { code }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'profile'] })
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}

export function useDisable2FA() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (code) =>
      api.post('/api/v1/settings/2fa/disable', { code }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'profile'] })
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}
