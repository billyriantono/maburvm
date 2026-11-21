// Moving a VM to another node, and draining a node.

export type MigrationMode = 'live' | 'offline' | '';

export interface MigrationCheck {
  vm_id: string;
  hostname: string;
  source_node_id: string;
  source_node_name: string;
  dest_node_id: string;
  dest_node_name: string;
  dest_pool_id: string;
  dest_pool_name: string;
  dest_pool_path: string;
  /** live: memory and disks copied while the VM runs. offline: a stopped VM's disks. */
  mode: MigrationMode;
  /** The VM's CPU mode: host-passthrough moves live only between identical CPUs. */
  cpu_mode: string;
  disk_gb: number;
  /** About how much the copy moves, in bytes. */
  copy_bytes: number;
  blockers: string[];
  warnings: string[];
  can_move: boolean;
  /** Drives on shared storage both nodes use: they stay where they are. */
  shared_disks?: string[];
  /** Every disk is one of those, so no disk is copied. */
  copy_free?: boolean;
}

export type MigrationStatus = 'queued' | 'running' | 'cancelling' | 'completed' | 'failed' | 'cancelled' | 'skipped';

export interface VMMigration {
  id: string;
  vm_id: string;
  source_node_id: string;
  dest_node_id: string | null;
  dest_pool_id: string | null;
  drain_id?: string;
  mode: MigrationMode;
  status: MigrationStatus;
  phase: string;
  bandwidth_mbps: number;
  bytes_total: number;
  bytes_done: number;
  error: string;
  /** What did not work after the VM moved (it moved regardless). */
  note: string;
  created_at: string;
  started_at?: string;
  updated_at: string;
  finished_at?: string;
}

export interface MigrationRequest {
  dest_node_id?: string;
  dest_pool_id?: string;
  mode?: MigrationMode;
  bandwidth_mbps?: number;
}

export interface DrainPlanItem {
  vm_id: string;
  hostname: string;
  status: string;
  mode: MigrationMode;
  dest_node_id: string;
  dest_node_name: string;
  can_move: boolean;
  blockers: string[];
  warnings: string[];
}

export interface DrainPlan {
  node_id: string;
  node_name: string;
  items: DrainPlanItem[];
  movable: number;
  blocked: number;
}

export type DrainStatus = 'running' | 'aborting' | 'completed' | 'incomplete' | 'aborted';

export interface NodeDrain {
  id: string;
  node_id: string;
  status: DrainStatus;
  concurrency: number;
  destinations: Record<string, string>;
  error: string;
  started_at: string;
  updated_at: string;
  finished_at?: string;
}

export interface DrainReportItem extends VMMigration {
  hostname: string;
  dest_node_name: string;
}

export interface DrainReport {
  drain: NodeDrain | null;
  items: DrainReportItem[];
  moved: number;
  failed: number;
  skipped: number;
  /** VMs still on the node. */
  remaining: number;
}

export interface NodeLink {
  key_fingerprint: string;
  host_key_fingerprints: string[];
  recorded_at: string | null;
}

// Recovering the VMs of a node that went down on other nodes.

export type RecoveryMethod = 'shared' | 'backup' | '';
export type RecoveryStatus = 'queued' | 'running' | 'completed' | 'failed';
/** What became of the VM's copy on the node it was recovered from. */
export type OldCopyState = '' | 'pending' | 'fenced' | 'running' | 'removed';

export interface VMRecovery {
  id: string;
  vm_id: string;
  source_node_id: string;
  dest_node_id: string;
  method: RecoveryMethod;
  backup_id?: string;
  start_vm: boolean;
  new_address: boolean;
  status: RecoveryStatus;
  phase: string;
  error: string;
  /** What did not work, or was changed, as the VM was recovered. */
  note: string;
  moved: boolean;
  old_copy: OldCopyState;
  created_at: string;
  finished_at?: string;
}

export interface RecoveryOption {
  node_id: string;
  node_name: string;
  method: RecoveryMethod;
  can_recover: boolean;
  /** Only with new addresses from that node's pools. */
  can_with_new_address: boolean;
  blockers: string[];
  address_blockers: string[];
  warnings: string[];
}

export interface RecoveryPlanItem {
  vm_id: string;
  hostname: string;
  status: string;
  disk_gb: number;
  /** The shared pool all its disks are on; "" when they are not. */
  shared_pool: string;
  backup: { id: string; completed_at: string | null; size: number; mode: string } | null;
  options: RecoveryOption[];
  /** Why it cannot be recovered anywhere; "" when it can. */
  reason: string;
  active?: VMRecovery;
}

export interface RecoveryRun extends VMRecovery {
  hostname: string;
  dest_node_name: string;
}

export interface RecoveryPlan {
  node_id: string;
  node_name: string;
  node_status: string;
  last_report_at: string | null;
  /** Why nothing can be recovered from this node now; "" when it can. */
  refusal: string;
  items: RecoveryPlanItem[];
  runs: RecoveryRun[];
}

export interface RecoveryChoice {
  vm_id: string;
  dest_node_id: string;
  new_address: boolean;
  start: boolean;
}

export interface RecoveryStarted {
  queued: VMRecovery[];
  refused: { vm_id: string; hostname: string; reason: string }[];
}

export interface RecoveryLeftover extends RecoveryRun {
  defined: boolean;
  state: string;
  disks: { target: string; path: string; size_bytes?: number; shared?: boolean }[] | null;
}
