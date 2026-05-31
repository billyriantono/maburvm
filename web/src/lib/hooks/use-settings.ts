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

// System settings (admin) — one object per section: general/security/backup/api/email.
export function useSystemSettings() {
  return useQuery<Record<string, Record<string, unknown>>>({
    queryKey: ['settings', 'system'],
    queryFn: async () => {
      const response = await api.get<Record<string, Record<string, unknown>>>('/api/v1/settings/system')
      return response.data.data || {}
    },
  })
}

export function useSaveSystemSettings() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, Record<string, unknown>>({
    mutationFn: async (data) => {
      await api.put('/api/v1/settings/system', data)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings', 'system'] })
    },
  })
}

// useTestEmail sends a test email using the supplied SMTP settings. The backend
// sends to the current admin's email when `to` is omitted.
export interface EmailTestConfig {
  smtpHost: string
  smtpPort: number
  smtpUser: string
  smtpPassword: string
  smtpFrom?: string
  smtpFromName?: string
  to?: string
}

export function useTestEmail() {
  return useMutation<{ message: string }, Error, EmailTestConfig>({
    mutationFn: async (cfg) => {
      const res = await api.post('/api/v1/settings/system/email/test', cfg)
      return res.data as { message: string }
    },
  })
}
