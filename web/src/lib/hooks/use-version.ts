import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

export interface BuildInfo {
  version: string
  commit: string
  short_sha: string
  build_time: string
  /** False when the build carries no revision — unknown, not up to date. */
  stamped: boolean
  go_version: string
}

export interface NodeBuild {
  node_id: string
  node_name: string
  status: string
  version: string
  commit: string
  short_sha: string
  build_time: string
  matches_panel: boolean
  error?: string
}

export interface SystemVersion {
  panel: BuildInfo
  nodes: NodeBuild[]
}

// Node agents are deployed separately from the panel, so a single version number
// would be misleading: the panel can be current while a node is still on an
// older agent — deliberately so, when that node has a long export in flight.
export function useSystemVersion() {
  return useQuery<SystemVersion>({
    queryKey: ['system', 'version'],
    queryFn: async () => {
      const response = await api.get<SystemVersion>('/api/v1/system/version')
      return response.data.data
    },
    // Each node is contacted over gRPC, so this is not free to poll.
    staleTime: 60_000,
  })
}
