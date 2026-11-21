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
