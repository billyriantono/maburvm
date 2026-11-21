import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'

// One alert as the panel recorded it. "firing" lasts until the condition has
// stayed clear; "event" is something that happened once.
export interface Alert {
  id: string
  key: string
  kind: string
  severity: 'critical' | 'warning'
  state: 'firing' | 'resolved' | 'event'
  title: string
  detail: string
  resource_type: string
  resource_id: string
  resource_name: string
  fired_at: string
  clear_since: string | null
  resolved_at: string | null
  notified_at: string | null
  delivered: boolean
  /** How telling operators went, in words. */
  delivery: string
}

export interface AlertPage {
  alerts: Alert[]
  total: number
  page: number
  page_size: number
}

export type AlertKind = 'node_offline' | 'storage_pool' | 'backup_failed' | 'job_failed' | 'firewall_off' | 'network_drift' | 'vm_crash' | 'abuse'
export type AlertWebhookFormat = 'generic' | 'slack' | 'discord'

// What the server says about the settings. The webhook address and signing
// secret never come back, only whether they are saved.
export interface AlertSettings {
  email_enabled: boolean
  email_recipients: string[]
  smtp_configured: boolean
  webhook_enabled: boolean
  webhook_format: AlertWebhookFormat
  has_webhook_url: boolean
  webhook_host: string
  has_webhook_secret: boolean
  kinds: Record<AlertKind, boolean>
  storage_critical_percent: number
  cooldown_minutes: number
  node_offline_after: number
}

export interface AlertSettingsInput {
  email_enabled: boolean
  email_recipients: string[]
  webhook_enabled: boolean
  webhook_format: AlertWebhookFormat
  /** Empty keeps the saved address. */
  webhook_url: string
  /** Empty keeps the saved secret. */
  webhook_secret: string
  remove_webhook?: boolean
  kinds: Record<AlertKind, boolean>
  storage_critical_percent: number
  cooldown_minutes: number
  node_offline_after: number
}

export interface AlertChannelResult {
  channel: 'email' | 'webhook'
  ok: boolean
  message: string
}

const SETTINGS = '/api/v1/settings/system/alerts'
const SETTINGS_KEY = ['alerts', 'settings']

export function useAlerts(openOnly: boolean, page: number) {
  return useQuery<AlertPage>({
    queryKey: ['alerts', 'list', openOnly, page],
    queryFn: async () =>
      (await api.get<AlertPage>(`/api/v1/alerts?page=${page}&page_size=25${openOnly ? '&state=open' : ''}`)).data.data,
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
}

export function useCloseAlert() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.post(`/api/v1/alerts/${id}/close`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['alerts', 'list'] }),
  })
}

export function useAlertSettings() {
  return useQuery<AlertSettings>({
    queryKey: SETTINGS_KEY,
    queryFn: async () => (await api.get<AlertSettings>(SETTINGS)).data.data,
  })
}

export function useSaveAlertSettings() {
  const queryClient = useQueryClient()
  return useMutation<AlertSettings, Error, AlertSettingsInput>({
    mutationFn: async (input) => (await api.put<AlertSettings>(SETTINGS, input)).data.data,
    onSuccess: (next) => queryClient.setQueryData(SETTINGS_KEY, next),
  })
}

export function useTestAlert() {
  return useMutation<AlertChannelResult[], Error, AlertSettingsInput>({
    mutationFn: async (input) =>
      (await api.post<{ results: AlertChannelResult[] }>(`${SETTINGS}/test`, input)).data.data.results,
  })
}
