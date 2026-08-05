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
  /** Customers may order a floating IP from this pool. Off unless an operator opens it. */
  orderable?: boolean
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
  /** 'floating' = lives on the host and is 1:1-NATed to the VM, so it can be
   *  moved between VMs and survives deletion of the VM it was attached to. */
  delivery_mode?: IPDeliveryMode
  /** 'inbound' = DNAT only (VM keeps its own egress identity);
   *  'full' = DNAT + SNAT (VM egresses as the floating IP). */
  nat_mode?: NATMode
  /** Tenant owning a floating IP while it is attached to no VM. */
  user_id?: string
  note?: string
  rdns?: string
  created_at: string
  updated_at: string
}

export type IPDeliveryMode = 'direct' | 'floating'
export type NATMode = 'inbound' | 'full'

export interface AllocateFloatingIPRequest {
  pool_id: string
  node_id?: string
  user_id?: string
  requested_ip?: string
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

// UpdateIPPoolRequest patches an existing pool's editable metadata. Omitted
// fields are left unchanged; the bridge is the field that lets a stuck VM boot
// (the VM re-reads it on its next start).
export interface UpdateIPPoolRequest {
  name?: string
  gateway?: string
  bridge?: string
  description?: string
  node_ids?: string[]
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

// A VPC is a customer-defined private network. Two customers may hold the SAME
// subnet — each one's gateway lives in its own router namespace on the node — so
// only a customer's own VPCs are checked against each other for overlap.
export interface VPC {
  id: string
  name: string
  type: string
  subnet: string
  gateway: string
  bridge?: string
  node_id?: string
  user_id?: string
  created_at: string
}

export interface CreateVPCRequest {
  name: string
  subnet: string
  gateway?: string
}

export interface FloatingIPBilling {
  user_id: string
  total: number
  free: number
  billable: number
}

// A region is the location a customer picks when ordering: a city holding one or
// more nodes. `flag` arrives from the API as a Unicode flag glyph, so no icon
// assets are shipped and nothing needs updating when a region is added.
export interface Region {
  id: string
  slug: string
  name: string
  country: string
  flag: string
  enabled: boolean
  node_count: number
}

export interface CreateRegionRequest {
  slug: string
  name: string
  country: string
  enabled?: boolean
}
