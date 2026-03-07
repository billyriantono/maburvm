export type TemplateType = 'template' | 'iso'

export interface OSTemplate {
  id: string
  name: string
  version: string
  image_path: string
  is_active: boolean
  description?: string
  created_at: string
  updated_at: string
}

export interface CreateTemplateRequest {
  name: string
  version: string
  image_path: string
  description?: string
}

export interface UpdateTemplateRequest {
  name?: string
  version?: string
  description?: string
  is_active?: boolean
}
