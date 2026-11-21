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
  /** A staff account's role id (owner, administrator, support, billing, readonly or a custom role's id). */
  staff_role?: string;
  staff_role_name?: string;
  /** The account unsubscribed from news and offers by email. */
  marketing_opt_out?: boolean;
  /** From /auth/me: what this account may do. The panel checks every request regardless. */
  permissions?: string[];
  /** From /auth/me: the staff member viewing the panel as this customer. */
  impersonator?: { id: string; email: string };
  /** Profile details the account holder edits. */
  phone?: string;
  company?: string;
  /** IANA name such as Asia/Jakarta; empty follows the browser. */
  timezone?: string;
  locale?: string;
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
  /** A new staff account's role; Administrator when left out. */
  staff_role?: string;
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

/** One signed-in session of an account. */
export interface AccountSession {
  id: string;
  method: 'password' | 'sso' | 'impersonation' | string;
  ip_address: string;
  user_agent: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  current: boolean;
  /** The staff member behind a view as this customer. */
  impersonator_email?: string;
}

/** One sign-in attempt, or a password reset request. */
export interface SignInEvent {
  id: string;
  email: string;
  method: 'password' | 'sso' | 'impersonation' | 'password_reset' | string;
  success: boolean;
  /** Why it failed, or for a staff view, who opened it. */
  note: string;
  ip_address: string;
  user_agent: string;
  created_at: string;
}

export interface PermissionArea {
  area: string;
  permissions: { key: string; label: string }[];
}

export interface StaffRole {
  id: string;
  name: string;
  description: string;
  permissions: string[];
  built_in: boolean;
  members: number;
}
