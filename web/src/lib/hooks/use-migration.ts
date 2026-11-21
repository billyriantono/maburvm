import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type {
  DrainPlan,
  DrainReport,
  MigrationCheck,
  MigrationRequest,
  NodeDrain,
  NodeLink,
  RecoveryChoice,
  RecoveryLeftover,
  RecoveryPlan,
  RecoveryStarted,
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

// useNodeRecovery is what could be recovered from a node that went down, and
// the recoveries made from it. Polled while the node is down or one runs.
export function useNodeRecovery(nodeId: string) {
  return useQuery<RecoveryPlan>({
    queryKey: ['node-recovery', nodeId],
    queryFn: async () => (await api.get<RecoveryPlan>(`/api/v1/nodes/${nodeId}/recovery`)).data.data,
    enabled: !!nodeId,
    refetchInterval: (query) => {
      const plan = query.state.data as RecoveryPlan | undefined
      if (plan?.runs.some((r) => r.status === 'queued' || r.status === 'running')) return 3000
      return plan?.node_status === 'offline' ? 30000 : false
    },
  })
}

export function useStartRecovery(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation<RecoveryStarted, Error, { node_is_down: boolean; vms: RecoveryChoice[] }>({
    mutationFn: async (data) => (await api.post<RecoveryStarted>(`/api/v1/nodes/${nodeId}/recovery`, data)).data.data,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['node-recovery', nodeId] })
      queryClient.invalidateQueries({ queryKey: ['nodes'] })
    },
  })
}

// useRecoveryLeftovers asks a node that is back what it still holds of the
// VMs recovered from it. Asking also tells the node again not to run them.
export function useRecoveryLeftovers(nodeId: string, enabled: boolean) {
  return useQuery<RecoveryLeftover[]>({
    queryKey: ['node-recovery-leftovers', nodeId],
    queryFn: async () => (await api.get<RecoveryLeftover[]>(`/api/v1/nodes/${nodeId}/recovery/leftovers`)).data.data,
    enabled: !!nodeId && enabled,
    retry: false,
  })
}

export function useRemoveLeftover(nodeId: string) {
  const queryClient = useQueryClient()
  return useMutation<{ removed: string[] | null; kept: string[] | null }, Error, { vmId: string; forceStop: boolean }>({
    mutationFn: async ({ vmId, forceStop }) =>
      (await api.post<{ removed: string[] | null; kept: string[] | null }>(
        `/api/v1/nodes/${nodeId}/recovery/leftovers/${vmId}/remove`, { force_stop: forceStop })).data.data,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['node-recovery-leftovers', nodeId] })
      queryClient.invalidateQueries({ queryKey: ['node-recovery', nodeId] })
    },
  })
}
