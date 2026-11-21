"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Check, Loader2, RefreshCw, Save, Send } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { useFlash } from "@/lib/use-feedback"
import {
  type AlertChannelResult, type AlertKind, type AlertSettingsInput, type AlertWebhookFormat,
  useAlertSettings, useSaveAlertSettings, useTestAlert,
} from "@/lib/hooks/use-alerts"

const KINDS: { kind: AlertKind; label: string; help: string }[] = [
  { kind: "node_offline", label: "A node goes offline or stops reporting", help: "Also tells you when it is back." },
  { kind: "storage_pool", label: "A storage pool passes its alert level", help: "A warning at the level set on the pool, critical at the level below." },
  { kind: "backup_failed", label: "A backup fails", help: "Once every retry is used up." },
  { kind: "job_failed", label: "A background job fails for good", help: "Creating, reinstalling, restoring, snapshots, imports and the rest of the job queue." },
  { kind: "firewall_off", label: "A node stops filtering guest traffic", help: "VM firewall rules and spoofing protection are not enforced on that node." },
  { kind: "network_drift", label: "A node keeps losing VM network rules", help: "Its agent had to put VM firewall, spoofing or NAT rules back three times within an hour." },
  { kind: "vm_crash", label: "A VM keeps crashing", help: "A VM set to restart on crash crashed twice within an hour, or so often that its node stopped starting it." },
  { kind: "abuse", label: "A guest is attacking or under attack", help: "A guest went over a threshold of the abuse policy, or the policy acted on one. See Abuse." },
]

const ALL_ON = Object.fromEntries(KINDS.map((k) => [k.kind, true])) as Record<AlertKind, boolean>

