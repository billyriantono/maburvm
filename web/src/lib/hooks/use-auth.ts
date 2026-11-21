import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { User } from '@/types'

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

/**
 * Whether the account may do something, for showing or hiding a control. It
 * mirrors middleware.Can: "admin:access" is the Administrator's wildcard,
 * which leaves out staff management. The panel checks every request anyway.
 */
export function can(user: Pick<User, 'permissions'> | undefined, permission: string): boolean {
  const held = user?.permissions ?? []
  return held.includes(permission) || (held.includes('admin:access') && permission !== 'staff:manage')
}

/** can() for the signed-in account. */
export function useCan() {
  const { data } = useCurrentUser()
  return (permission: string) => can(data, permission)
}

// Alias for useCurrentUser - provides a cleaner API for auth checks
export function useAuth(
  options?: Omit<UseQueryOptions<User>, 'queryKey' | 'queryFn'>
) {
  return useCurrentUser(options)
}

export function useLogout() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, void>({
    mutationFn: async () => {
      try {
        // The panel revokes the session and removes the HttpOnly cookie.
        await api.post('/api/v1/auth/logout')
      } catch {
        // The panel removes the cookie even when it refuses the session.
      }
    },
    onSuccess: () => {
      queryClient.clear()
    },
  })
}
