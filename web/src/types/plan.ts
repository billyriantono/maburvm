// Plan is a VPS flavor: a named bundle of resources.
export interface Plan {
  id: string
  name: string
  cpu: number
  ram: number // MB
  disk: number // GB
  bandwidth_mbps: number
  description?: string
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface CreatePlanRequest {
  name: string
  cpu: number
  ram: number
  disk: number
  bandwidth_mbps?: number
  description?: string
  is_active?: boolean
}