// Where operator alerts are sent and what raises one.
//
// The webhook address and its signing secret are write-only: the server stores
// them encrypted and only says whether they are saved. Leaving a field empty
// keeps what is saved.
export function AlertSettings() {
  const confirm = useConfirm()
  const saved = useFlash()
  const { data: status, isLoading, isError, error, refetch } = useAlertSettings()
  const save = useSaveAlertSettings()
  const test = useTestAlert()

  const [emailOn, setEmailOn] = useState(false)
  const [recipients, setRecipients] = useState("")
  const [webhookOn, setWebhookOn] = useState(false)
  const [format, setFormat] = useState<AlertWebhookFormat>("generic")
  const [url, setUrl] = useState("")
  const [secret, setSecret] = useState("")
  const [kinds, setKinds] = useState<Record<AlertKind, boolean>>(ALL_ON)
  const [offlineAfter, setOfflineAfter] = useState(3)
  const [critical, setCritical] = useState(95)
  const [cooldown, setCooldown] = useState(10)
  const [formError, setFormError] = useState<string | null>(null)
  const [results, setResults] = useState<AlertChannelResult[] | null>(null)

  useEffect(() => {
    if (!status) return
    setEmailOn(status.email_enabled)
    setRecipients(status.email_recipients.join(", "))
    setWebhookOn(status.webhook_enabled)
    setFormat(status.webhook_format)
    setKinds({ ...ALL_ON, ...status.kinds })
    setOfflineAfter(status.node_offline_after)
    setCritical(status.storage_critical_percent)
    setCooldown(status.cooldown_minutes)
  }, [status])

  const body = (): AlertSettingsInput => ({
    email_enabled: emailOn,
    email_recipients: recipients.split(/[\s,;]+/).filter(Boolean),
    webhook_enabled: webhookOn,
    webhook_format: format,
    webhook_url: url.trim(),
    webhook_secret: secret,
    kinds,
    storage_critical_percent: critical,
    cooldown_minutes: cooldown,
    node_offline_after: offlineAfter,
  })

  const onSave = (input: AlertSettingsInput, message: string) =>
    save.mutate(input, {
      onSuccess: () => {
        setUrl("")
        setSecret("")
        setFormError(null)
        saved.trigger()
        toast.success(message)
      },
      onError: (err) => setFormError(err.message),
    })

  const onTest = () => {
    setResults(null)
    test.mutate(body(), {
      onSuccess: (next) => {
        setFormError(null)
        setResults(next)
      },
      onError: (err) => setFormError(err.message),
    })
  }

  if (isLoading) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        Loading the alert settings
      </p>
    )
  }
  if (isError || !status) {
    return (
      <div>
        <p className="text-sm font-medium text-destructive">Could not load the alert settings</p>
        <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
        <Button type="button" variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
          Retry
        </Button>
      </div>
    )
  }

  const busy = save.isPending || test.isPending
  const sending = [
    status.email_enabled && `by email to ${status.email_recipients.length} ${status.email_recipients.length === 1 ? "address" : "addresses"}`,
    status.webhook_enabled && status.has_webhook_url && `to a webhook at ${status.webhook_host || "the saved address"}`,
  ].filter(Boolean)

  return (
    <form
      className="space-y-6"
      onSubmit={(e) => {
        e.preventDefault()
        onSave(body(), "Alert settings saved")
      }}
    >
      <p role="status" className="rounded-md border bg-muted px-3 py-2 text-sm">
        {sending.length
          ? `Alerts are sent ${sending.join(" and ")}.`
          : "Nothing is sent yet. Alerts are still recorded and listed on this page."}
      </p>

      <fieldset className="space-y-3">
        <legend className="text-sm font-semibold">Email</legend>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" className="h-4 w-4 rounded border-input" checked={emailOn} onChange={(e) => setEmailOn(e.target.checked)} />
          Send alerts by email
        </label>
        <div>
          <label htmlFor="al-recipients" className="mb-2 block text-sm font-medium">Recipients</label>
          <Input
            id="al-recipients"
            value={recipients}
            onChange={(e) => setRecipients(e.target.value)}
            placeholder="ops@example.com, oncall@example.com"
            autoComplete="off"
            spellCheck={false}
            aria-describedby="al-recipients-help"
          />
          <p id="al-recipients-help" className="mt-2 text-xs text-muted-foreground">
            Separate addresses with commas. Each gets its own copy.
          </p>
        </div>
        {!status.smtp_configured && (
          <p className="text-sm">
            <span className="font-medium text-warning">No mail server is set up.</span>{" "}
            Emails cannot be sent until one is saved under{" "}
            <Link href="/settings/system" className="font-medium underline underline-offset-4">Settings, System, Email</Link>.
          </p>
        )}
      </fieldset>

      <fieldset className="space-y-3 border-t pt-5">
        <legend className="text-sm font-semibold">Webhook</legend>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" className="h-4 w-4 rounded border-input" checked={webhookOn} onChange={(e) => setWebhookOn(e.target.checked)} />
          Send alerts to a webhook
        </label>
        <div className="grid gap-5 md:grid-cols-3">
          <div>
            <label htmlFor="al-format" className="mb-2 block text-sm font-medium">Format</label>
            <select
              id="al-format"
              value={format}
              onChange={(e) => setFormat(e.target.value as AlertWebhookFormat)}
              className="h-10 w-full cursor-pointer rounded-md border border-input bg-background px-3 text-sm"
            >
              <option value="generic">JSON (any receiver)</option>
              <option value="slack">Slack</option>
              <option value="discord">Discord</option>
            </select>
          </div>
          <div className="md:col-span-2">
            <label htmlFor="al-url" className="mb-2 block text-sm font-medium">Address</label>
            <Input
              id="al-url"
              type="password"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder={status.has_webhook_url ? `An address at ${status.webhook_host || "an unreadable host"} is saved. Leave empty to keep it.` : "https://"}
              autoComplete="new-password"
              spellCheck={false}
              aria-describedby="al-url-help"
            />
            <p id="al-url-help" className="mt-2 text-xs text-muted-foreground">
              Stored encrypted and never shown again: a chat webhook address is a password.
            </p>
          </div>
        </div>
        {format === "generic" && (
          <div>
            <label htmlFor="al-secret" className="mb-2 block text-sm font-medium">Signing secret</label>
            <Input
              id="al-secret"
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder={status.has_webhook_secret ? "A secret is saved. Leave empty to keep it." : "Optional"}
              autoComplete="new-password"
              aria-describedby="al-secret-help"
            />
            <p id="al-secret-help" className="mt-2 text-xs text-muted-foreground">
              With a secret, each request carries an <span className="font-mono">X-Webhook-Signature</span> header: the
              HMAC-SHA256 of the body, in hex. The receiver checks it to know the alert came from this panel.
            </p>
          </div>
        )}
        {status.has_webhook_url && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() =>
              confirm({
                title: "Remove the webhook?",
                description: "The saved address and signing secret are deleted. Alerts stop going to that receiver.",
                confirmLabel: "Remove webhook",
                destructive: true,
                action: async () => onSave({ ...body(), webhook_url: "", webhook_secret: "", remove_webhook: true }, "Webhook removed"),
              })
            }
          >
            Remove webhook
          </Button>
        )}
      </fieldset>

      <fieldset className="space-y-3 border-t pt-5">
        <legend className="text-sm font-semibold">Alert when</legend>
        <ul className="space-y-3">
          {KINDS.map(({ kind, label, help }) => (
            <li key={kind}>
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 rounded border-input"
                  checked={kinds[kind]}
                  onChange={(e) => setKinds({ ...kinds, [kind]: e.target.checked })}
                />
                <span>
                  {label}
                  <span className="block text-xs text-muted-foreground">{help}</span>
                </span>
              </label>
            </li>
          ))}
        </ul>
      </fieldset>

      <fieldset className="grid gap-5 border-t pt-5 md:grid-cols-3">
        <legend className="text-sm font-semibold">Levels</legend>
        <div>
          <label htmlFor="al-offline" className="mb-2 block text-sm font-medium">Missed checks before a node is offline</label>
          <Input id="al-offline" type="number" min={1} max={30} value={offlineAfter} onChange={(e) => setOfflineAfter(Number(e.target.value))} aria-describedby="al-offline-help" />
          <p id="al-offline-help" className="mt-2 text-xs text-muted-foreground">
            Each node is checked once a minute. A lower number reacts sooner and is fooled more easily by a brief network fault.
          </p>
        </div>
        <div>
          <label htmlFor="al-critical" className="mb-2 block text-sm font-medium">Storage is critical at (% full)</label>
          <Input id="al-critical" type="number" min={50} max={100} value={critical} onChange={(e) => setCritical(Number(e.target.value))} aria-describedby="al-critical-help" />
          <p id="al-critical-help" className="mt-2 text-xs text-muted-foreground">
            The warning comes earlier, at the alert level set on each pool on the{" "}
            <Link href="/storage" className="underline underline-offset-4">Storage page</Link>.
          </p>
        </div>
        <div>
          <label htmlFor="al-cooldown" className="mb-2 block text-sm font-medium">Settle time (minutes)</label>
          <Input id="al-cooldown" type="number" min={1} max={1440} value={cooldown} onChange={(e) => setCooldown(Number(e.target.value))} aria-describedby="al-cooldown-help" />
          <p id="al-cooldown-help" className="mt-2 text-xs text-muted-foreground">
            A problem must stay away this long before its alert closes and the recovery is sent, so a node that flaps is one
            alert. The same failure is also not repeated within this time.
          </p>
        </div>
      </fieldset>

      {results && (
        <ul role="status" className="space-y-2 rounded-md border p-4">
          {results.map((r) => (
            <li key={r.channel} className="text-sm">
              <span className="inline-flex items-center gap-2 font-medium">
                <span className={cn("h-2 w-2 rounded-full", r.ok ? "bg-success" : "bg-destructive")} aria-hidden="true" />
                {r.channel === "email" ? "Email" : "Webhook"}: {r.ok ? "Test alert sent" : "Failed"}
              </span>
              <p className="ml-4 mt-0.5 break-words text-xs text-muted-foreground">{r.message}</p>
            </li>
          ))}
        </ul>
      )}

      {formError && (
        <p role="alert" className="rounded-md border border-destructive/40 px-3 py-2 text-sm font-medium text-destructive">
          {formError}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-3 border-t pt-5">
        <Button type="submit" disabled={busy}>
          {save.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : saved.active() ? (
            <Check className="h-4 w-4" aria-hidden="true" />
          ) : (
            <Save className="h-4 w-4" aria-hidden="true" />
          )}
          {saved.active() ? "Saved" : "Save"}
        </Button>
        <Button type="button" variant="outline" disabled={busy || (!emailOn && !webhookOn)} onClick={onTest}>
          {test.isPending ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : <Send className="h-4 w-4" aria-hidden="true" />}
          {test.isPending ? "Sending" : "Send test alert"}
        </Button>
        <p className="text-xs text-muted-foreground">The test uses the form as it is now, saved or not.</p>
      </div>
    </form>
  )
}
