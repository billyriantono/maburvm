"use client"

import { useState } from "react"
import Link from "next/link"
import { Check, CircleAlert, Copy, Loader2, TriangleAlert } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import {
  type SecurityCheck, type SupportGrant,
  useEndSupport, useGrantSupport, useSecurityChecks, useSupportGrants,
} from "@/lib/hooks/use-ops"

const ORDER = { fail: 0, warn: 1, ok: 2 } as const
const LABEL = { fail: "Fix now", warn: "Worth fixing", ok: "Good" } as const

function StatusMark({ status }: { status: SecurityCheck["status"] }) {
  // Icon, word and colour together: never colour alone.
  const tone =
    status === "fail" ? "text-destructive" : status === "warn" ? "text-amber-700 dark:text-amber-400" : "text-emerald-700 dark:text-emerald-400"
  const Icon = status === "fail" ? CircleAlert : status === "warn" ? TriangleAlert : Check
  return (
    <span className={`inline-flex w-32 shrink-0 items-center gap-1.5 text-sm font-semibold ${tone}`}>
      <Icon className="h-4 w-4" aria-hidden="true" />
      {LABEL[status]}
    </span>
  )
}

function timeLeft(iso: string) {
  const mins = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 60000))
  if (mins < 60) return `${mins} min left`
  const hours = Math.floor(mins / 60)
  return hours < 48 ? `${hours} h ${mins % 60} min left` : `${Math.floor(hours / 24)} days left`
}

