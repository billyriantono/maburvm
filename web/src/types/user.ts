// UserRole matches the Go UserRole type
export type UserRole = 'admin' | 'client';

// User represents a user in the system
export interface User {
  id: string;
  email: string;
  role: UserRole;
  two_factor_secret?: string;
  ip_whitelist: string[];
  created_at: string;
  updated_at: string;
}

// Session represents a user session
export interface Session {
  id: string;
  user_id: string;
  expires_at: string;
  ip_address?: string;
  user_agent?: string;
  created_at: string;
  updated_at: string;
}

// LoginRequest for authentication
export interface LoginRequest {
  email: string;
  password: string;
  two_factor_code?: string;
}

// LoginResponse after successful authentication
export interface LoginResponse {
  access_token: string;
  refresh_token: string;
  user: User;
  expires_at: string;
}

// CreateUserRequest for creating new users
export interface CreateUserRequest {
  email: string;
  password: string;
  role?: UserRole;
  ip_whitelist?: string[];
}

// UpdateUserRequest for updating users
export interface UpdateUserRequest {
  email?: string;
  password?: string;
  role?: UserRole;
  ip_whitelist?: string[];
}

// Enable2FARequest for enabling 2FA
export interface Enable2FARequest {
  code: string;
}

// Enable2FAResponse with backup codes
export interface Enable2FAResponse {
  secret: string;
  backup_codes: string[];
}
