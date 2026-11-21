import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  DrainPlan,
  DrainReport,
  MigrationCheck,
  MigrationRequest,
  NodeDrain,
  NodeLink,
  VMMigration,
} from '@/types'

const ACTIVE = ['queued', 'running', 'cancelling']

export function migrationActive(m?: VMMigration | null) {
  return !!m && ACTIVE.includes(m.status)
}

// useMigrationCheck asks what moving the VM would do and what stops it. It
// changes nothing.
export function useMigrationCheck(vmId: string, req: MigrationRequest, enabled: boolean) {
  return useQuery<MigrationCheck>({
    queryKey: ['vm-migration-check', vmId, req.dest_node_id ?? '', req.dest_pool_id ?? '', req.mode ?? ''],
    queryFn: async () => {
      const response = await api.get<MigrationCheck>(`/api/v1/vms/${vmId}/migration/check`, {
        params: { dest_node_id: req.dest_node_id || undefined, dest_pool_id: req.dest_pool_id || undefined, mode: req.mode || undefined },
      })
      return response.data.data
    },
    enabled: !!vmId && enabled,
    staleTime: 0,
    retry: false,
  })
}

// useVMMigration is the VM's newest move, polled while it runs.
export function useVMMigration(vmId: string, enabled: boolean) {
  return useQuery<VMMigration | null>({
    queryKey: ['vm-migration', vmId],
    queryFn: async () => (await api.get<VMMigration | null>(`/api/v1/vms/${vmId}/migration`)).data.data,
    enabled: !!vmId && enabled,
    refetchInterval: (query) => (migrationActive(query.state.data as VMMigration | null | undefined) ? 3000 : false),
  })
}

export function useStartMigration(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<VMMigration, Error, MigrationRequest>({
    mutationFn: async (data) => (await api.post<VMMigration>(`/api/v1/vms/${vmId}/migrate`, data)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['vm-migration', vmId] }),
  })
}

export function useCancelMigration(vmId: string) {
  const queryClient = useQueryClient()
  return useMutation<VMMigration, Error, void>({
    mutationFn: async () => (await api.post<VMMigration>(`/api/v1/vms/${vmId}/migration/cancel`)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['vm-migration', vmId] }),
  })
}

// useNodeDrain is the node's latest drain and its migration link, polled while
// a drain runs.
export function useNodeDrain(nodeId: string) {
  return useQuery<{ report: DrainReport; link: NodeLink | null }>({
    queryKey: ['node-drain', nodeId],
    queryFn: async () => (await api.get<{ report: DrainReport; link: NodeLink | null }>(`/api/v1/nodes/${nodeId}/drain`)).data.data,
    enabled: !!nodeId,
    refetchInterval: (query) => {
      const st = (query.state.data as { report: DrainReport } | undefined)?.report.drain?.status
      return st === 'running' || st === 'aborting' ? 3000 : false
    },
  })
}

// useDrainPlan works out where each VM on the node would go. It changes nothing.
export function useDrainPlan(nodeId: string) {
  return useMutation<DrainPlan, Error, Record<string, string>>({
    mutationFn: async (destinations) =>
      (await api.post<DrainPlan>(`/api/v1/nodes/${nodeId}/drain/plan`, { destinations })).data.data,
  })
}

export function useStartDrain(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation<NodeDrain, Error, { concurrency: number; destinations: Record<string, string> }>({
    mutationFn: async (data) => (await api.post<NodeDrain>(`/api/v1/nodes/${nodeId}/drain`, data)).data.data,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['node-drain', nodeId] })
      queryClient.invalidateQueries({ queryKey: ['nodes'] })
    },
  })
}

export function useAbortDrain(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation<NodeDrain, Error, void>({
    mutationFn: async () => (await api.post<NodeDrain>(`/api/v1/nodes/${nodeId}/drain/abort`)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['node-drain', nodeId] }),
  })
}
