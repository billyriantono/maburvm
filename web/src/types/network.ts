// Network represents a network configuration for a VM. Gateway/netmask/bridge/
// rdns/pool_id are enriched server-side from the owning IPAM pool & address when
// the interface IP is tracked in IPAM (best-effort, may be empty otherwise).
export interface Network {
  id: string;
  vm_id: string;
  ip_address: string;
  bandwidth_limit: number;
  vlan_id?: number;
  gateway?: string;
  netmask?: string;
  bridge?: string;
  rdns?: string;
  pool_id?: string;
  created_at: string;
  updated_at: string;
}

// ManagedNetwork is an administrator-defined virtual network (bridge/NAT/isolated).
export interface ManagedNetwork {
  id: string;
  name: string;
  type: string; // bridge | nat | isolated
  bridge?: string;
  subnet?: string;
  gateway?: string;
  dhcp_start?: string;
  dhcp_end?: string;
  vlan_id?: number;
  node_id?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateManagedNetworkRequest {
  name: string;
  type: string;
  bridge?: string;
  subnet?: string;
  gateway?: string;
  dhcp_start?: string;
  dhcp_end?: string;
  vlan_id?: number;
}

// PortForward represents a port forwarding (NAT) rule for a VM
export interface PortForward {
  id: string;
  vm_id: string;
  network_id: string;
  external_port: number;
  internal_port: number;
  protocol: 'tcp' | 'udp';
  source_ip: string;
  created_at: string;
  updated_at: string;
}

// FirewallRule represents a firewall rule for a VM
export interface FirewallRule {
  id: string;
  vm_id: string;
  protocol: 'tcp' | 'udp' | 'icmp' | 'all';
  port_range?: string;
  action: 'allow' | 'deny';
  direction: 'inbound' | 'outbound';
  source_ip: string;
  priority: number;
  created_at: string;
  updated_at: string;
}

// CreateNetworkRequest for creating networks
export interface CreateNetworkRequest {
  vm_id: string;
  ip_address?: string;
  bandwidth_limit?: number;
  vlan_id?: number;
}

// CreatePortForwardRequest for creating port forwards
export interface CreatePortForwardRequest {
  vm_id: string;
  network_id: string;
  external_port: number;
  internal_port: number;
  protocol?: 'tcp' | 'udp';
  source_ip?: string;
}

// CreateFirewallRuleRequest for creating firewall rules
export interface CreateFirewallRuleRequest {
  vm_id: string;
  protocol: 'tcp' | 'udp' | 'icmp' | 'all';
  port_range?: string;
  action: 'allow' | 'deny';
  direction: 'inbound' | 'outbound';
  source_ip?: string;
  priority?: number;
}
