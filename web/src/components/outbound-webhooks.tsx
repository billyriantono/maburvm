"use client"

import { useId, useState } from "react"
import { Loader2, Plus } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DeliveryLog } from "@/components/billing-deliveries"
import { useCan } from "@/lib/hooks/use-auth"
import type { BillingDelivery } from "@/lib/hooks/use-billing-deliveries"
import {
  ALL_WEBHOOK_EVENTS,
  type WebhookEndpoint,
  type WebhookEventGroup,
  useDeleteWebhook,
  useSaveWebhook,
  useTestWebhook,
  useWebhooks,
} from "@/lib/hooks/use-webhooks"
import { makeWebhookSecret, summarizeEvents } from "@/lib/webhooks"

const logPath = (id: string) => `/api/v1/webhooks/${id}/deliveries`

function testOutcome(d: BillingDelivery): { ok: boolean; text: string } {
  if (d.status === "delivered") return { ok: true, text: `The test event arrived (HTTP ${d.response_code}).` }
  return { ok: false, text: `The test event did not arrive: ${d.last_error || "no answer recorded"}` }
}

// The operator's own webhooks: each gets the events it chose, signed with its
// own secret, retried like the billing events, with its own log.
export function OutboundWebhooks() {
  const can = useCan()
  const canRead = can("webhook:read")
  const canEdit = can("webhook:update")
  const headingId = useId()
  const { data, isLoading, error, refetch } = useWebhooks(canRead)
  const test = useTestWebhook()
  const remove = useDeleteWebhook()
  const [editing, setEditing] = useState<WebhookEndpoint | "new" | null>(null)
  const [removing, setRemoving] = useState<WebhookEndpoint | null>(null)
  const [openLog, setOpenLog] = useState<string | null>(null)
  const [tested, setTested] = useState<Record<string, BillingDelivery>>({})

  if (!canRead) return null
  const endpoints = data?.endpoints ?? []
  const catalog = data?.catalog ?? []

  const sendTest = async (w: WebhookEndpoint) => {
    try {
      const d = await test.mutateAsync(w.id)
      setTested((t) => ({ ...t, [w.id]: d }))
    } catch (err) {
      toast.error(`Could not send the test: ${(err as Error).message}`)
    }
  }
  const confirmRemove = async () => {
    if (!removing) return
    try {
      await remove.mutateAsync(removing.id)
      toast.success(`Removed ${removing.name}`)
      setRemoving(null)
    } catch (err) {
      toast.error(`Could not remove it: ${(err as Error).message}`)
    }
  }

  return (
    <section aria-labelledby={headingId} className="mt-6">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id={headingId} className="text-lg font-semibold">Outbound webhooks</h2>
          <p className="mt-1 max-w-prose text-sm text-muted-foreground">
            Send the events you choose (servers, backups, alerts, billing, accounts) to your own systems. Each webhook has
            its own secret and log, and an event that is not accepted is tried again up to 10 times over about four hours.
          </p>
        </div>
        {canEdit && (
          <Button size="sm" onClick={() => setEditing("new")}>
            <Plus className="mr-2 h-4 w-4" aria-hidden="true" />
            Add webhook
          </Button>
        )}
      </div>

      <div className="divide-y rounded-lg border bg-card shadow-sm">
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Loading webhooks
          </p>
        ) : error ? (
          <div className="p-5">
            <p className="text-sm font-medium">Could not load the webhooks: {(error as Error).message}</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Retry</Button>
          </div>
        ) : endpoints.length === 0 ? (
          <div className="p-5">
            <p className="text-sm font-medium">No webhooks yet.</p>
            <p className="mt-1 text-sm text-muted-foreground">
              {canEdit ? "Add one to have events posted to a URL of yours." : "Staff who may change webhooks can add one."}
            </p>
          </div>
        ) : (
          endpoints.map((w) => {
            const result = tested[w.id] && testOutcome(tested[w.id])
            const logOpen = openLog === w.id
            return (
              <div key={w.id} className="p-4 sm:p-5">
                <div className="flex flex-wrap items-start gap-x-4 gap-y-2">
                  <span className="inline-flex w-14 shrink-0 items-center gap-2 text-sm font-semibold">
                    <span className={cn("h-2 w-2 rounded-full", w.enabled ? "bg-success" : "bg-muted-foreground")} aria-hidden="true" />
                    {w.enabled ? "On" : "Off"}
                  </span>
                  <div className="min-w-0 flex-1 basis-72">
                    <p className="font-medium">{w.name}</p>
                    <p className="break-all font-mono text-xs text-muted-foreground">{w.url}</p>
                    <p className="mt-1 text-sm text-muted-foreground">
                      {summarizeEvents(w.events, catalog)}. {w.has_secret ? "Signed with its secret." : "Not signed: no secret is saved."}
                    </p>
                    {result && (
                      <p role="status" className="mt-1 flex items-start gap-2 text-sm">
                        <span className={cn("mt-1.5 h-2 w-2 shrink-0 rounded-full", result.ok ? "bg-success" : "bg-destructive")} aria-hidden="true" />
                        {result.text}
                      </p>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-2 self-center">
                    {canEdit && (
                      <Button variant="outline" size="sm" disabled={test.isPending} onClick={() => sendTest(w)}>
                        {test.isPending && test.variables === w.id ? "Sending" : "Send test"}
                      </Button>
                    )}
                    <Button variant="outline" size="sm" aria-expanded={logOpen} onClick={() => setOpenLog(logOpen ? null : w.id)}>
                      {logOpen ? "Hide log" : "Show log"}
                    </Button>
                    {canEdit && (
                      <>
                        <Button variant="outline" size="sm" onClick={() => setEditing(w)}>Edit</Button>
                        <Button variant="outline" size="sm" onClick={() => setRemoving(w)}>Remove</Button>
                      </>
                    )}
                  </div>
                </div>
                {logOpen && (
                  <DeliveryLog
                    path={logPath(w.id)}
                    heading={`Events sent to ${w.name}`}
                    intro="Newest first, kept for 90 days. A test event is listed here too."
                    emptyHint="Nothing has been sent to this webhook yet."
                    className="mt-4"
                    canRetry={canEdit}
                    as="h3"
                  />
                )}
              </div>
            )
          })
        )}
      </div>

      {editing && (
        <WebhookForm
          key={editing === "new" ? "new" : editing.id}
          endpoint={editing === "new" ? null : editing}
          catalog={catalog}
          onClose={() => setEditing(null)}
        />
      )}

      <Dialog open={!!removing} onOpenChange={(open) => !open && setRemoving(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove {removing?.name}?</DialogTitle>
            <DialogDescription>
              No more events are sent to it, and its log of what was sent is removed with it. Events already queued for it are dropped.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRemoving(null)}>Cancel</Button>
            <Button variant="destructive" onClick={confirmRemove} disabled={remove.isPending}>
              {remove.isPending ? "Removing" : "Remove"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

function WebhookForm({ endpoint, catalog, onClose }: { endpoint: WebhookEndpoint | null; catalog: WebhookEventGroup[]; onClose: () => void }) {
  const save = useSaveWebhook()
  const [name, setName] = useState(endpoint?.name ?? "")
  const [url, setUrl] = useState(endpoint?.url ?? "")
  const [secret, setSecret] = useState("")
  const [showSecret, setShowSecret] = useState(false)
  const [dropSecret, setDropSecret] = useState(false)
  const [events, setEvents] = useState<string[]>(endpoint?.events ?? [])
  const [enabled, setEnabled] = useState(endpoint?.enabled ?? true)
  const [formError, setFormError] = useState("")
  const every = events.includes(ALL_WEBHOOK_EVENTS)

  const toggle = (event: string, on: boolean) =>
    setEvents((list) => (on ? [...list.filter((e) => e !== event), event] : list.filter((e) => e !== event)))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setFormError("")
    try {
      await save.mutateAsync({
        id: endpoint?.id,
        input: {
          name: name.trim(),
          url: url.trim(),
          // Blank keeps the saved secret; removing it is its own choice.
          secret: dropSecret ? "" : secret.trim() || undefined,
          events,
          enabled,
        },
      })
      toast.success(endpoint ? `Saved ${name.trim()}` : `Added ${name.trim()}`)
      onClose()
    } catch (err) {
      setFormError((err as Error).message)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-lg">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{endpoint ? `Edit ${endpoint.name}` : "Add a webhook"}</DialogTitle>
            <DialogDescription>
              Each event is posted as JSON. With a secret, X-Webhook-Signature carries the hex HMAC-SHA256 of the body.
            </DialogDescription>
          </DialogHeader>
          <div className="mt-4 max-h-[60vh] space-y-4 overflow-y-auto pr-1">
            <div>
              <Label htmlFor="webhook-name" className="mb-2 block">Name</Label>
              <Input id="webhook-name" value={name} maxLength={100} required onChange={(e) => setName(e.target.value)} placeholder="e.g. Operations chat" />
            </div>
            <div>
              <Label htmlFor="webhook-url" className="mb-2 block">URL</Label>
              <Input id="webhook-url" type="url" value={url} required onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/hooks/panel" />
            </div>
            <div>
              <Label htmlFor="webhook-secret" className="mb-2 block">Signing secret</Label>
              <div className="flex gap-2">
                <Input
                  id="webhook-secret"
                  type={showSecret ? "text" : "password"}
                  className="font-mono"
                  value={secret}
                  disabled={dropSecret}
                  aria-describedby="webhook-secret-help"
                  onChange={(e) => setSecret(e.target.value)}
                  placeholder={endpoint?.has_secret ? "Leave blank to keep the saved secret" : "Optional, at least 32 characters"}
                />
                <Button
                  type="button"
                  variant="outline"
                  disabled={dropSecret}
                  onClick={() => {
                    setSecret(makeWebhookSecret())
                    setShowSecret(true)
                  }}
                >
                  Make one
                </Button>
              </div>
              <p id="webhook-secret-help" className="mt-1 text-xs text-muted-foreground">
                Give the receiver the same secret so it can check each event came from this panel. It is stored encrypted and never shown again.
              </p>
              {endpoint?.has_secret && (
                <div className="mt-2 flex items-start gap-2">
                  <Checkbox id="webhook-drop-secret" checked={dropSecret} onCheckedChange={(v) => setDropSecret(v === true)} />
                  <Label htmlFor="webhook-drop-secret" className="font-normal">Remove the saved secret and send events unsigned</Label>
                </div>
              )}
            </div>
            <fieldset>
              <legend className="mb-2 block text-sm font-medium">Events</legend>
              <div className="flex items-start gap-2">
                <Checkbox id="webhook-every" checked={every} onCheckedChange={(v) => toggle(ALL_WEBHOOK_EVENTS, v === true)} />
                <Label htmlFor="webhook-every" className="font-normal">Every event, including ones added in later versions</Label>
              </div>
              {!every && (
                <div className="mt-3 space-y-3">
                  {catalog.map((g) => (
                    <div key={g.group}>
                      <p className="text-sm font-medium text-muted-foreground">{g.group}</p>
                      <div className="mt-1 space-y-1.5">
                        {g.events.map((ev) => {
                          const id = `webhook-event-${ev.event}`
                          return (
                            <div key={ev.event} className="flex items-start gap-2">
                              <Checkbox id={id} checked={events.includes(ev.event)} onCheckedChange={(v) => toggle(ev.event, v === true)} />
                              <Label htmlFor={id} className="font-normal">
                                {ev.label} <span className="font-mono text-xs text-muted-foreground">{ev.event}</span>
                              </Label>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </fieldset>
            <div className="flex items-start gap-2">
              <Checkbox id="webhook-enabled" checked={enabled} onCheckedChange={(v) => setEnabled(v === true)} />
              <Label htmlFor="webhook-enabled" className="font-normal">Send events to this webhook</Label>
            </div>
            {formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}
          </div>
          <DialogFooter className="mt-4">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={save.isPending || events.length === 0}>
              {save.isPending ? "Saving" : endpoint ? "Save" : "Add webhook"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
