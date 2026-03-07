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
    queryFn: () => api.get<User>('/api/v1/auth/me'),
    ...options,
  })
}

export function useLogin() {
  const queryClient = useQueryClient()

  return useMutation<LoginResponse, Error, LoginRequest>({
    mutationFn: (data) => api.post<LoginResponse>('/api/v1/auth/login', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    },
  })
}

export function useLogout() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, void>({
    mutationFn: () => api.post('/api/v1/auth/logout'),
    onSuccess: () => {
      queryClient.clear()
    },
  })
}
