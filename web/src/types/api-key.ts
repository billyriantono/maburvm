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
  // "client" = narrowed to the owner's own resources; absent = owner's role.
  role?: "admin" | "client"
  read_only: boolean
  // What the key may do within its role. Keys made before scopes read as
  // "read_only" or "full".
  scope?: APIKeyScope
  // Addresses or CIDR ranges the key works from; empty = anywhere.
  allowed_ips?: string[] | null
  created_at: string
  updated_at: string
}

export type APIKeyScope = "full" | "read_only" | "vm_power" | "billing"

export interface CreateAPIKeyRequest {
  name: string
  expires_at?: string
  role?: "client"
  scope?: APIKeyScope
  allowed_ips?: string[]
}

// CreatedAPIKey is the one-time creation response that carries the plaintext token.
export interface CreatedAPIKey extends APIKey {
  token: string
}
