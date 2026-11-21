// Network represents a network configuration for a VM. Gateway/netmask/bridge/
// rdns/pool_id are enriched server-side from the owning IPAM pool & address when
// the interface IP is tracked in IPAM (best-effort, may be empty otherwise).
export interface Network {
  id: string;
  vm_id: string;
  ip_address: string;
  bandwidth_limit: number;
  vlan_id?: number;
  /** Drop traffic from this VM that claims an address it was not given. */
  anti_spoofing?: boolean;
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
  node_id?: string;
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
  /** deny drops silently; reject answers the sender so it gives up at once. */
  action: FirewallAction;
  direction: 'inbound' | 'outbound';
  source_ip: string;
  priority: number;
  /** One ICMP type ("echo-request", or a number with a family); empty = all. */
  icmp_type?: string;
  /** Matches are written to the node's kernel log. */
  log?: boolean;
  /** Empty applies the rule to IPv4 and IPv6. */
  family?: FirewallFamily;
  created_at: string;
  updated_at: string;
}

export type FirewallAction = 'allow' | 'deny' | 'reject';
export type FirewallFamily = '' | 'v4' | 'v6';

// CreateNetworkRequest for creating networks
export interface CreateNetworkRequest {
  vm_id: string;
  ip_address?: string;
  bandwidth_limit?: number;
  vlan_id?: number;
}

// CreatePortForwardRequest for creating port forwards
export interface CreatePortForwardRequest {
  vm_id?: string;
  network_id?: string;
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
  action: FirewallAction;
  direction: 'inbound' | 'outbound';
  source_ip?: string;
  priority?: number;
  icmp_type?: string;
  log?: boolean;
  family?: FirewallFamily;
}

/** Where an attached group's rules sit: before the VM's own rules (they
 * cannot override it) or after (they can). */
export type FirewallGroupPosition = 'before' | 'after';

/** A named rule set attached to many VMs. user_id null: staff manage it for everyone. */
export interface FirewallGroup {
  id: string;
  user_id: string | null;
  name: string;
  description: string;
  owner_email?: string;
  rule_count: number;
  vm_count: number;
  created_at: string;
  updated_at: string;
}

export interface FirewallGroupRule {
  id: string;
  group_id: string;
  protocol: 'tcp' | 'udp' | 'icmp' | 'all';
  port_range?: string;
  action: FirewallAction;
  direction: 'inbound' | 'outbound';
  source_ip: string;
  priority: number;
  icmp_type?: string;
  log?: boolean;
  family?: FirewallFamily;
}

export interface FirewallGroupDetail extends FirewallGroup {
  rules: FirewallGroupRule[];
  vms: { id: string; hostname: string; position: FirewallGroupPosition }[];
}

/** A group as attached to one VM. */
export interface AttachedFirewallGroup {
  id: string;
  name: string;
  global: boolean;
  position: FirewallGroupPosition;
  sort_order: number;
  rules: FirewallGroupRule[];
}

/** A hostname routed to a NAT VM through its node's web ports. */
export interface VMHostname {
  id: string;
  vm_id: string;
  hostname: string;
  http_port: number;
  https_port: number;
  verify_token: string;
  verified_at?: string | null;
  /** Why the node is not serving it, as the node last said. */
  node_error: string;
  created_at: string;
}

/** What a NAT VM's owner needs to reach it; nat false for a VM with its own address. */
export interface VMNATInfo {
  nat: boolean;
  public_address: string;
  port_start: number;
  port_end: number;
  remote_port: number;
  remote_target: number;
  hostnames: VMHostname[];
  hostname_limit: number;
  verify_prefix: string;
}

/** A node's range of public ports for NAT VMs; 0 and 0 offers none. */
export interface NodeNATSettings {
  port_start: number;
  port_end: number;
  address: string;
  blocks_used: number;
  ports_used: number;
}
