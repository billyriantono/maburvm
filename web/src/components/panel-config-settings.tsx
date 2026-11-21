"use client"

import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Loader2, RefreshCw } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useConfirm } from "@/components/confirm-provider"
import { useFlash } from "@/lib/use-feedback"
import { useCan } from "@/lib/hooks/use-auth"

// One setting as GET /api/v1/settings/system/panel reports it
// (PanelSettingStatus in internal/panel/service/panel_settings.go).
interface PanelSetting {
  key: string
  env: string
  secret: boolean
  default?: string
  /** Empty for a secret: only `set` says whether one is in force. */
  value: string
  set: boolean
  source: "setting" | "environment" | "default" | ""
  unreadable?: boolean
}

const ENDPOINT = "/api/v1/settings/system/panel"
const QUERY_KEY = ["settings", "panel"]

const FIELDS: Record<string, { label: string; placeholder?: string; help?: string }> = {
  publicUrl: {
    label: "Panel address",
    placeholder: "https://panel.example.com",
    help: "The address people open. Links in emails, the node installer and the sign-in-with-SSO return address are built from it.",
  },
  billingApiKey: { label: "API key", help: "At least 24 characters. Enter the same value in the billing system." },
  billingWebhookSecret: { label: "Signing secret", help: "At least 24 characters. Signs every request the billing system sends." },
  oidcIssuer: { label: "Issuer", placeholder: "https://sso.example.com", help: "Its /.well-known/openid-configuration must be reachable from the panel." },
  oidcClientId: { label: "Client ID" },
  oidcClientSecret: { label: "Client secret" },
  oidcButtonLabel: { label: "Button label" },
  metricsToken: { label: "Scrape token", help: "At least 16 characters. A scraper sends it as Authorization: Bearer <token> to /metrics." },
}

const GROUPS: { title: string; description: string; keys: string[]; on?: string[] }[] = [
  { title: "Address", description: "", keys: ["publicUrl"] },
  {
    title: "Billing webhook",
    description: "Lets a billing system such as WHMCS create, suspend and remove servers. Off until both are set.",
    keys: ["billingApiKey", "billingWebhookSecret"],
    on: ["billingApiKey", "billingWebhookSecret"],
  },
  {
    title: "Sign in with SSO",
    description: "A sign-in button for an OpenID Connect provider. Nobody is created automatically: a verified email must match a panel account.",
    keys: ["oidcIssuer", "oidcClientId", "oidcClientSecret", "oidcButtonLabel"],
    on: ["oidcIssuer", "oidcClientId", "oidcClientSecret"],
  },
  {
    title: "Metrics",
    description: "Prometheus metrics at /metrics on the panel's loopback port. Off until a token is set.",
    keys: ["metricsToken"],
    on: ["metricsToken"],
  },
]

// A non-secret saved here starts filled in; everything else starts empty.
function initial(s: PanelSetting): string {
  return !s.secret && s.source === "setting" ? s.value : ""
}

function sourceLine(s: PanelSetting): string {
  if (s.unreadable) return "The saved value can no longer be decrypted (the panel's key changed). Enter it again."
  switch (s.source) {
    case "setting":
      return "Saved here."
    case "environment":
      return s.secret
        ? `From ${s.env} on the panel host. A value saved here takes over.`
        : `From ${s.env} on the panel host: ${s.value}. A value saved here takes over.`
    case "default":
      return `Default: ${s.value}.`
    default:
      return `Not set (${s.env} is not set either).`
  }
}

