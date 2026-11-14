// The kinds of system a template can hold. The kind decides how a VM built
// from it is set up, and which account it is reached with.
export type OSType = "linux" | "routeros";

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
  created_at: string;
  updated_at: string;
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
}
