// OSTemplate represents an operating system template for VM provisioning
export interface OSTemplate {
  id: string;
  name: string;
  version: string;
  image_path: string;
  is_active: boolean;
  description?: string;
  created_at: string;
  updated_at: string;
}

// CreateTemplateRequest for creating OS templates
export interface CreateTemplateRequest {
  name: string;
  version: string;
  image_path: string;
  description?: string;
}

// UpdateTemplateRequest for updating OS templates
export interface UpdateTemplateRequest {
  name?: string;
  version?: string;
  image_path?: string;
  is_active?: boolean;
  description?: string;
}
