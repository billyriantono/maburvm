"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { Loader2, RotateCcw, Send } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { useBranding } from "@/lib/branding/provider"
import {
  type EmailTemplate, type RenderedEmail, previewEmail,
  useEmailTemplates, useResetEmailTemplate, useSaveEmailTemplate, useSendTestEmail,
} from "@/lib/hooks/use-branding"

const same = (a: EmailTemplate, b: EmailTemplate) => a.subject === b.subject && a.body === b.body && a.button === b.button

const fieldClass =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"

const LOGO_NOTE: Record<string, string> = {
  no_logo: "You have not added a logo, so emails open with your panel name in text.",
  svg: "Your logo is an SVG, which most mail apps will not draw, so emails open with your panel name in text. Upload a PNG in the brand studio to show it.",
  no_public_url: "Emails open with your panel name in text, because the panel does not know its public address to serve the logo from. Set PANEL_PUBLIC_URL on the panel to show it.",
}

export default function EmailsPage() {
  const confirm = useConfirm()
  const { branding } = useBranding()
  const { data, isLoading, error, refetch } = useEmailTemplates()
  const save = useSaveEmailTemplate()
  const reset = useResetEmailTemplate()
  const sendTest = useSendTestEmail()

  const [activeKey, setActiveKey] = useState<string>()
  // One draft per email, so switching between them never loses typing.
  const [drafts, setDrafts] = useState<Record<string, EmailTemplate>>({})
  const [preview, setPreview] = useState<RenderedEmail>()
  const [previewError, setPreviewError] = useState("")
  const [view, setView] = useState<"html" | "text">("html")
  const [testTo, setTestTo] = useState("")
  const lastField = useRef<"subject" | "body">("body")
  const subjectRef = useRef<HTMLInputElement>(null)
  const bodyRef = useRef<HTMLTextAreaElement>(null)

  const templates = useMemo(() => data?.templates ?? [], [data])
  const active = templates.find((t) => t.key === activeKey) ?? templates[0]
  const draft = active ? drafts[active.key] ?? active.current : undefined
  const dirty = !!active && !!draft && !same(draft, active.current)
  const dirtyKeys = useMemo(
    () => templates.filter((t) => drafts[t.key] && !same(drafts[t.key], t.current)).map((t) => t.key),
    [templates, drafts],
  )

  const setDraft = (patch: Partial<EmailTemplate>) => {
    if (!active || !draft) return
    setDrafts((d) => ({ ...d, [active.key]: { ...draft, ...patch } }))
  }

  // Ask the server to draw the email a moment after typing stops.
  useEffect(() => {
    if (!active || !draft) return
    if (!draft.subject.trim() || !draft.body.trim()) { setPreviewError("Add a subject and a message to see the email."); return }
    let live = true
    const timer = setTimeout(() => {
      previewEmail(active.key, draft)
        .then((r) => { if (live) { setPreview(r); setPreviewError("") } })
        .catch((e) => { if (live) setPreviewError((e as Error).message) })
    }, 350)
    return () => { live = false; clearTimeout(timer) }
    // branding is a dependency on purpose: a new logo or colour changes the email.
  }, [active?.key, draft?.subject, draft?.body, draft?.button, branding]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (dirtyKeys.length === 0) return
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault() }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirtyKeys.length])

  // Drop a placeholder where the cursor was, in whichever field was last used.
  const insert = (token: string) => {
    if (!draft) return
    const field = lastField.current
    const el = field === "subject" ? subjectRef.current : bodyRef.current
    const value = draft[field]
    const start = el?.selectionStart ?? value.length
    const end = el?.selectionEnd ?? value.length
    setDraft({ [field]: value.slice(0, start) + token + value.slice(end) })
    requestAnimationFrame(() => { el?.focus(); el?.setSelectionRange(start + token.length, start + token.length) })
  }

  const handleSave = async () => {
    if (!active || !draft) return
    try {
      await save.mutateAsync({ key: active.key, template: draft })
      setDrafts((d) => { const next = { ...d }; delete next[active.key]; return next })
      toast.success(`${active.name} email saved`)
    } catch (e) {
      toast.error(`Could not save the email: ${(e as Error).message}`)
    }
  }

  const handleReset = async () => {
    if (!active) return
    const ok = await confirm({
      title: `Put back the original ${active.name.toLowerCase()} email?`,
      description: "Your wording for this email is deleted and the built-in text is used again.",
      confirmLabel: "Use the original",
      destructive: true,
    })
    if (!ok) return
    try {
      await reset.mutateAsync(active.key)
      setDrafts((d) => { const next = { ...d }; delete next[active.key]; return next })
      toast.success("Original wording restored")
    } catch (e) {
      toast.error(`Could not reset the email: ${(e as Error).message}`)
    }
  }

  const handleTest = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!active || !draft) return
    try {
      await sendTest.mutateAsync({ key: active.key, to: testTo.trim(), template: draft })
      toast.success(`Test sent to ${testTo.trim()}`)
    } catch (err) {
      toast.error(`The test was not sent: ${(err as Error).message}`)
    }
  }

  if (isLoading) {
    return <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading emails…</div>
  }
  if (error || !active || !draft) {
    return (
      <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6">
        <p className="font-semibold">The emails could not be loaded</p>
        <p className="text-sm text-muted-foreground mt-1">{(error as Error | null)?.message ?? "The panel returned no emails."}</p>
        <Button variant="outline" size="sm" className="mt-4" onClick={() => refetch()}>Try again</Button>
      </div>
    )
  }

  const usesUnknown = [...`${draft.subject} ${draft.body}`.matchAll(/\{\{[^}]*\}\}/g)]
    .map((m) => m[0])
    .filter((t) => !data!.placeholders.some((p) => p.token === t))

  return (
    <div className="pb-24">
      <div className="mb-6">
        <h1 className="text-3xl font-bold tracking-tight">Emails</h1>
        <p className="text-muted-foreground text-sm mt-1 max-w-2xl">
          The messages the panel sends to your customers, in your words. Each one goes out with your name and colour from the{" "}
          <Link href="/settings/branding" className="underline underline-offset-2 text-foreground">brand studio</Link>, and a plain-text copy for mail apps that do not show formatting.
        </p>
      </div>

      {!data!.smtp_ready && (
        <div role="status" className="mb-6 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
          No email can go out yet: the panel has no mail server. Add one under{" "}
          <Link href="/settings/system" className="underline underline-offset-2 font-medium">System settings, Email</Link>. You can still write and preview here.
        </div>
      )}

      <div className="grid gap-6 xl:grid-cols-[15rem_minmax(0,1fr)_minmax(0,1.05fr)] items-start">
        {/* Which email */}
        <nav aria-label="Emails" className="rounded-lg border bg-card shadow-sm overflow-hidden">
          {templates.map((t) => {
            const isActive = t.key === active.key
            const unsaved = dirtyKeys.includes(t.key)
            return (
              <button
                key={t.key}
                type="button"
                aria-current={isActive ? "true" : undefined}
                onClick={() => setActiveKey(t.key)}
                className={`block w-full text-left px-4 py-3 border-b last:border-0 cursor-pointer transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring ${isActive ? "bg-muted" : "hover:bg-muted/50"}`}
              >
                <span className="flex items-center justify-between gap-2">
                  <span className="font-medium text-sm">{t.name}</span>
                  <span className="text-xs text-muted-foreground">{unsaved ? "Unsaved" : t.customised ? "Edited" : "Original"}</span>
                </span>
                <span className="block text-xs text-muted-foreground mt-1 leading-snug">{t.sent_when}</span>
              </button>
            )
          })}
        </nav>

        {/* Write */}
        <section aria-label={`${active.name} email`} className="rounded-lg border bg-card p-5 sm:p-6 shadow-sm space-y-5">
          <div>
            <label htmlFor="email-subject" className="text-sm font-medium block mb-1.5">Subject</label>
            <Input
              id="email-subject"
              ref={subjectRef}
              value={draft.subject}
              maxLength={150}
              onFocus={() => { lastField.current = "subject" }}
              onChange={(e) => setDraft({ subject: e.target.value })}
            />
          </div>

          <div>
            <div className="flex items-baseline justify-between mb-1.5">
              <label htmlFor="email-body" className="text-sm font-medium">Message</label>
              <span className="text-xs text-muted-foreground tabular-nums">{draft.body.length}/4000</span>
            </div>
            <textarea
              id="email-body"
              ref={bodyRef}
              rows={11}
              maxLength={4000}
              value={draft.body}
              aria-describedby="email-body-hint"
              onFocus={() => { lastField.current = "body" }}
              onChange={(e) => setDraft({ body: e.target.value })}
              className={`${fieldClass} leading-relaxed`}
            />
            <p id="email-body-hint" className="text-xs text-muted-foreground mt-1.5">Plain text. Leave an empty line between paragraphs.</p>
          </div>

          <div>
            <p className="text-sm font-medium mb-2">Fill-ins</p>
            <div className="flex flex-wrap gap-2">
              {data!.placeholders.map((p) => (
                <button
                  key={p.token}
                  type="button"
                  title={p.meaning}
                  onClick={() => insert(p.token)}
                  className="rounded-md border border-input bg-background px-2 py-1 font-mono text-xs cursor-pointer hover:bg-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
                >
                  {p.token}
                </button>
              ))}
            </div>
            <p className="text-xs text-muted-foreground mt-2">Click one to drop it where your cursor was. Each is replaced with the real value when the email is sent.</p>
            {usesUnknown.length > 0 && (
              <p role="alert" className="text-xs text-amber-700 dark:text-amber-400 mt-2">
                {usesUnknown.join(", ")} {usesUnknown.length === 1 ? "is" : "are"} not a fill-in the panel knows, so {usesUnknown.length === 1 ? "it" : "they"} will be sent exactly as typed.
              </p>
            )}
          </div>

          <div>
            <label htmlFor="email-button" className="text-sm font-medium block mb-1.5">Button label</label>
            <Input id="email-button" value={draft.button} maxLength={40} aria-describedby="email-button-hint" onChange={(e) => setDraft({ button: e.target.value })} className="max-w-xs" />
            <p id="email-button-hint" className="text-xs text-muted-foreground mt-1.5">{active.link_meaning} Leave empty to send the address as a plain link only.</p>
          </div>

          <div className="flex flex-wrap items-center gap-2 pt-1">
            <Button onClick={handleSave} disabled={!dirty || save.isPending || !draft.subject.trim() || !draft.body.trim()}>
              {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              Save this email
            </Button>
            {dirty && <Button variant="ghost" onClick={() => setDrafts((d) => { const next = { ...d }; delete next[active.key]; return next })}>Discard changes</Button>}
            {active.customised && (
              <Button variant="ghost" className="ml-auto" onClick={handleReset} disabled={reset.isPending}>
                <RotateCcw className="h-4 w-4" aria-hidden="true" />Use the original
              </Button>
            )}
          </div>
        </section>

        {/* See it */}
        <section aria-label="Preview" className="xl:sticky xl:top-6 space-y-4">
          <div className="rounded-xl border bg-card shadow-sm overflow-hidden">
            {/* The line a customer sees in their inbox before opening it. */}
            <div className="border-b px-4 py-3">
              <div className="flex items-baseline justify-between gap-3">
                <p className="text-sm font-semibold truncate">{branding.name}</p>
                <div role="group" aria-label="Show the email as" className="flex shrink-0 rounded-md border p-0.5 text-xs">
                  {(["html", "text"] as const).map((v) => (
                    <button
                      key={v}
                      type="button"
                      aria-pressed={view === v}
                      onClick={() => setView(v)}
                      className={`rounded px-2 py-1 font-medium cursor-pointer focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring ${view === v ? "bg-foreground text-background" : "text-muted-foreground hover:text-foreground"}`}
                    >
                      {v === "html" ? "Formatted" : "Plain text"}
                    </button>
                  ))}
                </div>
              </div>
              <p className="text-sm truncate mt-0.5">{preview?.subject ?? draft.subject}</p>
              <p className="text-xs text-muted-foreground mt-0.5">Shown with an example customer, Dewi.</p>
            </div>

            {previewError ? (
              <p role="alert" className="p-6 text-sm text-muted-foreground">{previewError}</p>
            ) : !preview ? (
              <div className="p-6 flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Drawing the email…</div>
            ) : view === "html" ? (
              // Sandboxed with no permissions: the markup is the panel's own, and it still gets no scripts.
              <iframe title={`${active.name} email, formatted`} sandbox="" srcDoc={preview.html} className="block w-full h-[520px] bg-[#f4f4f5]" />
            ) : (
              <pre className="p-5 text-sm leading-relaxed whitespace-pre-wrap font-mono h-[520px] overflow-auto">{preview.text}</pre>
            )}
          </div>

          {data!.logo !== "shown" && <p className="text-xs text-muted-foreground">{LOGO_NOTE[data!.logo]}</p>}

          <form onSubmit={handleTest} className="rounded-lg border bg-card p-4 shadow-sm">
            <label htmlFor="email-test-to" className="text-sm font-medium block mb-1.5">Send yourself this email</label>
            <div className="flex flex-wrap gap-2">
              <Input id="email-test-to" type="email" required value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder="you@example.com" className="flex-1 min-w-[12rem]" aria-describedby="email-test-hint" />
              <Button type="submit" variant="outline" disabled={sendTest.isPending || !data!.smtp_ready}>
                {sendTest.isPending ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : <Send className="h-4 w-4" aria-hidden="true" />}
                Send test
              </Button>
            </div>
            <p id="email-test-hint" className="text-xs text-muted-foreground mt-1.5">
              {data!.smtp_ready ? "Sends what you see above, including changes you have not saved yet." : "Add a mail server first to send tests."}
            </p>
          </form>
        </section>
      </div>
    </div>
  )
}
