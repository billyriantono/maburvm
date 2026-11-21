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
  created_at: string;
  updated_at: string;
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
}
