// APIKey is a per-user automation credential. The plaintext token is only ever
// returned once, at creation time (see CreatedAPIKey).
export interface APIKey {
  id: string
  user_id: string
  name: string
  prefix: string
  last_used_at?: string
  expires_at?: string
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface CreateAPIKeyRequest {
  name: string
  expires_at?: string
}

// CreatedAPIKey is the one-time creation response that carries the plaintext token.
export interface CreatedAPIKey extends APIKey {
  token: string
}
