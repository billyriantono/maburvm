import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Recipe, RecipeRequest } from '@/types/recipe'

export function useRecipes() {
  return useQuery<Recipe[]>({
    queryKey: ['recipes', 'list'],
    queryFn: async () => {
      const response = await api.get<Recipe[]>('/api/v1/recipes')
      return response.data.data
    },
  })
}

export function useCreateRecipe() {
  const queryClient = useQueryClient()
  return useMutation<Recipe, Error, RecipeRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Recipe>('/api/v1/recipes', data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['recipes', 'list'] })
    },
  })
}

export function useUpdateRecipe() {
  const queryClient = useQueryClient()
  return useMutation<Recipe, Error, { id: string; data: RecipeRequest }>({
    mutationFn: async ({ id, data }) => {
      const response = await api.put<Recipe>(`/api/v1/recipes/${id}`, data)
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['recipes', 'list'] })
    },
  })
}

export function useDeleteRecipe() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/recipes/${id}`)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['recipes', 'list'] })
    },
  })
}
