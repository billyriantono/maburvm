"use client"

import { FormEvent, useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { Download, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { useQueryClient } from "@tanstack/react-query"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Meter } from "@/components/ui/meter"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { can, useCurrentUser } from "@/lib/hooks/use-auth"
import {
  applyImportItems,
  useDeleteImportSource,
  useImportPreview,
  useImportSources,
  useSaveImportSource,
  useSendImportInvites,
  useTestImportSource,
  useUpdateImportPreview,
} from "@/lib/hooks/use-import"
import { actionable, billingLinksCsv, formatBillingRefs, importBatches, itemKey, parseBillingRefs } from "@/lib/import"
import type {
  ImportAction,
  ImportApplyResult,
  ImportItem,
  ImportKind,
  ImportOptions,
  ImportPlan,
  ImportSource,
  ImportSourceInput,
  SourceTestResult,
} from "@/types/import"

const KINDS: { kind: ImportKind; word: string; one: string }[] = [
  { kind: "pool", word: "IP pools", one: "IP pool" },
  { kind: "plan", word: "Plans", one: "Plan" },
  { kind: "user", word: "Customers", one: "Customer" },
  { kind: "vm", word: "VMs", one: "VM" },
]
const kindOne = (k: ImportKind) => KINDS.find((x) => x.kind === k)?.one ?? k

const ACTIONS: { action: ImportAction; word: string; filter: string; lamp: string }[] = [
  { action: "create", word: "Create", filter: "Will be created", lamp: "bg-success" },
  { action: "link", word: "Use existing", filter: "Will use what is here", lamp: "bg-success" },
  { action: "update", word: "Update", filter: "Will be updated", lamp: "bg-warning" },
  { action: "exists", word: "Imported", filter: "Already imported", lamp: "bg-muted-foreground" },
  { action: "blocked", word: "Cannot import", filter: "Cannot be imported", lamp: "bg-destructive" },
]
const actionOf = (a: ImportAction) => ACTIONS.find((x) => x.action === a) ?? ACTIONS[0]

const STATUS_WORDS: Record<ImportApplyResult["status"], string> = {
  created: "Created",
  linked: "Uses existing",
  updated: "Updated",
  skipped: "Already imported",
  failed: "Failed",
}

const selectClass = "h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
const when = (iso?: string | null) => (iso ? new Date(iso).toLocaleString() : "")

function normalized(o: ImportOptions | undefined): Required<ImportOptions> {
  return {
    default_template_id: o?.default_template_id ?? "",
    node_map: { ...(o?.node_map ?? {}) },
    plan_map: { ...(o?.plan_map ?? {}) },
    billing_refs: { ...(o?.billing_refs ?? {}) },
  }
}

function saveText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv" }))
  const a = document.createElement("a")
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

export default function ImportPage() {
  const { data: me } = useCurrentUser()
  const { data: sources, isLoading, error, refetch } = useImportSources()
  const [chosen, setChosen] = useState("")
  const [form, setForm] = useState<(ImportSourceInput & { id?: string }) | null>(null)

  const source = sources?.find((s) => s.id === chosen) ?? sources?.[0]

  if (me && !can(me, "import:manage")) {
    return (
      <div className="max-w-3xl">
        <h1 className="text-3xl font-bold tracking-tight">Import</h1>
        <p className="mt-2 text-sm text-muted-foreground">Your staff role does not include importing from another panel.</p>
      </div>
    )
  }

  return (
    <div className="max-w-6xl space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Import</h1>
        <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
          Bring a whole installation over from the Virtualizor panel you are moving from: its customers, plans, IP pools and
          VMs, each VM with its real owner and its billing link. VMs are adopted where they run; nothing on a node changes.
          You see everything first and choose what comes over.
        </p>
      </div>

      <section aria-labelledby="connect-heading" className="space-y-3">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 id="connect-heading" className="text-lg font-semibold">1. Connect</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              The source panel&apos;s admin API key and pass are on its Server Info page. They are stored encrypted and only read from.
            </p>
          </div>
          {sources && sources.length > 0 && (
            <Button variant="outline" onClick={() => setForm({ name: "", base_url: "", api_key: "", api_pass: "", tls_fingerprint: "" })}>
              Add another source
            </Button>
          )}
        </div>

        {isLoading ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> Loading
          </p>
        ) : error && !sources ? (
          <div className="rounded-lg border bg-card p-5 text-sm">
            <p className="font-medium">The sources could not be loaded: {(error as Error).message}</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !sources || sources.length === 0 ? (
          <div className="rounded-lg border bg-card p-5">
            <p className="text-sm font-medium">No source panel yet.</p>
            <p className="mt-1 text-sm text-muted-foreground">Add the panel you are moving from to see what it holds.</p>
            <Button className="mt-4" onClick={() => setForm({ name: "", base_url: "", api_key: "", api_pass: "", tls_fingerprint: "" })}>
              Add source panel
            </Button>
          </div>
        ) : (
          <>
            {sources.length > 1 && (
              <div className="max-w-sm">
                <label htmlFor="imp-source" className="mb-1 block text-xs text-muted-foreground">Source</label>
                <select id="imp-source" className={selectClass} value={source?.id} onChange={(e) => setChosen(e.target.value)}>
                  {sources.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                </select>
              </div>
            )}
            {source && (
              <SourceCard
                key={source.id}
                source={source}
                onEdit={() => setForm({ id: source.id, name: source.name, base_url: source.base_url, api_key: "", api_pass: "", tls_fingerprint: source.tls_fingerprint })}
              />
            )}
          </>
        )}
      </section>

      {source && <PreviewSection key={source.id} source={source} />}

      {form && <SourceDialog form={form} setForm={setForm} onSaved={(s) => setChosen(s.id)} />}
    </div>
  )
}

function SourceDialog({
  form,
  setForm,
  onSaved,
}: {
  form: ImportSourceInput & { id?: string }
  setForm: (f: (ImportSourceInput & { id?: string }) | null) => void
  onSaved: (s: ImportSource) => void
}) {
  const save = useSaveImportSource()
  const editing = !!form.id
  const set = (k: keyof ImportSourceInput) => (e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, [k]: e.target.value })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const s = await save.mutateAsync(form)
      toast.success(editing ? "Source saved" : "Source added")
      onSaved(s)
      setForm(null)
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && setForm(null)}>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>{editing ? "Change source panel" : "Add source panel"}</DialogTitle>
            <DialogDescription>
              Its admin API answers on port 4085 unless you changed it.
              {editing && " A new address needs the API key and pass again: the saved ones are only sent to the address they were saved for."}
            </DialogDescription>
          </DialogHeader>
          <div>
            <label htmlFor="src-name" className="mb-1 block text-sm font-medium">Name</label>
            <Input id="src-name" value={form.name} onChange={set("name")} placeholder="Old panel" maxLength={100} required />
          </div>
          <div>
            <label htmlFor="src-url" className="mb-1 block text-sm font-medium">Address</label>
            <Input id="src-url" value={form.base_url} onChange={set("base_url")} placeholder="https://master.example.com:4085" required
              autoComplete="off" className="font-mono" />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor="src-key" className="mb-1 block text-sm font-medium">API key</label>
              <Input id="src-key" value={form.api_key} onChange={set("api_key")} required={!editing} autoComplete="off" className="font-mono"
                placeholder={editing ? "Leave empty to keep" : ""} />
            </div>
            <div>
              <label htmlFor="src-pass" className="mb-1 block text-sm font-medium">API pass</label>
              <Input id="src-pass" type="password" value={form.api_pass} onChange={set("api_pass")} required={!editing} autoComplete="new-password"
                placeholder={editing ? "Leave empty to keep" : ""} />
            </div>
          </div>
          <div>
            <label htmlFor="src-fp" className="mb-1 block text-sm font-medium">Certificate fingerprint (optional)</label>
            <Input id="src-fp" value={form.tls_fingerprint} onChange={set("tls_fingerprint")} autoComplete="off" className="font-mono text-xs" />
            <p className="mt-1 text-xs text-muted-foreground">
              The SHA-256 fingerprint of a self-signed certificate to trust. Testing the connection shows the one the panel presents.
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setForm(null)}>Cancel</Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              {editing ? "Save" : "Add source"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function SourceCard({ source, onEdit }: { source: ImportSource; onEdit: () => void }) {
  const confirm = useConfirm()
  const test = useTestImportSource()
  const save = useSaveImportSource()
  const remove = useDeleteImportSource()
  const [result, setResult] = useState<SourceTestResult | null>(null)

  const runTest = async () => {
    try {
      setResult(await test.mutateAsync(source.id))
    } catch (err) {
      setResult({ ok: false, servers: 0, message: (err as Error).message })
    }
  }

  const trust = async (fp: string) => {
    try {
      await save.mutateAsync({ id: source.id, name: source.name, base_url: source.base_url, api_key: "", api_pass: "", tls_fingerprint: fp })
      toast.success("Certificate trusted")
      await runTest()
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  const forget = () =>
    confirm({
      title: `Remove the source "${source.name}"?`,
      description:
        "Its address, credentials and the record of what came from it are removed. Accounts, plans, pools and VMs already imported stay as they are.",
      confirmLabel: "Remove source",
      destructive: true,
      action: () => remove.mutateAsync(source.id),
    })

  const imported = KINDS.filter((k) => source.imported?.[k.kind]).map((k) => `${source.imported[k.kind]} ${k.word.toLowerCase()}`)

  return (
    <div className="rounded-lg border bg-card p-5 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <p className="font-medium">{source.name}</p>
          <p className="break-all font-mono text-sm text-muted-foreground">{source.base_url}</p>
          <p className="mt-1 text-sm text-muted-foreground">
            {source.snapshot_at ? `Last read ${when(source.snapshot_at)}` : "Not read yet"}
            {imported.length > 0 && ` · Imported so far: ${imported.join(", ")}`}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={runTest} disabled={test.isPending}>
            {test.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
            Test connection
          </Button>
          <Button variant="outline" size="sm" onClick={onEdit}>Change</Button>
          <Button variant="outline" size="sm" onClick={forget}>Remove</Button>
        </div>
      </div>
      {result && (
        <div className="mt-4 border-t border-hair pt-4 text-sm" role="status">
          <p className="flex items-center gap-2 font-medium">
            <span className={cn("h-2 w-2 rounded-full", result.ok ? "bg-success" : "bg-destructive")} aria-hidden="true" />
            {result.ok ? "Connected" : "Not connected"}
          </p>
          <p className="mt-1 text-muted-foreground">{result.message}</p>
          {result.fingerprint && (
            <div className="mt-3">
              <p className="text-xs text-muted-foreground">Fingerprint presented (SHA-256)</p>
              <p className="break-all font-mono text-xs">{result.fingerprint}</p>
              <Button size="sm" variant="outline" className="mt-2" onClick={() => trust(result.fingerprint!)} disabled={save.isPending}>
                Trust this certificate
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

type Run = { total: number; done: number; results: ImportApplyResult[]; running: boolean; error?: string; stopped?: boolean }

function PreviewSection({ source }: { source: ImportSource }) {
  const qc = useQueryClient()
  const confirm = useConfirm()
  const { data: plan, isLoading, error, refetch } = useImportPreview(source.id)
  const update = useUpdateImportPreview(source.id)
  const invites = useSendImportInvites(source.id)

  const [draft, setDraft] = useState<Required<ImportOptions> | null>(null)
  const [refsText, setRefsText] = useState("")
  const [excluded, setExcluded] = useState<Set<string>>(new Set())
  const [kind, setKind] = useState<ImportKind | "">("")
  const [action, setAction] = useState<ImportAction | "">("")
  const [q, setQ] = useState("")
  const [limit, setLimit] = useState(100)
  const [run, setRun] = useState<Run | null>(null)
  const [failedOnly, setFailedOnly] = useState(false)
  const stop = useRef(false)

  // The choices start from what was saved with the source.
  useEffect(() => {
    if (plan && !draft) {
      setDraft(normalized(plan.options))
      setRefsText(formatBillingRefs(plan.options.billing_refs))
    }
  }, [plan, draft])

  const parsedRefs = useMemo(() => parseBillingRefs(refsText), [refsText])
  const dirty = useMemo(() => {
    if (!plan || !draft) return false
    return JSON.stringify({ ...draft, billing_refs: parsedRefs.refs }) !== JSON.stringify(normalized(plan.options))
  }, [plan, draft, parsedRefs])

  const items = useMemo(() => plan?.items ?? [], [plan])
  const counts = useMemo(() => {
    const byKind: Record<string, number> = {}
    const byAction: Record<string, number> = {}
    for (const it of items) {
      byKind[it.kind] = (byKind[it.kind] ?? 0) + 1
      byAction[it.action] = (byAction[it.action] ?? 0) + 1
    }
    return { byKind, byAction }
  }, [items])

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return items.filter((it) => {
      if (kind && it.kind !== kind) return false
      if (action && it.action !== action) return false
      if (!needle) return true
      return [it.label, it.detail, it.key, it.vm?.owner_label, it.vm?.billing_ref, ...(it.vm?.ips ?? []), it.pool?.cidr]
        .some((v) => v?.toLowerCase().includes(needle))
    })
  }, [items, kind, action, q])

  const isChosen = (it: ImportItem) => actionable(it) && !excluded.has(itemKey(it.kind, it.key))
  const chosenCount = items.filter(isChosen).length
  const setShown = (on: boolean) =>
    setExcluded((prev) => {
      const next = new Set(prev)
      for (const it of filtered) {
        if (!actionable(it)) continue
        if (on) next.delete(itemKey(it.kind, it.key))
        else next.add(itemKey(it.kind, it.key))
      }
      return next
    })
  const toggle = (it: ImportItem, on: boolean) =>
    setExcluded((prev) => {
      const next = new Set(prev)
      if (on) next.delete(itemKey(it.kind, it.key))
      else next.add(itemKey(it.kind, it.key))
      return next
    })

  const saveChoices = async () => {
    if (!draft) return
    if (parsedRefs.errors.length) {
      toast.error("Fix the billing references first.")
      return
    }
    try {
      const p = await update.mutateAsync({ options: { ...draft, billing_refs: parsedRefs.refs } })
      setDraft(normalized(p.options))
      setRefsText(formatBillingRefs(p.options.billing_refs))
      toast.success("Choices saved; the preview is up to date")
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  const reread = async () => {
    try {
      await update.mutateAsync({ refresh: true })
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const startImport = async () => {
    if (!plan) return
    const batches = importBatches(plan.items, isChosen)
    const total = batches.reduce((n, b) => n + b.length, 0)
    const perKind = KINDS.map((k) => ({ label: k.word, value: plan.items.filter((it) => it.kind === k.kind && isChosen(it)).length }))
      .filter((d) => d.value > 0)
    const ok = await confirm({
      title: `Import ${total} ${total === 1 ? "item" : "items"}?`,
      description:
        "Customer accounts are created with no usable password, and nobody is emailed until you send the invitations. VMs are adopted where they run; nothing on the nodes changes. The owner of a chosen VM comes with it. Running it again skips what is already imported.",
      confirmLabel: "Import",
      details: perKind,
    })
    if (!ok) return
    stop.current = false
    const results: ImportApplyResult[] = []
    let done = 0
    setFailedOnly(false)
    setRun({ total, done, results, running: true })
    for (const batch of batches) {
      if (stop.current) {
        setRun({ total, done, results: [...results], running: false, stopped: true })
        break
      }
      try {
        results.push(...(await applyImportItems(source.id, batch)))
        done += batch.length
        setRun({ total, done, results: [...results], running: true })
      } catch (err) {
        setRun({ total, done, results: [...results], running: false, error: (err as Error).message })
        break
      }
    }
    setRun((r) => (r && r.running ? { ...r, running: false } : r))
    qc.invalidateQueries({ queryKey: ["import-preview", source.id] })
    qc.invalidateQueries({ queryKey: ["import-sources"] })
  }

  const sendInvites = async () => {
    if (!plan) return
    const n = plan.pending_invites
    let sent = 0
    const ok = await confirm({
      title: `Email ${n} imported ${n === 1 ? "customer" : "customers"}?`,
      description:
        "Each account the import created gets an email with a link to choose a password for this panel. The link works for 7 days. Each account is emailed once.",
      confirmLabel: "Send invitations",
      action: async () => {
        sent = await invites.mutateAsync()
      },
    })
    if (ok) toast.success(`${sent} ${sent === 1 ? "invitation" : "invitations"} queued`)
  }

  if (isLoading) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> Loading the preview
      </p>
    )
  }
  if (error && !plan) {
    return (
      <div className="rounded-lg border bg-card p-5 text-sm">
        <p className="font-medium">The preview could not be loaded: {(error as Error).message}</p>
        <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Try again</Button>
      </div>
    )
  }
  if (!plan) return null

  const busy = run?.running ?? false

  return (
    <>
      <section aria-labelledby="preview-heading" className="space-y-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 id="preview-heading" className="text-lg font-semibold">2. Choose what comes over</h2>
            <ReadStatus plan={plan} />
          </div>
          <Button variant="outline" onClick={reread} disabled={plan.reading || update.isPending || busy}>
            {plan.ready ? "Read the source again" : "Read the source"}
          </Button>
        </div>

        {plan.ready && draft && (
          <Mapping plan={plan} draft={draft} setDraft={setDraft} refsText={refsText} setRefsText={setRefsText}
            refErrors={parsedRefs.errors} dirty={dirty} saving={update.isPending} onSave={saveChoices} disabled={busy} />
        )}

        {plan.ready && (
          <>
            {plan.notes && plan.notes.length > 0 && (
              <ul className="space-y-1 rounded-lg border bg-card p-4 text-sm">
                {plan.notes.map((n) => <li key={n}>{n}</li>)}
              </ul>
            )}

            <div className="flex flex-wrap gap-2" role="group" aria-label="Show items of a kind">
              <Button type="button" size="sm" variant={kind === "" ? "default" : "outline"} aria-pressed={kind === ""} onClick={() => setKind("")}>
                All {items.length}
              </Button>
              {KINDS.map((k) => (
                <Button key={k.kind} type="button" size="sm" variant={kind === k.kind ? "default" : "outline"} aria-pressed={kind === k.kind}
                  onClick={() => setKind(k.kind)}>
                  {k.word} {counts.byKind[k.kind] ?? 0}
                </Button>
              ))}
            </div>
            <div className="flex flex-wrap items-end gap-3">
              <div className="min-w-0 flex-1 basis-56">
                <label htmlFor="imp-q" className="mb-1 block text-xs text-muted-foreground">Name, email, address or billing reference</label>
                <Input id="imp-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search" autoComplete="off" />
              </div>
              <div className="basis-56">
                <label htmlFor="imp-action" className="mb-1 block text-xs text-muted-foreground">What happens</label>
                <select id="imp-action" className={selectClass} value={action} onChange={(e) => setAction(e.target.value as ImportAction | "")}>
                  <option value="">Anything</option>
                  {ACTIONS.map((a) => <option key={a.action} value={a.action}>{a.filter} ({counts.byAction[a.action] ?? 0})</option>)}
                </select>
              </div>
              <div className="flex gap-2">
                <Button type="button" variant="outline" size="sm" onClick={() => setShown(true)} disabled={busy}>Include shown</Button>
                <Button type="button" variant="outline" size="sm" onClick={() => setShown(false)} disabled={busy}>Leave out shown</Button>
              </div>
            </div>

            <ItemsTable items={filtered.slice(0, limit)} isChosen={isChosen} toggle={toggle} disabled={busy} total={items.length} />
            {filtered.length > limit && (
              <Button variant="outline" size="sm" onClick={() => setLimit((l) => l + 100)}>
                Show more ({filtered.length - limit} not shown)
              </Button>
            )}
          </>
        )}
      </section>

      {plan.ready && (
        <section aria-labelledby="run-heading" className="space-y-3">
          <h2 id="run-heading" className="text-lg font-semibold">3. Import</h2>
          <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-card p-5 shadow-sm">
            <p className="min-w-0 flex-1 basis-64 text-sm">
              {chosenCount === 0
                ? "Nothing is chosen. Items already imported or that cannot be imported are not offered."
                : `${chosenCount} ${chosenCount === 1 ? "item is" : "items are"} chosen.`}
              {dirty && " Save your choices first: the import follows the saved ones."}
              {plan.reading && " Wait for the read to finish."}
            </p>
            {busy ? (
              <Button variant="outline" onClick={() => { stop.current = true }}>Stop after this part</Button>
            ) : (
              <Button onClick={startImport} disabled={chosenCount === 0 || dirty || plan.reading || update.isPending}>
                Import {chosenCount} {chosenCount === 1 ? "item" : "items"}
              </Button>
            )}
          </div>
          {run && <RunReport run={run} failedOnly={failedOnly} setFailedOnly={setFailedOnly} />}
        </section>
      )}

      {plan.ready && (
        <section aria-labelledby="after-heading" className="space-y-3">
          <h2 id="after-heading" className="text-lg font-semibold">4. After the import</h2>
          <div className="divide-y divide-hair rounded-lg border bg-card shadow-sm">
            <div className="flex flex-wrap items-center justify-between gap-3 p-5">
              <div className="min-w-0 flex-1 basis-64">
                <p className="font-medium">Billing links</p>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  Each imported VM with its new VM ID and its owner&apos;s account ID here, to put on the billing system&apos;s services.
                </p>
              </div>
              <Button variant="outline" onClick={() => saveText(`import-${source.name.replace(/[^\w-]+/g, "-")}-billing-links.csv`, billingLinksCsv(plan))}
                disabled={!items.some((it) => it.kind === "vm" && it.action === "exists")}>
                <Download className="h-4 w-4" aria-hidden="true" />
                Download CSV
              </Button>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 p-5">
              <div className="min-w-0 flex-1 basis-64">
                <p className="font-medium">Invitations</p>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  {plan.pending_invites > 0
                    ? `${plan.pending_invites} imported ${plan.pending_invites === 1 ? "account has" : "accounts have"} not been invited to choose a password yet.`
                    : "Every account the import created has been invited, or none was created."}{" "}
                  The email is the &quot;Account moved here&quot; template under <Link href="/settings/emails" className="underline underline-offset-4">Emails</Link>.
                </p>
              </div>
              <Button variant="outline" onClick={sendInvites} disabled={plan.pending_invites === 0 || busy}>Send invitations</Button>
            </div>
          </div>
        </section>
      )}
    </>
  )
}

function ReadStatus({ plan }: { plan: ImportPlan }) {
  if (plan.reading) {
    return (
      <p className="mt-1 flex items-center gap-2 text-sm text-muted-foreground" role="status">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        Reading the source panel and the nodes its VMs run on{plan.read_started_at ? `, since ${when(plan.read_started_at)}` : ""}. This can take a few minutes.
      </p>
    )
  }
  return (
    <div className="mt-1 text-sm">
      {plan.read_error && <p className="font-medium text-destructive">The last read failed: {plan.read_error}</p>}
      <p className="text-muted-foreground">
        {plan.ready
          ? `Planned from what was read ${when(plan.fetched_at)}. Nothing is written until you import.`
          : "The source has not been read yet."}
      </p>
    </div>
  )
}

function Mapping({
  plan, draft, setDraft, refsText, setRefsText, refErrors, dirty, saving, onSave, disabled,
}: {
  plan: ImportPlan
  draft: Required<ImportOptions>
  setDraft: (d: Required<ImportOptions>) => void
  refsText: string
  setRefsText: (t: string) => void
  refErrors: string[]
  dirty: boolean
  saving: boolean
  onSave: () => void
  disabled: boolean
}) {
  const nodes = plan.choices.nodes ?? []
  const templates = plan.choices.templates ?? []
  const panelPlans = plan.choices.plans ?? []
  const planItems = plan.items.filter((it) => it.kind === "plan")
  const setMap = (field: "node_map" | "plan_map", id: string, value: string) => {
    const next = { ...draft[field] }
    if (value) next[id] = value
    else delete next[id]
    setDraft({ ...draft, [field]: next })
  }

  return (
    <div className="space-y-4 rounded-lg border bg-card p-5 shadow-sm">
      <div className="grid gap-6 lg:grid-cols-2">
        <div>
          <h3 className="font-medium">Servers and nodes</h3>
          <p className="mt-0.5 text-sm text-muted-foreground">Where each source server&apos;s VMs run here. Servers match a node by address, then by name.</p>
          <div className="mt-3 space-y-3">
            {(plan.servers ?? []).map((s) => (
              <div key={s.id}>
                <label htmlFor={`srv-${s.id}`} className="block text-sm">
                  {s.name || `Server ${s.id}`} <span className="font-mono text-xs text-muted-foreground">{s.ip}</span>
                  <span className="text-xs text-muted-foreground"> · {s.vms} {s.vms === 1 ? "VM" : "VMs"}</span>
                </label>
                <select id={`srv-${s.id}`} className={cn(selectClass, "mt-1")} value={draft.node_map[s.id] ?? ""} disabled={disabled}
                  onChange={(e) => setMap("node_map", s.id, e.target.value)}>
                  <option value="">{s.matched === "auto" ? `Match automatically (${s.node_name})` : "Match automatically (no match)"}</option>
                  {nodes.map((n) => <option key={n.id} value={n.id}>{n.label}</option>)}
                  <option value="-">Leave its VMs out</option>
                </select>
                {s.scan_error && <p className="mt-1 text-xs text-destructive">The node could not be read: {s.scan_error}</p>}
              </div>
            ))}
          </div>
        </div>
        <div className="space-y-6">
          <div>
            <h3 className="font-medium">Template for unmatched systems</h3>
            <p className="mt-0.5 text-sm text-muted-foreground">
              Each VM is recorded with the template its system matches by name. VMs that match none get this one; nothing is installed from it.
            </p>
            <select aria-label="Template for unmatched systems" className={cn(selectClass, "mt-2")} value={draft.default_template_id} disabled={disabled}
              onChange={(e) => setDraft({ ...draft, default_template_id: e.target.value })}>
              <option value="">None chosen</option>
              {templates.map((t) => <option key={t.id} value={t.id}>{t.label}</option>)}
            </select>
          </div>
          {planItems.length > 0 && (
            <div>
              <h3 className="font-medium">Plans</h3>
              <p className="mt-0.5 text-sm text-muted-foreground">A source plan uses the panel plan of the same name, or is created. Choose another here.</p>
              <div className="mt-2 space-y-2">
                {planItems.map((it) => (
                  <div key={it.key} className="grid items-center gap-1 sm:grid-cols-2 sm:gap-3">
                    <label htmlFor={`plan-${it.key}`} className="text-sm">{it.label}</label>
                    <select id={`plan-${it.key}`} className={selectClass} value={draft.plan_map[it.key] ?? ""} disabled={disabled || it.action === "exists"}
                      onChange={(e) => setMap("plan_map", it.key, e.target.value)}>
                      <option value="">Automatic</option>
                      <option value="new">Create a new plan</option>
                      {panelPlans.map((p) => <option key={p.id} value={p.id}>Use {p.label}</option>)}
                    </select>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
      <div>
        <h3 className="font-medium"><label htmlFor="imp-refs">Billing references</label></h3>
        <p className="mt-0.5 text-sm text-muted-foreground">
          One line per VM: its ID on the source, then its billing service. A bare number is a WHMCS service ID. The reference is kept on the VM so
          the billing module can only act on the VM its service pays for.
        </p>
        <textarea id="imp-refs" value={refsText} onChange={(e) => setRefsText(e.target.value)} rows={4} disabled={disabled} spellCheck={false}
          placeholder={"37,1234\n38,1235"}
          className="mt-2 w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" />
        {refErrors.length > 0 && (
          <ul className="mt-1 space-y-0.5 text-xs text-destructive">{refErrors.slice(0, 5).map((e) => <li key={e}>{e}</li>)}</ul>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={onSave} disabled={!dirty || saving || disabled || refErrors.length > 0} variant={dirty ? "default" : "outline"}>
          {saving && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          Save choices
        </Button>
        {dirty && <p className="text-sm text-muted-foreground">The preview below shows the saved choices until you save these.</p>}
      </div>
    </div>
  )
}

function ItemDetail({ it }: { it: ImportItem }) {
  if (it.vm) {
    const v = it.vm
    return (
      <div className="space-y-0.5">
        <p>Owner {v.owner_label || `#${v.owner_key}`}</p>
        <p className="text-muted-foreground">
          {v.plan_label ? `Plan ${v.plan_label}` : "No plan"}{v.node_name ? ` · node ${v.node_name}` : ""}{v.template_label ? ` · ${v.template_label}` : ""}
        </p>
        {v.ips && v.ips.length > 0 && <p className="break-all font-mono text-xs">{v.ips.join(", ")}</p>}
        {v.billing_ref && <p className="text-xs">Billing <span className="font-mono">{v.billing_ref}</span></p>}
        {v.suspended && <p className="text-xs">Suspended</p>}
      </div>
    )
  }
  if (it.pool) {
    const p = it.pool
    return (
      <div className="space-y-0.5">
        <p className="font-mono text-xs">{p.cidr}{p.gateway ? ` via ${p.gateway}` : ""}</p>
        <p className="text-muted-foreground tabular-nums">{p.in_use} of {p.total} addresses in use</p>
        {p.dns && p.dns.length > 0 && <p className="font-mono text-xs text-muted-foreground">DNS {p.dns.join(", ")}</p>}
      </div>
    )
  }
  return <p className="text-muted-foreground">{it.detail}</p>
}

function ItemsTable({
  items, isChosen, toggle, disabled, total,
}: {
  items: ImportItem[]
  isChosen: (it: ImportItem) => boolean
  toggle: (it: ImportItem, on: boolean) => void
  disabled: boolean
  total: number
}) {
  if (items.length === 0) {
    return (
      <div className="rounded-lg border bg-card p-5 text-sm">
        <p className="font-medium">{total === 0 ? "The source holds nothing to import." : "Nothing matches these filters."}</p>
        {total > 0 && <p className="mt-1 text-muted-foreground">Choose All, or clear the search.</p>}
      </div>
    )
  }
  return (
    <div className="overflow-x-auto rounded-lg border bg-card shadow-sm">
      <table className="w-full min-w-[900px] text-sm">
        <thead className="border-b text-left text-xs text-muted-foreground">
          <tr>
            <th scope="col" className="w-10 px-4 py-2 font-medium"><span className="sr-only">Include</span></th>
            <th scope="col" className="px-4 py-2 font-medium">Item</th>
            <th scope="col" className="px-4 py-2 font-medium">What happens</th>
            <th scope="col" className="px-4 py-2 font-medium">Details</th>
            <th scope="col" className="px-4 py-2 font-medium">Why</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-hair">
          {items.map((it) => {
            const a = actionOf(it.action)
            const word = it.kind === "vm" && it.action === "create" ? "Adopt" : a.word
            const id = `inc-${it.kind}-${it.key}`
            return (
              <tr key={itemKey(it.kind, it.key)} className="align-top">
                <td className="px-4 py-3">
                  <Checkbox id={id} checked={isChosen(it)} disabled={disabled || !actionable(it)} onCheckedChange={(v) => toggle(it, v === true)}
                    aria-label={`Include ${it.label}`} />
                </td>
                <td className="px-4 py-3">
                  <label htmlFor={id} className="font-medium break-all">{it.label}</label>
                  <p className="text-xs text-muted-foreground">{kindOne(it.kind)} #{it.key}</p>
                </td>
                <td className="px-4 py-3">
                  <span className="inline-flex items-center gap-2 font-medium">
                    <span className={cn("h-2 w-2 rounded-full", a.lamp)} aria-hidden="true" />
                    {word}
                  </span>
                  {it.target_label && it.action !== "create" && <p className="text-xs text-muted-foreground break-all">{it.target_label}</p>}
                </td>
                <td className="max-w-xs px-4 py-3"><ItemDetail it={it} /></td>
                <td className="max-w-sm px-4 py-3">
                  {(it.conflicts ?? []).map((c) => <p key={c} className="text-destructive">{c}</p>)}
                  {(it.notes ?? []).map((n) => <p key={n} className="text-muted-foreground">{n}</p>)}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function RunReport({ run, failedOnly, setFailedOnly }: { run: Run; failedOnly: boolean; setFailedOnly: (v: boolean) => void }) {
  const byStatus = run.results.reduce<Record<string, number>>((m, r) => ({ ...m, [r.status]: (m[r.status] ?? 0) + 1 }), {})
  const rows = [...run.results]
    .filter((r) => !failedOnly || r.status === "failed")
    .sort((a, b) => Number(b.status === "failed") - Number(a.status === "failed"))
  return (
    <div className="space-y-3 rounded-lg border bg-card p-5 shadow-sm">
      <div role="status">
        <p className="text-sm font-medium tabular-nums">
          {run.running ? `Importing: ${run.done} of ${run.total}` : run.stopped ? `Stopped after ${run.done} of ${run.total}` : run.error ? `Stopped after ${run.done} of ${run.total}` : `Done: ${run.done} of ${run.total}`}
        </p>
        <Meter percent={run.total ? (run.done / run.total) * 100 : 0} />
        {run.error && (
          <p className="mt-2 text-sm text-destructive">
            The import stopped: {run.error} Import again to carry on; what is already imported is skipped.
          </p>
        )}
      </div>
      {run.results.length > 0 && (
        <>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm tabular-nums">
            {(Object.keys(STATUS_WORDS) as ImportApplyResult["status"][]).filter((s) => byStatus[s]).map((s) => (
              <span key={s} className={cn(s === "failed" && "font-medium text-destructive")}>{STATUS_WORDS[s]} {byStatus[s]}</span>
            ))}
            {byStatus.failed > 0 && (
              <label className="ml-auto inline-flex items-center gap-2">
                <Checkbox checked={failedOnly} onCheckedChange={(v) => setFailedOnly(v === true)} />
                Only failed
              </label>
            )}
          </div>
          <div className="max-h-96 overflow-auto rounded-md border">
            <table className="w-full min-w-[640px] text-sm">
              <thead className="sticky top-0 border-b bg-card text-left text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium">Item</th>
                  <th scope="col" className="px-3 py-2 font-medium">Result</th>
                  <th scope="col" className="px-3 py-2 font-medium">Message</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-hair">
                {rows.map((r, i) => (
                  <tr key={`${itemKey(r.kind, r.key)}-${i}`} className="align-top">
                    <td className="px-3 py-2">
                      {r.kind === "vm" && r.target_id ? (
                        <Link href={`/vms/${r.target_id}`} className="font-medium underline-offset-4 hover:underline">{r.label || r.key}</Link>
                      ) : (
                        <span className="font-medium">{r.label || r.key}</span>
                      )}
                      <p className="text-xs text-muted-foreground">{kindOne(r.kind)} #{r.key}</p>
                    </td>
                    <td className={cn("px-3 py-2", r.status === "failed" && "font-medium text-destructive")}>{STATUS_WORDS[r.status]}</td>
                    <td className="px-3 py-2 text-muted-foreground">{r.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  )
}
