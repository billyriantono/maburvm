// SSHKey is a user's saved SSH public key, selectable when creating/rebuilding VMs.
export interface SSHKey {
  id: string;
  user_id: string;
  name: string;
  public_key: string;
  fingerprint: string;
  created_at: string;
  updated_at: string;
}

export interface CreateSSHKeyRequest {
  name?: string;
  public_key: string;
}

export interface GenerateSSHKeyRequest {
  name?: string;
}

// Returned exactly once — the private key is never stored server-side.
export interface GeneratedSSHKey {
  key: SSHKey;
  private_key: string;
}
