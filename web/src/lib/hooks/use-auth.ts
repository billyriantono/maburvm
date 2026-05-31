import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { User, LoginRequest, LoginResponse } from '@/types'

export function useCurrentUser(
  options?: Omit<UseQueryOptions<User>, 'queryKey' | 'queryFn'>
) {
  return useQuery<User>({
    queryKey: ['auth', 'me'],
    queryFn: async () => {
      const response = await api.get<User>('/api/v1/auth/me')
      return response.data.data
    },
    ...options,
  })
}

// Alias for useCurrentUser - provides a cleaner API for auth checks
export function useAuth(
  options?: Omit<UseQueryOptions<User>, 'queryKey' | 'queryFn'>
) {
  return useCurrentUser(options)
}

export function useLogin() {
  const queryClient = useQueryClient()

  return useMutation<LoginResponse, Error, LoginRequest>({
    mutationFn: async (data) => {
      const response = await api.post<LoginResponse>('/api/v1/auth/login', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}

export function useLogout() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, void>({
    mutationFn: async () => {
      try {
        await api.post('/api/v1/auth/logout')
      } catch {
        // Ignore logout API errors
      }
      // Always clear the cookie client-side
      document.cookie = 'accessToken=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT'
    },
    onSuccess: () => {
      queryClient.clear()
    },
  })
}
