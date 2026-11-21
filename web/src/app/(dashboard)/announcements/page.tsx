"use client"

import { FormEvent, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { useCan } from "@/lib/hooks/use-auth"
import {
  type Announcement, type AnnouncementInput, type AnnouncementPreview, type AudienceType,
  useAnnouncements, useDeleteAnnouncement, useEmailAnnouncement, usePreviewAnnouncement, useSaveAnnouncement,
} from "@/lib/hooks/use-admin-extras"
import { useNodes } from "@/lib/hooks/use-nodes"
import { usePlans } from "@/lib/hooks/use-plans"
import { useRegions } from "@/lib/hooks/use-regions"

// Notices for customers: a maintenance window on one node, an incident in a
// region, news for everyone. Shown in the customer area while they run, and
// emailed through the email queue when asked.

const AUDIENCES: { id: AudienceType; label: string }[] = [
  { id: "all", label: "Every customer" },
  { id: "nodes", label: "Customers with a VM on chosen nodes" },
  { id: "regions", label: "Customers with a VM in chosen regions" },
  { id: "plans", label: "Customers with a VM on chosen plans" },
  { id: "vms", label: "The owners of chosen VMs" },
]

const fieldClass =
  "w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"

const empty: AnnouncementInput = { title: "", body: "", kind: "service", audience: { type: "all" }, starts_at: null, ends_at: null }

// <input type="datetime-local"> speaks local time without a zone.
const toLocal = (iso?: string | null) => {
  if (!iso) return ""
  const d = new Date(iso)
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
const fromLocal = (v: string) => (v ? new Date(v).toISOString() : null)
const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })

function showing(a: Announcement) {
  const now = Date.now()
  if (new Date(a.starts_at).getTime() > now) return "Starts " + when(a.starts_at)
  if (a.ends_at && new Date(a.ends_at).getTime() <= now) return "Ended " + when(a.ends_at)
  return a.ends_at ? "Showing until " + when(a.ends_at) : "Showing"
}

