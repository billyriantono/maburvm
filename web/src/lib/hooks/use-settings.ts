import {
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { User, Enable2FAResponse } from '@/types'

interface UpdateProfileRequest {
  name?: string
  email?: string
}

interface ChangePasswordRequest {
  current_password: string
  new_password: string
}

export function useProfile() {
  return useQuery<User>({
    queryKey: ['settings', 'profile'],
    queryFn: async () => {
      const response = await api.get<User>('/api/v1/settings/profile')
      return response.data.data
    },
  })
}

export function useUpdateProfile() {
  const queryClient = useQueryClient()

  return useMutation<
    User,
    Error,
    UpdateProfileRequest
  >({
    mutationFn: async (data) => {
      const response = await api.put<User>('/api/v1/settings/profile', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'profile'] })
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}

export function useChangePassword() {
  return useMutation<void, Error, ChangePasswordRequest>({
    mutationFn: async (data) => {
      await api.post('/api/v1/settings/change-password', data)
    },
  })
}

export function useSetup2FA() {
  return useMutation<Enable2FAResponse, Error, void>({
    mutationFn: async () => {
      const response = await api.post<Enable2FAResponse>('/api/v1/settings/2fa/setup')
      return response.data.data
    },
  })
}

export function useVerify2FA() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (code) => {
      await api.post('/api/v1/settings/2fa/verify', { code })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'profile'] })
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}

export function useDisable2FA() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (code) => {
      await api.post('/api/v1/settings/2fa/disable', { code })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'profile'] })
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}
