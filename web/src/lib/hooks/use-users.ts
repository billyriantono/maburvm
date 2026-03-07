import {
  useQuery,
  useMutation,
  useQueryClient,
  UseQueryOptions,
} from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  User,
  CreateUserRequest,
  UpdateUserRequest,
  PaginatedResponse,
} from '@/types'

interface UserListParams {
  page?: number
  pageSize?: number
  role?: string
}

export function useUsers(params: UserListParams = {}) {
  const { page = 1, pageSize = 20, role } = params

  return useQuery<PaginatedResponse<User>>({
    queryKey: ['users', 'list', { page, pageSize, role }],
    queryFn: () =>
      api.get<PaginatedResponse<User>>('/api/v1/users', {
        page,
        page_size: pageSize,
        role,
      }),
  })
}

export function useUser(id: string, options?: UseQueryOptions<User>) {
  return useQuery<User>({
    queryKey: ['users', 'detail', id],
    queryFn: () => api.get<User>(`/api/v1/users/${id}`),
    enabled: !!id,
    ...options,
  })
}

export function useCreateUser() {
  const queryClient = useQueryClient()

  return useMutation<User, Error, CreateUserRequest>({
    mutationFn: (data) => api.post<User>('/api/v1/users', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}

export function useUpdateUser(id: string) {
  const queryClient = useQueryClient()

  return useMutation<User, Error, UpdateUserRequest>({
    mutationFn: (data) => api.put<User>(`/api/v1/users/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'detail', id] })
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}

export function useDeleteUser() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: (id) => api.delete(`/api/v1/users/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users', 'list'] })
    },
  })
}
