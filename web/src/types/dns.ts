export type DNSRecordType = "A" | "AAAA" | "CNAME" | "MX" | "TXT" | "NS" | "SRV"

export interface DNSZone {
  id: string
  name: string
  ttl: number
  primary_ns: string
  admin_email: string
  description?: string
  created_at: string
  updated_at: string
}

export interface DNSRecord {
  id: string
  zone_id: string
  name: string
  type: DNSRecordType
  content: string
  ttl: number
  priority: number
  created_at: string
  updated_at: string
}

export interface CreateZoneRequest {
  name: string
  ttl?: number
  primary_ns?: string
  admin_email?: string
  description?: string
}

export interface CreateRecordRequest {
  name: string
  type: DNSRecordType
  content: string
  ttl?: number
  priority?: number
}

// DNSProviderStatus reports whether a live nameserver push is configured.
export interface DNSProviderStatus {
  configured: boolean
  name: string
}
