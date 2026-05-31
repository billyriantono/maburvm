import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Plan, CreatePlanRequest } from '@/types/plan'

export function usePlans(activeOnly = false) {
  return useQuery<Plan[]>({
    queryKey: ['plans', 'list', { activeOnly }],
    queryFn: async () => {
      const response = await api.get<Plan[]>('/api/v1/plans', {
        params: activeOnly ? { active: 'true' } : undefined,
      })
      return response.data.data
    },
  })
}

export function useCreatePlan() {
  const queryClient = useQueryClient()
  return useMutation<Plan, Error, CreatePlanRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Plan>('/api/v1/plans', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['plans', 'list'] })
    },
  })
}

export function useUpdatePlan() {
  const queryClient = useQueryClient()
  return useMutation<Plan, Error, { id: string; data: CreatePlanRequest }>({
    mutationFn: async ({ id, data }) => {
      const response = await api.put<Plan>(`/api/v1/plans/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['plans', 'list'] })
    },
  })
}

export function useDeletePlan() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/plans/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['plans', 'list'] })
    },
  })
}
