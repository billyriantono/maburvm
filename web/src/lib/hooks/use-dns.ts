import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { CreateRecordRequest, CreateZoneRequest, DNSProviderStatus, DNSRecord, DNSZone } from '@/types/dns'

// useDNSProvider reports whether a live nameserver (e.g. PowerDNS) is configured.
export function useDNSProvider() {
  return useQuery<DNSProviderStatus>({
    queryKey: ['dns', 'provider'],
    queryFn: async () => (await api.get<DNSProviderStatus>('/api/v1/dns/provider')).data.data,
  })
}

// useSyncDNSZone pushes a zone's full record set to the live nameserver.
export function useSyncDNSZone() {
  return useMutation<void, Error, string>({
    mutationFn: async (zoneId) => {
      await api.post(`/api/v1/dns/zones/${zoneId}/sync`)
    },
  })
}

export function useDNSZones() {
  return useQuery<DNSZone[]>({
    queryKey: ['dns', 'zones'],
    queryFn: async () => (await api.get<DNSZone[]>('/api/v1/dns/zones')).data.data,
  })
}

export function useCreateDNSZone() {
  const queryClient = useQueryClient()
  return useMutation<DNSZone, Error, CreateZoneRequest>({
    mutationFn: async (data) => (await api.post<DNSZone>('/api/v1/dns/zones', data)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['dns', 'zones'] }),
  })
}

export function useDeleteDNSZone() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.delete(`/api/v1/dns/zones/${id}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['dns', 'zones'] }),
  })
}

export function useDNSRecords(zoneId?: string) {
  return useQuery<DNSRecord[]>({
    queryKey: ['dns', 'records', zoneId],
    queryFn: async () => (await api.get<DNSRecord[]>(`/api/v1/dns/zones/${zoneId}/records`)).data.data,
    enabled: !!zoneId,
  })
}

export function useCreateDNSRecord(zoneId?: string) {
  const queryClient = useQueryClient()
  return useMutation<DNSRecord, Error, CreateRecordRequest>({
    mutationFn: async (data) => (await api.post<DNSRecord>(`/api/v1/dns/zones/${zoneId}/records`, data)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['dns', 'records', zoneId] }),
  })
}

export function useDeleteDNSRecord(zoneId?: string) {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (recordId) => {
      await api.delete(`/api/v1/dns/records/${recordId}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['dns', 'records', zoneId] }),
  })
}

// downloadZoneFile fetches a zone's BIND export and saves it as a file.
export async function downloadZoneFile(zoneId: string, zoneName: string) {
  const response = await api.get<string>(`/api/v1/dns/zones/${zoneId}/export`)
  const text = response.data as unknown as string
  const blob = new Blob([text], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${zoneName || 'zone'}.zone`
  a.click()
  URL.revokeObjectURL(url)
}
