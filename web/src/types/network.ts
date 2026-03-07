export interface Network {
  id: string
  vm_id: string
  ip_address: string
  bandwidth_limit: number
  vlan_id?: number
  created_at: string
  updated_at: string
}

export interface PortForward {
  id: string
  vm_id: string
  network_id: string
  external_port: number
  internal_port: number
  protocol: 'tcp' | 'udp'
  source_ip: string
  created_at: string
  updated_at: string
}

export interface FirewallRule {
  id: string
  vm_id: string
  protocol: 'tcp' | 'udp' | 'icmp' | 'all'
  port_range?: string
  action: 'allow' | 'deny'
  direction: 'inbound' | 'outbound'
  source_ip: string
  priority: number
  created_at: string
  updated_at: string
}

export interface CreateNetworkRequest {
  vm_id: string
  ip_address: string
  bandwidth_limit?: number
  vlan_id?: number
}

export interface CreatePortForwardRequest {
  vm_id: string
  network_id: string
  external_port: number
  internal_port: number
  protocol?: 'tcp' | 'udp'
  source_ip?: string
}

export interface CreateFirewallRuleRequest {
  vm_id: string
  protocol: 'tcp' | 'udp' | 'icmp' | 'all'
  port_range?: string
  action: 'allow' | 'deny'
  direction: 'inbound' | 'outbound'
  source_ip?: string
  priority?: number
}
