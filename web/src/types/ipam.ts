export type IPFamily = 'ipv4' | 'ipv6'

export type IPAddressStatus = 'available' | 'reserved' | 'assigned' | 'disabled'

export interface IPPool {
  id: string
  name: string
  node_id?: string
  node_ids?: string[]
  family: IPFamily
  cidr?: string
  gateway?: string
  bridge?: string
  range_start?: string
  range_end?: string
  description?: string
  created_at: string
  updated_at: string
}

export interface IPAddress {
  id: string
  pool_id: string
  node_id?: string
  address: string
  family: IPFamily
  status: IPAddressStatus
  vm_id?: string
  note?: string
  rdns?: string
  created_at: string
  updated_at: string
}

export interface CreateIPPoolRequest {
  name: string
  node_ids?: string[]
  family: IPFamily
  cidr?: string
  gateway?: string
  bridge?: string
  range_start?: string
  range_end?: string
  description?: string
}

export interface CreateIPAddressRequest {
  node_id?: string
  address: string
  family?: IPFamily
  status?: IPAddressStatus
  note?: string
}

export interface AllocateIPAddressRequest {
  pool_id?: string
  node_id?: string
  vm_id?: string
}
