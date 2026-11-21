import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import { accountParams, useActingAccount } from '@/lib/acting-account'
import type { Recipe, RecipeRequest } from '@/types/recipe'

/** accountId lists another account's in place of the page's acting account. */
export function useRecipes(accountId?: string, enabled = true) {
  const acting = useActingAccount()
  const account = accountId ?? acting
  return useQuery<Recipe[]>({
    enabled,
    queryKey: ['recipes', 'list', account ?? 'own'],
    queryFn: async () => {
      const response = await api.get<Recipe[]>('/api/v1/recipes', accountParams(account))
      return response.data.data
    },
  })
}

export function useCreateRecipe() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<Recipe, Error, RecipeRequest>({
    mutationFn: async (data) => {
      const response = await api.post<Recipe>('/api/v1/recipes', data, accountParams(account))
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['recipes', 'list'] })
    },
  })
}

export function useUpdateRecipe() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<Recipe, Error, { id: string; data: RecipeRequest }>({
    mutationFn: async ({ id, data }) => {
      const response = await api.put<Recipe>(`/api/v1/recipes/${id}`, data, accountParams(account))
      return response.data.data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['recipes', 'list'] })
    },
  })
}

export function useDeleteRecipe() {
  const account = useActingAccount()
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/recipes/${id}`, accountParams(account))
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['recipes', 'list'] })
    },
  })
}
