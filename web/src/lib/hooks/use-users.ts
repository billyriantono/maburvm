import {
  useQuery,
  keepPreviousData,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  User,
  AccountSuspension,
  CreateUserRequest,
  UpdateUserRequest,
  PaginatedResponse,
} from '@/types'

interface UserListParams {
  page?: number
  pageSize?: number
  role?: string
  search?: string
}

export function useUsers(params: UserListParams = {}) {
  const { page = 1, pageSize = 20, role, search } = params

  return useQuery<PaginatedResponse<User>>({
    queryKey: ['users', 'list', { page, pageSize, role, search }],
    queryFn: async () => {
      const response = await api.get<PaginatedResponse<User>>('/api/v1/users', {
        params: {
          page,
          page_size: pageSize,
          role,
          search: search || undefined,
        },
      })
      // Unlike most endpoints, /users returns the paginated payload at the top
      // level ({ data, total, page, ... }) rather than the standard { data }
      // envelope, so the response body itself is the PaginatedResponse.
      return response.data as unknown as PaginatedResponse<User>
    },
    placeholderData: keepPreviousData,
  })
}

export function useUser(id: string, options?: UseQueryOptions<User>) {
  return useQuery<User>({
    queryKey: ['users', 'detail', id],
    queryFn: async () => {
      const response = await api.get<User>(`/api/v1/users/${id}`)
      return response.data.data
    },
    enabled: !!id,
    ...options,
  })
}

export function useCreateUser() {
  const queryClient = useQueryClient()

  return useMutation<User, Error, CreateUserRequest>({
    mutationFn: async (data) => {
      const response = await api.post<User>('/api/v1/users', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}

export function useUpdateUser(id: string) {
  const queryClient = useQueryClient()

  return useMutation<User, Error, UpdateUserRequest>({
    mutationFn: async (data) => {
      const response = await api.put<User>(`/api/v1/users/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}

/** Suspend a customer account, or restore it. withVms does the same to its VMs. */
export function useSetUserSuspended(id: string) {
  const queryClient = useQueryClient()

  return useMutation<AccountSuspension, Error, { suspend: boolean; withVms: boolean; reason?: string }>({
    mutationFn: async ({ suspend, withVms, reason }) => {
      const response = await api.post<AccountSuspension>(
        `/api/v1/users/${id}/${suspend ? 'suspend' : 'unsuspend'}`,
        { with_vms: withVms, reason },
        // Each VM is powered off or started on its node in turn.
        { timeout: 180000 },
      )
      return response.data.data
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
      queryClient.invalidateQueries({ queryKey: ['vms'] })
    },
  })
}

/**
 * A new password for someone who cannot sign in: "email" sends them a link,
 * "temporary" sets a random one and returns it once.
 */
export function useResetUserPassword(id: string) {
  return useMutation<{ sent_to?: string; temporary_password?: string }, Error, 'email' | 'temporary'>({
    mutationFn: async (method) => {
      const response = await api.post<{ sent_to?: string; temporary_password?: string }>(
        `/api/v1/users/${id}/reset-password`,
        { method },
      )
      return response.data.data
    },
  })
}

/** Takes two-factor off an account whose owner lost the authenticator and the backup codes. */
export function useResetUserTwoFactor(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(`/api/v1/users/${id}/reset-2fa`)
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}

/** Lifts a sign-in lock before it runs out by itself. */
export function useUnlockUser(id: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(`/api/v1/users/${id}/unlock`)
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}

export function useDeleteUser() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/users/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}
