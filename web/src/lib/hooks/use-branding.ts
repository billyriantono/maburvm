import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { Branding } from '@/lib/branding/core'

// Saving is admin-only; reading is public and comes with the page itself.
export function useSaveBranding() {
  return useMutation<Branding, Error, Branding>({
    mutationFn: async (data) => (await api.put<Branding>('/api/v1/settings/system/branding', data)).data.data,
  })
}

export interface EmailTemplate {
  subject: string
  body: string
  button: string
  /** Stops this email being sent. Only optional emails honour it. */
  off?: boolean
}

export interface EmailTemplateItem {
  key: string
  name: string
  sent_when: string
  link_meaning: string
  default: EmailTemplate
  current: EmailTemplate
  customised: boolean
  /** The operator may switch this email off. */
  optional: boolean
}

export interface EmailTemplatesResponse {
  templates: EmailTemplateItem[]
  placeholders: { token: string; meaning: string }[]
  /** Whether the logo makes it into emails, and if not, why. */
  logo: 'shown' | 'no_logo' | 'svg' | 'no_public_url'
  smtp_ready: boolean
}

export interface RenderedEmail {
  subject: string
  text: string
  html: string
}

const BASE = '/api/v1/settings/system/email-templates'

export function useEmailTemplates() {
  return useQuery<EmailTemplatesResponse>({
    queryKey: ['email-templates'],
    queryFn: async () => (await api.get<EmailTemplatesResponse>(BASE)).data.data,
  })
}

export function useSaveEmailTemplate() {
  const queryClient = useQueryClient()
  return useMutation<EmailTemplate, Error, { key: string; template: EmailTemplate }>({
    mutationFn: async ({ key, template }) => (await api.put<EmailTemplate>(`${BASE}/${key}`, template)).data.data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['email-templates'] }),
  })
}

export function useResetEmailTemplate() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (key) => {
      await api.delete(`${BASE}/${key}`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['email-templates'] }),
  })
}

// The preview is drawn by the server with the same code that sends the email,
// so what the page shows is what arrives.
export async function previewEmail(key: string, template: EmailTemplate): Promise<RenderedEmail> {
  return (await api.post<RenderedEmail>(`${BASE}/${key}/preview`, template)).data.data
}

export function useSendTestEmail() {
  return useMutation<void, Error, { key: string; to: string; template: EmailTemplate }>({
    mutationFn: async ({ key, to, template }) => {
      await api.post(`${BASE}/${key}/test`, { to, template })
    },
  })
}