function Editor({ initial, id, onDone }: { initial: AnnouncementInput; id?: string; onDone: () => void }) {
  const [f, setF] = useState<AnnouncementInput>(initial)
  const [vmText, setVmText] = useState(initial.audience.type === "vms" ? (initial.audience.ids ?? []).join("\n") : "")
  const [preview, setPreview] = useState<AnnouncementPreview | null>(null)
  const save = useSaveAnnouncement()
  const previewIt = usePreviewAnnouncement()
  const { data: nodes } = useNodes(f.audience.type === "nodes")
  const { data: regions } = useRegions()
  const { data: plans } = usePlans()

  const input = (): AnnouncementInput => ({
    ...f,
    audience: f.audience.type === "vms"
      ? { type: "vms", ids: vmText.split(/[\s,]+/).filter(Boolean) }
      : f.audience.type === "all" ? { type: "all" } : f.audience,
  })
  const choices: { id: string; name: string }[] =
    f.audience.type === "nodes" ? (nodes ?? []).map((n) => ({ id: n.id, name: n.name }))
      : f.audience.type === "regions" ? (regions ?? []).map((r) => ({ id: r.id, name: r.name }))
        : f.audience.type === "plans" ? (plans ?? []).map((p) => ({ id: p.id, name: p.name })) : []
  const ids = f.audience.ids ?? []
  const toggle = (x: string) => setF({ ...f, audience: { ...f.audience, ids: ids.includes(x) ? ids.filter((i) => i !== x) : [...ids, x] } })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await save.mutateAsync({ id, input: input() })
      toast.success(id ? "Announcement saved" : "Announcement published")
      onDone()
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }
  const look = async () => {
    try {
      setPreview(await previewIt.mutateAsync(input()))
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4 rounded-lg border bg-card p-5">
      <h2 className="text-base font-semibold">{id ? "Change the announcement" : "New announcement"}</h2>
      <div>
        <label htmlFor="ann-title" className="mb-1 block text-sm font-medium">Title</label>
        <Input id="ann-title" value={f.title} maxLength={150} onChange={(e) => setF({ ...f, title: e.target.value })} placeholder="Maintenance on Sunday night" />
      </div>
      <div>
        <label htmlFor="ann-body" className="mb-1 block text-sm font-medium">Message</label>
        <textarea id="ann-body" rows={6} maxLength={4000} className={fieldClass} value={f.body} onChange={(e) => setF({ ...f, body: e.target.value })} />
        <p className="mt-1 text-xs text-muted-foreground">Plain text; a blank line starts a new paragraph. In the email, {"{{name}}"} and {"{{greeting}}"} become the customer&apos;s name.</p>
      </div>
      <fieldset>
        <legend className="mb-1 text-sm font-medium">Kind</legend>
        <div className="flex flex-wrap gap-4 text-sm">
          <label className="flex items-start gap-2">
            <input type="radio" name="kind" checked={f.kind === "service"} onChange={() => setF({ ...f, kind: "service" })} className="mt-1" />
            <span>Service notice<span className="block text-xs text-muted-foreground">Maintenance, incidents. Reaches everyone in the audience.</span></span>
          </label>
          <label className="flex items-start gap-2">
            <input type="radio" name="kind" checked={f.kind === "marketing"} onChange={() => setF({ ...f, kind: "marketing" })} className="mt-1" />
            <span>News or offer<span className="block text-xs text-muted-foreground">Skips customers who unsubscribed; the email carries an unsubscribe link.</span></span>
          </label>
        </div>
      </fieldset>
      <div>
        <label htmlFor="ann-audience" className="mb-1 block text-sm font-medium">Who</label>
        <select id="ann-audience" className={`${fieldClass} h-10`} value={f.audience.type}
          onChange={(e) => setF({ ...f, audience: { type: e.target.value as AudienceType, ids: [] } })}>
          {AUDIENCES.map((a) => <option key={a.id} value={a.id}>{a.label}</option>)}
        </select>
        {f.audience.type === "vms" ? (
          <div className="mt-2">
            <label htmlFor="ann-vms" className="mb-1 block text-xs text-muted-foreground">VM ids, one per line (each VM&apos;s page shows its id)</label>
            <textarea id="ann-vms" rows={3} className={`${fieldClass} font-mono`} value={vmText} onChange={(e) => setVmText(e.target.value)} />
          </div>
        ) : f.audience.type !== "all" && (
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-2 text-sm" role="group" aria-label="Choose">
            {choices.length === 0 && <p className="text-muted-foreground">None to choose from.</p>}
            {choices.map((c) => (
              <label key={c.id} className="flex items-center gap-2">
                <input type="checkbox" checked={ids.includes(c.id)} onChange={() => toggle(c.id)} />{c.name}
              </label>
            ))}
          </div>
        )}
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label htmlFor="ann-start" className="mb-1 block text-sm font-medium">Show from</label>
          <Input id="ann-start" type="datetime-local" value={toLocal(f.starts_at)} onChange={(e) => setF({ ...f, starts_at: fromLocal(e.target.value) })} />
          <p className="mt-1 text-xs text-muted-foreground">Empty: now.</p>
        </div>
        <div>
          <label htmlFor="ann-end" className="mb-1 block text-sm font-medium">Show until</label>
          <Input id="ann-end" type="datetime-local" value={toLocal(f.ends_at)} onChange={(e) => setF({ ...f, ends_at: fromLocal(e.target.value) })} />
          <p className="mt-1 text-xs text-muted-foreground">Empty: until it is taken down.</p>
        </div>
      </div>
      {preview && (
        <div className="space-y-2 rounded-md border bg-background p-4 text-sm" role="status">
          <p className="font-medium">
            An email would reach {preview.recipients} {preview.recipients === 1 ? "customer" : "customers"}
            {preview.sample.length > 0 && <span className="font-normal text-muted-foreground">, such as {preview.sample.slice(0, 3).join(", ")}</span>}.
          </p>
          <p className="text-xs text-muted-foreground">Subject</p>
          <p className="font-medium">{preview.email.subject}</p>
          <pre className="max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded border p-3 font-sans text-sm">{preview.email.text}</pre>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={save.isPending || !f.title.trim() || !f.body.trim()}>
          {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          {id ? "Save" : "Publish"}
        </Button>
        <Button type="button" variant="outline" disabled={previewIt.isPending || !f.title.trim() || !f.body.trim()} onClick={look}>
          {previewIt.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}Preview who and the email
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>Cancel</Button>
      </div>
    </form>
  )
}

function EmailButton({ a }: { a: Announcement }) {
  const confirm = useConfirm()
  const email = useEmailAnnouncement()
  const [rate, setRate] = useState("60")
  const send = async () => {
    const perMinute = Math.min(Math.max(Number(rate) || 60, 1), 600)
    const ok = await confirm({
      title: a.emailed_at ? `Email "${a.title}" to whoever has not had it?` : `Email "${a.title}"?`,
      description: `One email to each customer in its audience who has not had it yet, ${perMinute} a minute. Nobody gets it twice.`,
      confirmLabel: "Send",
    })
    if (!ok) return
    try {
      const out = await email.mutateAsync({ id: a.id, per_minute: perMinute })
      toast.success(out.queued ? `${out.queued} emails queued, ${out.per_minute} a minute. Follow them in Logs, Emails.` : "Everyone in the audience has had it already.")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }
  return (
    <span className="inline-flex items-center gap-2">
      <label htmlFor={`rate-${a.id}`} className="sr-only">Emails a minute</label>
      <Input id={`rate-${a.id}`} inputMode="numeric" className="h-8 w-16" value={rate} onChange={(e) => setRate(e.target.value)} />
      <span className="text-xs text-muted-foreground">a minute</span>
      <Button size="sm" variant="outline" disabled={email.isPending} onClick={send}>{a.emailed_at ? "Email the rest" : "Email it"}</Button>
    </span>
  )
}

export default function AnnouncementsPage() {
  const allow = useCan()
  const mayWrite = allow("announcement:update")
  const confirm = useConfirm()
  const { data, isLoading, error, refetch } = useAnnouncements()
  const remove = useDeleteAnnouncement()
  const [editing, setEditing] = useState<{ id?: string; input: AnnouncementInput } | null>(null)

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Announcements</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Shown at the top of the customer area to the customers they are for, while they run. Email one when it cannot wait for
            them to sign in.
          </p>
        </div>
        {mayWrite && !editing && <Button onClick={() => setEditing({ input: empty })}>New announcement</Button>}
      </div>

      {editing && <Editor key={editing.id ?? "new"} id={editing.id} initial={editing.input} onDone={() => setEditing(null)} />}

      <section aria-label="Announcements" className="divide-y rounded-lg border bg-card">
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading announcements</p>
        ) : error ? (
          <div className="p-5" role="alert">
            <p className="text-sm font-medium">The announcements could not be loaded: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !data?.length ? (
          <p className="p-5 text-sm text-muted-foreground">No announcements yet.</p>
        ) : (
          data.map((a) => (
            <article key={a.id} className="space-y-2 p-5">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <h2 className="font-semibold">{a.title}</h2>
                <p className="text-xs text-muted-foreground">{showing(a)}</p>
              </div>
              <p className="whitespace-pre-wrap break-words text-sm">{a.body}</p>
              <p className="text-xs text-muted-foreground">
                {a.kind === "marketing" ? "News or offer" : "Service notice"} for {AUDIENCES.find((x) => x.id === a.audience.type)?.label.toLowerCase()}
                {a.audience.ids?.length ? ` (${a.audience.ids.length})` : ""}.{" "}
                {a.emailed_at ? `${a.email_queued} emails queued, last ${when(a.emailed_at)}.` : "Not emailed."}
              </p>
              {mayWrite && (
                <div className="flex flex-wrap items-center gap-2 pt-1">
                  <EmailButton a={a} />
                  <Button size="sm" variant="ghost" onClick={() => setEditing({
                    id: a.id, input: { title: a.title, body: a.body, kind: a.kind, audience: a.audience, starts_at: a.starts_at, ends_at: a.ends_at },
                  })}>Edit</Button>
                  <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={async () => {
                    if (await confirm({ title: `Take down "${a.title}"?`, description: "Customers stop seeing it. Emails already sent are not recalled.", confirmLabel: "Take down", destructive: true })) {
                      remove.mutate(a.id, { onError: (err) => toast.error(err.message) })
                    }
                  }}>Take down</Button>
                </div>
              )}
            </article>
          ))
        )}
      </section>
    </div>
  )
}