// PanelConfigSettings edits the panel-wide settings that used to need a shell
// and a container restart. Each one still falls back to its environment
// variable, and says which one is in force.
export function PanelConfigSettings() {
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const saved = useFlash()
  // The SSO issuer decides who can sign in as anyone: changing these is the Owner's.
  const mayEdit = useCan()("staff:manage")
  const { data, isLoading, isError, error, refetch } = useQuery<PanelSetting[]>({
    queryKey: QUERY_KEY,
    queryFn: async () => (await api.get<PanelSetting[]>(ENDPOINT)).data.data,
  })
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState<string | null>(null)

  const byKey = Object.fromEntries((data ?? []).map((s) => [s.key, s]))
  useEffect(() => {
    if (data) setDraft(Object.fromEntries(data.map((s) => [s.key, initial(s)])))
  }, [data])

  const changed = (data ?? []).filter((s) => (draft[s.key] ?? "") !== initial(s) && (draft[s.key] ?? "").trim() !== "")

  const save = useMutation<PanelSetting[], Error, Record<string, string>>({
    mutationFn: async (body) => (await api.put<PanelSetting[]>(ENDPOINT, body)).data.data,
    onSuccess: (next) => {
      queryClient.setQueryData(QUERY_KEY, next)
      setFormError(null)
      saved.trigger()
      toast.success("Panel settings saved. They apply now.")
    },
    onError: (e) => setFormError(e.message),
  })

  const clear = async (s: PanelSetting) => {
    await confirm({
      title: `Remove the saved ${FIELDS[s.key]?.label.toLowerCase() ?? s.key}?`,
      description: `The value saved here is deleted and ${s.env} on the panel host, if it is set, applies again at once.`,
      confirmLabel: "Remove",
      destructive: true,
      action: () => save.mutateAsync({ [s.key]: "" }),
    })
  }

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle>Panel</CardTitle>
        <CardDescription>
          Settings that used to be environment variables on the panel host. A value saved here takes over from its
          variable and applies at once, without a restart; remove it to use the variable again.
          {!mayEdit && " Only the Owner, or staff who may assign staff roles, can change them."}
        </CardDescription>
      </CardHeader>
      <CardContent className="p-6">
        {isLoading ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Loading the panel settings
          </p>
        ) : isError ? (
          <div>
            <p className="text-sm font-medium text-destructive">Could not load the panel settings</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
            <Button type="button" variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
              Retry
            </Button>
          </div>
        ) : (
          <form
            className="space-y-8"
            onSubmit={(e) => {
              e.preventDefault()
              save.mutate(Object.fromEntries(changed.map((s) => [s.key, draft[s.key].trim()])))
            }}
          >
            {GROUPS.map((g) => {
              const on = g.on ? g.on.every((k) => byKey[k]?.set) : undefined
              return (
                <section key={g.title} className="space-y-4">
                  <div>
                    <h3 className="flex flex-wrap items-center gap-3 text-sm font-semibold">
                      {g.title}
                      {on !== undefined && (
                        <span className="inline-flex items-center gap-1.5 text-sm font-normal">
                          <span className={`h-2 w-2 rounded-full ${on ? "bg-success" : "bg-muted-foreground"}`} aria-hidden="true" />
                          {on ? "On" : "Off"}
                        </span>
                      )}
                    </h3>
                    {g.description && <p className="mt-1 text-sm text-muted-foreground">{g.description}</p>}
                    {g.title === "Sign in with SSO" && byKey.publicUrl?.value && (
                      <p className="mt-1 text-sm text-muted-foreground">
                        Register this return address with the provider:{" "}
                        <span className="font-mono break-all">{byKey.publicUrl.value.replace(/\/+$/, "")}/api/v1/auth/oidc/callback</span>
                      </p>
                    )}
                  </div>
                  {g.keys.map((k) => {
                    const s = byKey[k]
                    if (!s) return null
                    const f = FIELDS[k] ?? { label: k }
                    const id = `panel-${k}`
                    return (
                      <div key={k}>
                        <label htmlFor={id} className="mb-2 block text-sm font-medium">
                          {f.label}
                        </label>
                        <Input
                          id={id}
                          type={s.secret ? "password" : k.endsWith("Url") || k === "oidcIssuer" ? "url" : "text"}
                          value={draft[k] ?? ""}
                          onChange={(e) => setDraft((d) => ({ ...d, [k]: e.target.value }))}
                          disabled={!mayEdit}
                          placeholder={
                            s.secret
                              ? s.source === "setting" && s.set
                                ? "Saved. Leave empty to keep it."
                                : "Enter a value to save it here"
                              : f.placeholder ?? s.default ?? ""
                          }
                          autoComplete={s.secret ? "new-password" : "off"}
                          spellCheck={false}
                          className={s.secret || k.endsWith("Url") || k === "oidcIssuer" || k === "oidcClientId" ? "font-mono" : undefined}
                          aria-describedby={`${id}-help`}
                        />
                        <div id={`${id}-help`} className="mt-2 flex flex-wrap items-start justify-between gap-2 text-xs text-muted-foreground">
                          <p className="max-w-prose">
                            <span className={s.unreadable ? "text-destructive" : undefined}>{sourceLine(s)}</span>
                            {f.help ? ` ${f.help}` : ""}
                          </p>
                          {s.source === "setting" && mayEdit && (
                            <button
                              type="button"
                              className="font-medium text-foreground underline-offset-2 hover:underline disabled:opacity-50"
                              disabled={save.isPending}
                              onClick={() => clear(s)}
                            >
                              Use {s.env} instead
                            </button>
                          )}
                        </div>
                      </div>
                    )
                  })}
                </section>
              )
            })}

            {formError && (
              <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {formError}
              </p>
            )}

            {mayEdit && <div className="flex items-center gap-3 border-t pt-5">
              <Button type="submit" disabled={changed.length === 0 || save.isPending}>
                {save.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                ) : saved.active() ? (
                  <Check className="h-4 w-4" aria-hidden="true" />
                ) : null}
                {saved.active() ? "Saved" : "Save"}
              </Button>
              <p className="text-sm text-muted-foreground">
                {changed.length === 0 ? "Nothing changed." : `${changed.length} ${changed.length === 1 ? "change" : "changes"} to save.`}
              </p>
            </div>}
          </form>
        )}
      </CardContent>
    </Card>
  )
}
