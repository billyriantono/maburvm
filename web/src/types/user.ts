// UserRole matches the Go UserRole type
export type UserRole = 'admin' | 'client';

// User represents a user in the system
export interface User {
  id: string;
  name?: string;
  email: string;
  role: UserRole;
  two_factor_enabled?: boolean;
  /** From /auth/me: an administrator who must turn on two-factor before anything else. */
  two_factor_setup_required?: boolean;
  ip_whitelist: string[];
  /** Set while the account is suspended: no sign-in, sessions and API keys stopped. */
  suspended_at?: string;
  /** Sign-in refused until then, after too many failed attempts. May be in the past. */
  locked_until?: string;
  created_at: string;
  updated_at: string;
}

/** Whether sign-in is refused right now after too many failed attempts. */
export function isSignInLocked(user: Pick<User, 'locked_until'>): boolean {
  return !!user.locked_until && new Date(user.locked_until).getTime() > Date.now();
}

/** What suspending or restoring an account did. */
export interface AccountSuspension {
  user: User;
  /** VMs suspended or restored along with the account. */
  vms_changed: number;
  /** Hostnames of VMs that could not be powered off or started. */
  vms_failed: string[];
}

// CreateUserRequest for creating new users
export interface CreateUserRequest {
  name?: string;
  email: string;
  password: string;
  role?: UserRole;
  ip_whitelist?: string[];
  send_welcome_email?: boolean;
}

// UpdateUserRequest for updating users
export interface UpdateUserRequest {
  name?: string;
  email?: string;
  role?: UserRole;
  ip_whitelist?: string[];
}

// Enable2FAResponse with backup codes
export interface Enable2FAResponse {
  secret: string;
  backup_codes: string[];
}
