// The kinds of system a template can hold. The kind decides how a VM built
// from it is set up, and which account it is reached with.
export type OSType = "linux" | "routeros" | "windows";

// OSTemplate represents an operating system template for VM provisioning
export interface OSTemplate {
  id: string;
  name: string;
  version: string;
  image_path: string;
  size_bytes?: number;
  is_active: boolean;
  // What kind of system the image holds; decides how a VM from it is set up.
  os_type?: OSType;
  description?: string;
  // A system that costs a licence per VM. Customers install it only on a plan
  // in plan_ids; with no plans listed, only staff and billing orders do.
  licensed?: boolean;
  // The plans the template is offered on. Empty or null means every plan.
  plan_ids?: string[] | null;
  // "disk" (a disk image VMs are built from) or "iso" (an installer).
  kind?: TemplateKind;
  // The sha256 every node checks its download against; empty when none.
  checksum_sha256?: string;
  // A checksum list (SHA256SUMS) the checksum is read from again on refresh.
  checksum_url?: string;
  checksum_checked_at?: string | null;
  // Plain http accepted for the image address.
  allow_http?: boolean;
  // Checked against checksum_url daily, and fetched again when it changed.
  auto_refresh?: boolean;
  firmware?: Firmware;
  // VMs installed from the image get Secure Boot (UEFI only) and a virtual TPM.
  secure_boot?: boolean;
  tpm?: boolean;
  // Per node, with include_nodes: whether the image is there, and its checksum.
  nodes?: TemplateNodeStatus[];
  created_at: string;
  updated_at: string;
}

export type TemplateKind = 'disk' | 'iso';
export type Firmware = 'bios' | 'uefi';

export interface TemplateNodeStatus {
  node_id: string;
  node_name: string;
  status: 'pending' | 'syncing' | 'available' | 'error';
  error_msg?: string;
  synced_at?: string | null;
  checksum?: string;
}

export interface TemplateRefreshResult {
  template_id: string;
  changed: boolean;
  old_checksum: string;
  checksum: string;
  node_ids?: string[] | null;
}

// Whether a customer may install the template on a server of this plan. The
// panel enforces the same rule; this only keeps the choice off the screen.
export function templateOfferedOn(t: OSTemplate, planId?: string | null): boolean {
  const plans = t.plan_ids ?? [];
  if (plans.length === 0) return !t.licensed;
  return !!planId && plans.includes(planId);
}

// CreateTemplateRequest for creating OS templates.
// The backend downloads/caches the image on the node from this URL on first use.
export interface CreateTemplateRequest {
  name: string;
  version: string;
  file_url: string;
  description?: string;
  os_type?: OSType;
  kind?: TemplateKind;
  // One of these is required for an http(s) image, unless skip_checksum.
  checksum_sha256?: string;
  checksum_url?: string;
  skip_checksum?: boolean;
  allow_http?: boolean;
  auto_refresh?: boolean;
  firmware?: Firmware;
  secure_boot?: boolean;
  tpm?: boolean;
}

// UpdateTemplateRequest for updating OS templates
export interface UpdateTemplateRequest {
  name?: string;
  version?: string;
  image_path?: string;
  is_active?: boolean;
  description?: string;
  licensed?: boolean;
  plan_ids?: string[];
  checksum_sha256?: string;
  checksum_url?: string;
  allow_http?: boolean;
  auto_refresh?: boolean;
  firmware?: Firmware;
  secure_boot?: boolean;
  tpm?: boolean;
}
