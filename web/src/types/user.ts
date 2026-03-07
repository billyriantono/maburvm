export type UserRole = 'admin' | 'client'

export interface User {
  id: string
  email: string
  role: UserRole
  two_factor_secret?: string
  ip_whitelist: string[]
  created_at: string
  updated_at: string
}

export interface CreateUserRequest {
  email: string
  password: string
  role: UserRole
  ip_whitelist?: string[]
}

export interface UpdateUserRequest {
  email?: string
  role?: UserRole
  ip_whitelist?: string[]
}

export interface LoginRequest {
  email: string
  password: string
  totp_code?: string
}

export interface LoginResponse {
  token: string
  user: User
  requires_2fa?: boolean
}

export interface TwoFASetup {
  secret: string
  qr_code: string
  backup_codes: string[]
}

export interface Session {
  id: string
  user_id: string
  expires_at: string
  ip_address?: string
  user_agent?: string
  created_at: string
  updated_at: string
}

export interface ChangePasswordRequest {
  current_password: string
  new_password: string
}
