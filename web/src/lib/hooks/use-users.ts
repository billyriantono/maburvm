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
  role?: string;
}

export function useUsers(params: UserListParams = {}) {
  const { page = 1, pageSize = 20, role } = params

  return useQuery<PaginatedResponse<User>>({
    queryKey: ['users', 'list', { page, pageSize, role }],
    queryFn: async () => {
      const response = await api.get<PaginatedResponse<User>>('/api/v1/users', {
        params: {
          page,
          page_size: pageSize,
          role,
        },
      })
      return response.data.data
    },
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