export default function SecurityPage() {
  const confirm = useConfirm()
  const checks = useSecurityChecks()
  const grants = useSupportGrants()
  const grant = useGrantSupport()
  const end = useEndSupport()

  const [note, setNote] = useState("")
  const [hours, setHours] = useState(4)
  const [issued, setIssued] = useState<{ grant: SupportGrant; password: string }>()
  const [formError, setFormError] = useState("")

  const sorted = [...(checks.data ?? [])].sort((a, b) => ORDER[a.status] - ORDER[b.status])
  const failing = sorted.filter((c) => c.status === "fail").length
  const warning = sorted.filter((c) => c.status === "warn").length

  const handleGrant = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!note.trim()) { setFormError("Say who the access is for and why. It is kept with the account."); return }
    setFormError("")
    try {
      setIssued(await grant.mutateAsync({ hours, note: note.trim() }))
      setNote("")
    } catch (err) {
      setFormError((err as Error).message)
    }
  }

  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(`${what} copied`)
    } catch {
      toast.error("Could not copy. Select the text and copy it by hand.")
    }
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Security</h1>
        <p className="text-muted-foreground text-sm mt-1 max-w-2xl">
          What this panel looks like to someone trying to get in, checked just now against your live settings.
        </p>
      </div>

      <section aria-labelledby="checks-heading">
        <div className="flex flex-wrap items-baseline justify-between gap-3 mb-3">
          <h2 id="checks-heading" className="text-lg font-semibold">Checks</h2>
          {checks.data && (
            <p className="text-sm text-muted-foreground" role="status">
              {failing === 0 && warning === 0
                ? "Nothing needs attention."
                : [failing && `${failing} to fix now`, warning && `${warning} worth fixing`].filter(Boolean).join(", ")}
            </p>
          )}
        </div>
        <div className="rounded-lg border bg-card shadow-sm divide-y">
          {checks.isLoading ? (
            <p className="p-5 text-sm text-muted-foreground flex items-center gap-2"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Checking…</p>
          ) : checks.error ? (
            <div className="p-5">
              <p className="text-sm font-medium">The checks could not run: {(checks.error as Error).message}</p>
              <Button variant="outline" size="sm" className="mt-3" onClick={() => checks.refetch()}>Run again</Button>
            </div>
          ) : (
            sorted.map((c) => (
              <div key={c.id} className="flex flex-wrap items-start gap-x-4 gap-y-1 p-4 sm:p-5">
                <StatusMark status={c.status} />
                <div className="min-w-0 flex-1 basis-64">
                  <p className="font-medium">{c.title}</p>
                  <p className="text-sm text-muted-foreground mt-0.5 max-w-prose">{c.detail}</p>
                </div>
                {c.fix && c.status !== "ok" && (
                  <Link href={c.fix} className="text-sm font-medium underline underline-offset-4 self-center">Open the page to fix it</Link>
                )}
              </div>
            ))
          )}
        </div>
      </section>

      <section aria-labelledby="support-heading" className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] items-start">
        <div>
          <h2 id="support-heading" className="text-lg font-semibold">Temporary support access</h2>
          <p className="text-sm text-muted-foreground mt-1 mb-4 max-w-prose">
            Let someone outside your team in to investigate a problem, without sharing your own password. The account has full
            administrator rights, stops working by itself when the time is up, and can be ended early.
          </p>

          <form onSubmit={handleGrant} className="rounded-lg border bg-card p-5 shadow-sm space-y-4">
            <div>
              <label htmlFor="support-note" className="text-sm font-medium block mb-1.5">Who is it for, and why</label>
              <Input id="support-note" value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} placeholder="Example: vendor engineer, ticket 4411, VM will not boot" aria-describedby="support-note-error" aria-invalid={!!formError} />
              <p id="support-note-error" role={formError ? "alert" : undefined} className={`text-xs mt-1.5 ${formError ? "text-destructive font-medium" : "text-muted-foreground"}`}>
                {formError || "Shown beside the account for as long as it exists."}
              </p>
            </div>
            <div>
              <label htmlFor="support-hours" className="text-sm font-medium block mb-1.5">Works for</label>
              <select id="support-hours" value={hours} onChange={(e) => setHours(Number(e.target.value))} className="h-10 rounded-md border border-input bg-background px-3 text-sm cursor-pointer">
                <option value={1}>1 hour</option>
                <option value={4}>4 hours</option>
                <option value={24}>1 day</option>
                <option value={72}>3 days</option>
                <option value={168}>7 days</option>
              </select>
            </div>
            <Button type="submit" disabled={grant.isPending}>
              {grant.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              Create support account
            </Button>
          </form>

          {issued && (
            <div role="status" className="mt-4 rounded-lg border border-amber-300 bg-amber-50 p-5 text-amber-950 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-100">
              <p className="font-semibold">Copy these now. The password is not shown again.</p>
              <dl className="mt-3 space-y-2 text-sm">
                {[["Sign-in email", issued.grant.email], ["Password", issued.password]].map(([label, value]) => (
                  <div key={label} className="flex flex-wrap items-center gap-2">
                    <dt className="w-28 shrink-0 font-medium">{label}</dt>
                    <dd className="font-mono break-all flex-1 min-w-0">{value}</dd>
                    <Button type="button" variant="outline" size="sm" onClick={() => copy(value, label)}>
                      <Copy className="h-4 w-4" aria-hidden="true" />Copy
                    </Button>
                  </div>
                ))}
              </dl>
              <Button type="button" variant="ghost" size="sm" className="mt-3" onClick={() => setIssued(undefined)}>I have copied them</Button>
            </div>
          )}
        </div>

        <div>
          <h3 className="text-sm font-semibold mb-3 lg:mt-1">Open right now</h3>
          <div className="rounded-lg border bg-card shadow-sm divide-y">
            {grants.isLoading ? (
              <p className="p-5 text-sm text-muted-foreground">Loading…</p>
            ) : grants.error ? (
              <p className="p-5 text-sm">Could not list support accounts: {(grants.error as Error).message}</p>
            ) : !grants.data?.length ? (
              <p className="p-5 text-sm text-muted-foreground">No support account is open. Nobody outside your team can sign in.</p>
            ) : (
              grants.data.map((g) => (
                <div key={g.id} className="flex flex-wrap items-center justify-between gap-3 p-4">
                  <div className="min-w-0">
                    <p className="font-medium text-sm">{g.note}</p>
                    <p className="text-xs text-muted-foreground font-mono break-all mt-0.5">{g.email}</p>
                    <p className="text-xs text-muted-foreground mt-0.5">{timeLeft(g.expires_at)}, until {new Date(g.expires_at).toLocaleString()}</p>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => confirm({
                      title: "End this support access now?",
                      description: "The account is signed out everywhere and cannot sign in again.",
                      confirmLabel: "End access",
                      destructive: true,
                      action: () => end.mutateAsync(g.id),
                    })}
                  >
                    End access
                  </Button>
                </div>
              ))
            )}
          </div>
        </div>
      </section>
    </div>
  )
}
