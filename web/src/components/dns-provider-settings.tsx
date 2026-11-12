"use client"

import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Loader2, PlugZap, RefreshCw, Save, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useConfirm } from "@/components/confirm-provider"
import { useFlash } from "@/lib/use-feedback"

interface DNSProviderStatus {
  source: "settings" | "environment" | "none"
  url: string
  server_id: string
  has_api_key: boolean
}

const QUERY_KEY = ["settings", "dns-provider"]
const ENDPOINT = "/api/v1/settings/system/dns-provider"

// Connection to the nameserver that receives reverse DNS and zone changes.
//
// The API key is write-only. The server stores it encrypted and only ever says
// whether one is saved, so this form never has the key to show: the field is
// empty on load, and leaving it empty keeps the saved key.
export function DNSProviderSettings() {
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const saved = useFlash()
  const { data: status, isLoading, isError, error, refetch } = useQuery<DNSProviderStatus>({
    queryKey: QUERY_KEY,
    queryFn: async () => (await api.get<DNSProviderStatus>(ENDPOINT)).data.data,
  })

  const [url, setUrl] = useState("")
  const [serverId, setServerId] = useState("")
  const [apiKey, setApiKey] = useState("")
  const [formError, setFormError] = useState<string | null>(null)

  // Fill the form from what is saved. Values that come from the environment
  // are shown as a starting point but are not "saved here" until Save.
  useEffect(() => {
    if (!status) return
    setUrl(status.url)
    setServerId(status.server_id)
  }, [status])

  const body = () => ({ url: url.trim(), server_id: serverId.trim(), api_key: apiKey })

  const save = useMutation<DNSProviderStatus, Error, void>({
    mutationFn: async () => (await api.put<DNSProviderStatus>(ENDPOINT, body())).data.data,
    onSuccess: (next) => {
      queryClient.setQueryData(QUERY_KEY, next)
      setApiKey("")
      setFormError(null)
      saved.trigger()
      toast.success("DNS provider saved")
    },
    onError: (err) => setFormError(err.message),
  })

  const test = useMutation<void, Error, void>({
    mutationFn: async () => {
      await api.post(`${ENDPOINT}/test`, body())
    },
    onSuccess: () => {
      setFormError(null)
      toast.success("The nameserver answered and accepted the key")
    },
    onError: (err) => setFormError(err.message),
  })

  const remove = useMutation<DNSProviderStatus, Error, void>({
    mutationFn: async () => (await api.delete<DNSProviderStatus>(ENDPOINT)).data.data,
    onSuccess: (next) => {
      queryClient.setQueryData(QUERY_KEY, next)
      setApiKey("")
      toast.success("Saved DNS provider removed")
    },
  })

  const keySaved = status?.source === "settings" && status.has_api_key
  const busy = save.isPending || test.isPending || remove.isPending
  const canSubmit = url.trim() !== "" && !busy

  return (
    <Card className="mt-6">
      <CardHeader className="border-b">
        <CardTitle className="text-xl">DNS provider</CardTitle>
        <CardDescription>
          The PowerDNS server that receives reverse DNS (PTR) records and zone changes. Without one, records are kept in
          the panel and can only be exported.
        </CardDescription>
      </CardHeader>
      <CardContent className="p-6">
        {isLoading ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Loading the DNS provider
          </p>
        ) : isError ? (
          <div>
            <p className="text-sm font-medium text-destructive">Could not load the DNS provider</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
            <Button type="button" variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
              Retry
            </Button>
          </div>
        ) : (
          <form
            className="space-y-5"
            onSubmit={(e) => {
              e.preventDefault()
              save.mutate()
            }}
          >
            <p role="status" className="rounded-md border bg-muted px-3 py-2 text-sm">
              {status?.source === "settings" && "In use: the connection saved here."}
              {status?.source === "environment" &&
                "In use: the PDNS_API_URL and PDNS_API_KEY environment variables. Saving here takes over from them."}
              {status?.source === "none" && "Not configured. Records are not pushed to any nameserver."}
            </p>

            <div className="grid gap-5 md:grid-cols-3">
              <div className="md:col-span-2">
                <label htmlFor="dns-url" className="mb-2 block text-sm font-medium">
                  API URL
                </label>
                <Input
                  id="dns-url"
                  type="url"
                  inputMode="url"
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://pdns.example.com:8081"
                  autoComplete="off"
                  spellCheck={false}
                  required
                />
              </div>
              <div>
                <label htmlFor="dns-server-id" className="mb-2 block text-sm font-medium">
                  Server ID
                </label>
                <Input
                  id="dns-server-id"
                  value={serverId}
                  onChange={(e) => setServerId(e.target.value)}
                  placeholder="localhost"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
            </div>

            <div>
              <label htmlFor="dns-api-key" className="mb-2 block text-sm font-medium">
                API key
              </label>
              <Input
                id="dns-api-key"
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder={keySaved ? "A key is saved. Leave empty to keep it." : "Enter the API key"}
                autoComplete="new-password"
                aria-describedby="dns-api-key-help"
              />
              <p id="dns-api-key-help" className="mt-2 text-xs text-muted-foreground">
                Stored encrypted and never shown again, here or anywhere else. If you change the server address you must
                enter the key again, so a saved key is never sent to a different server.
              </p>
            </div>

            {formError && (
              <p role="alert" className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
                {formError}
              </p>
            )}

            <div className="flex flex-wrap items-center gap-3 border-t pt-5">
              <Button type="submit" disabled={!canSubmit}>
                {save.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                ) : saved.active() ? (
                  <Check className="h-4 w-4" aria-hidden="true" />
                ) : (
                  <Save className="h-4 w-4" aria-hidden="true" />
                )}
                {saved.active() ? "Saved" : "Save"}
              </Button>
              <Button type="button" variant="outline" disabled={!canSubmit} onClick={() => test.mutate()}>
                {test.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                ) : (
                  <PlugZap className="h-4 w-4" aria-hidden="true" />
                )}
                Test connection
              </Button>
              {status?.source === "settings" && (
                <Button
                  type="button"
                  variant="ghost"
                  className="ml-auto text-destructive hover:text-destructive"
                  disabled={busy}
                  onClick={async () => {
                    await confirm({
                      title: "Remove the saved DNS provider?",
                      description:
                        "The saved address and key are deleted. If PDNS environment variables are set they take over; otherwise records stop being pushed to a nameserver.",
                      confirmLabel: "Remove",
                      destructive: true,
                      action: () => remove.mutateAsync(),
                    })
                  }}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                  Remove
                </Button>
              )}
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}
