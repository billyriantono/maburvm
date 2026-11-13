"use client"

import { useEffect, useId, useMemo, useRef, useState } from "react"
import { Check, ImagePlus, Loader2, Pipette, RotateCcw, Trash2, TriangleAlert } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { useBranding } from "@/lib/branding/provider"
import { useSaveBranding } from "@/lib/hooks/use-branding"
import {
  type Branding, DEFAULT_BRANDING, NEUTRAL_ACCENT, accentOf, contrast, faviconOf, markOf, onAccent, themeVars,
} from "@/lib/branding/core"
import { ACCEPT_ATTR, logoColours, prepareImage } from "@/lib/branding/images"
import { brandFont } from "@/lib/branding/font"

// Colours that hold white or dark text at AA and look like somebody chose them.
const PRESETS: { name: string; hex: string }[] = [
  { name: "Graphite", hex: NEUTRAL_ACCENT },
  { name: "Cobalt", hex: "#1d4ed8" },
  { name: "Lagoon", hex: "#0e7490" },
  { name: "Pine", hex: "#15803d" },
  { name: "Saffron", hex: "#f5b400" },
  { name: "Ember", hex: "#c2410c" },
  { name: "Crimson", hex: "#be123c" },
  { name: "Plum", hex: "#7e22ce" },
]

type Scene = "signin" | "dashboard"
const SCENES: { id: Scene; label: string }[] = [
  { id: "signin", label: "Sign-in page" },
  { id: "dashboard", label: "Dashboard" },
]

const fieldClass =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"

function Field({ label, hint, error, count, children }: {
  label: string; hint?: string; error?: string; count?: string; children: (id: string, describedBy: string) => React.ReactNode
}) {
  const id = useId()
  const describedBy = `${id}-hint`
  return (
    <div>
      <div className="flex items-baseline justify-between gap-3 mb-1.5">
        <label htmlFor={id} className="text-sm font-medium">{label}</label>
        {count && <span className="text-xs text-muted-foreground tabular-nums">{count}</span>}
      </div>
      {children(id, describedBy)}
      <p id={describedBy} role={error ? "alert" : undefined} className={`mt-1.5 text-xs ${error ? "text-destructive font-medium" : "text-muted-foreground"}`}>
        {error || hint || " "}
      </p>
    </div>
  )
}

function Section({ title, lead, children }: { title: string; lead: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border bg-card p-5 sm:p-6 shadow-sm">
      <h2 className="text-base font-semibold">{title}</h2>
      <p className="text-sm text-muted-foreground mt-1 mb-5">{lead}</p>
      <div className="space-y-4">{children}</div>
    </section>
  )
}

// A drop target that is also a normal file button: click, keyboard and drag
// all end in the same handler.
function ImageDrop({ label, current, onPick, onClear, busy }: {
  label: string; current: string; onPick: (file: File) => void; onClear?: () => void; busy: boolean
}) {
  const id = useId()
  const [over, setOver] = useState(false)
  return (
    <div className="flex items-center gap-4">
      <div className="h-16 w-16 shrink-0 rounded-lg border bg-[repeating-conic-gradient(hsl(var(--muted))_0%_25%,transparent_0%_50%)] bg-[length:12px_12px] flex items-center justify-center overflow-hidden">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={current} alt="" className="max-h-full max-w-full object-contain" />
      </div>
      <label
        htmlFor={id}
        onDragOver={(e) => { e.preventDefault(); setOver(true) }}
        onDragLeave={() => setOver(false)}
        onDrop={(e) => { e.preventDefault(); setOver(false); const f = e.dataTransfer.files?.[0]; if (f) onPick(f) }}
        className={`flex-1 min-w-0 cursor-pointer rounded-lg border border-dashed px-4 py-3 text-sm transition-colors focus-within:outline focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-ring ${over ? "border-primary bg-primary/5" : "border-input hover:bg-muted/60"}`}
      >
        <span className="flex items-center gap-2 font-medium">
          {busy ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : <ImagePlus className="h-4 w-4" aria-hidden="true" />}
          {label}
        </span>
        <span className="block text-xs text-muted-foreground mt-0.5">Drop a file here or choose one. PNG, JPEG, WebP, SVG or ICO.</span>
        <input
          id={id}
          type="file"
          accept={ACCEPT_ATTR}
          className="sr-only"
          onChange={(e) => { const f = e.target.files?.[0]; if (f) onPick(f); e.target.value = "" }}
        />
      </label>
      {onClear && (
        <Button type="button" variant="ghost" size="sm" onClick={onClear}>
          <Trash2 className="h-4 w-4" aria-hidden="true" />Remove
        </Button>
      )}
    </div>
  )
}

function ContrastRow({ label, ratio, need }: { label: string; ratio: number; need: number }) {
  const pass = ratio >= need
  return (
    <div className="flex items-center justify-between gap-3 text-sm py-1.5">
      <span>{label}</span>
      <span className={`inline-flex items-center gap-1.5 font-medium tabular-nums ${pass ? "text-emerald-700 dark:text-emerald-400" : "text-amber-700 dark:text-amber-400"}`}>
        {pass ? <Check className="h-4 w-4" aria-hidden="true" /> : <TriangleAlert className="h-4 w-4" aria-hidden="true" />}
        {ratio.toFixed(1)}:1 {pass ? "Readable" : "Hard to read"}
      </span>
    </div>
  )
}

export default function BrandStudioPage() {
  const confirm = useConfirm()
  const { branding: saved, setBranding } = useBranding()
  const save = useSaveBranding()
  const [draft, setDraft] = useState<Branding>(saved)
  const [scene, setScene] = useState<Scene>("signin")
  const [busy, setBusy] = useState<"logo" | "favicon" | null>(null)
  const [fromLogo, setFromLogo] = useState<string[]>([])
  const [hexText, setHexText] = useState(accentOf(saved))
  const tabRefs = useRef<Record<Scene, HTMLButtonElement | null>>({ signin: null, dashboard: null })

  const set = <K extends keyof Branding>(key: K, value: Branding[K]) => setDraft((d) => ({ ...d, [key]: value }))
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(saved), [draft, saved])
  const accent = accentOf(draft)

  // Leaving with unsaved work is the one way to lose it here.
  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault() }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirty])

  useEffect(() => {
    let live = true
    if (!draft.logo) { setFromLogo([]); return }
    logoColours(draft.logo).then((c) => { if (live) setFromLogo(c) }).catch(() => { if (live) setFromLogo([]) })
    return () => { live = false }
  }, [draft.logo])

  const errors = {
    name: draft.name.trim() ? "" : "Give your panel a name.",
    hex: /^#[0-9a-fA-F]{6}$/.test(hexText) ? "" : "Use six hex digits, like #1d4ed8.",
    support_url: !draft.support_url || /^https?:\/\/\S+\.\S+/.test(draft.support_url) ? "" : "Start with https://",
    support_email: !draft.support_email || /^\S+@\S+\.\S+$/.test(draft.support_email) ? "" : "That does not look like an email address.",
  }
  const invalid = Object.values(errors).some(Boolean)

  const pickAccent = (hex: string) => { setHexText(hex); set("accent", hex === NEUTRAL_ACCENT ? "" : hex) }

  const pickImage = async (kind: "logo" | "favicon", file: File) => {
    setBusy(kind)
    try {
      set(kind, await prepareImage(file, kind === "logo" ? 512 : 64))
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  const handleSave = async () => {
    try {
      const stored = await save.mutateAsync({ ...draft, name: draft.name.trim() })
      setBranding(stored)
      setDraft(stored)
      toast.success("Branding saved. Everyone sees it on their next page load.")
    } catch (e) {
      toast.error(`Could not save branding: ${(e as Error).message}`)
    }
  }

  const handleReset = async () => {
    const ok = await confirm({
      title: "Remove all branding?",
      description: "The name, logo, colour and sign-in text go back to the defaults for everyone. This cannot be undone.",
      confirmLabel: "Remove branding",
      destructive: true,
    })
    if (!ok) return
    try {
      const stored = await save.mutateAsync(DEFAULT_BRANDING)
      setBranding(stored)
      setDraft(stored)
      setHexText(accentOf(stored))
      toast.success("Branding removed")
    } catch (e) {
      toast.error(`Could not reset branding: ${(e as Error).message}`)
    }
  }

  const onTabKey = (e: React.KeyboardEvent) => {
    const i = SCENES.findIndex((s) => s.id === scene)
    const next = e.key === "ArrowRight" ? (i + 1) % SCENES.length : e.key === "ArrowLeft" ? (i + SCENES.length - 1) % SCENES.length : -1
    if (next < 0) return
    e.preventDefault()
    setScene(SCENES[next].id)
    tabRefs.current[SCENES[next].id]?.focus()
  }

  const year = new Date().getFullYear()
  const fg = onAccent(accent)

  return (
    <div className="pb-24">
      <div className="flex flex-wrap items-end justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Brand studio</h1>
          <p className="text-muted-foreground text-sm mt-1 max-w-xl">
            Put your own name, logo and colour on the panel your customers use. The preview follows every change; nothing is live until you save.
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={handleReset} disabled={save.isPending}>
          <RotateCcw className="h-4 w-4" aria-hidden="true" />Remove all branding
        </Button>
      </div>

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] items-start">
        {/* Controls */}
        <div className="space-y-6">
          <Section title="Name" lead="Shown in the sidebar, the browser tab, the sign-in page and every email. The wording of the emails is on the Emails page.">
            <Field label="Panel name" error={errors.name} count={`${draft.name.length}/60`}>
              {(id, desc) => <Input id={id} aria-describedby={desc} aria-invalid={!!errors.name} maxLength={60} value={draft.name} onChange={(e) => set("name", e.target.value)} />}
            </Field>
            <Field label="Tagline" hint="A few words under the name. Leave empty to show none." count={`${draft.tagline.length}/80`}>
              {(id, desc) => <Input id={id} aria-describedby={desc} maxLength={80} value={draft.tagline} onChange={(e) => set("tagline", e.target.value)} placeholder="Cloud servers in Jakarta" />}
            </Field>
          </Section>

          <Section title="Logo" lead="Without a logo, a mark is drawn from your name and colour, so the panel never looks unfinished.">
            <ImageDrop
              label={draft.logo ? "Replace logo" : "Add a logo"}
              current={markOf(draft)}
              busy={busy === "logo"}
              onPick={(f) => pickImage("logo", f)}
              onClear={draft.logo ? () => set("logo", "") : undefined}
            />
            <ImageDrop
              label={draft.favicon ? "Replace browser tab icon" : "Add a browser tab icon"}
              current={faviconOf(draft)}
              busy={busy === "favicon"}
              onPick={(f) => pickImage("favicon", f)}
              onClear={draft.favicon ? () => set("favicon", "") : undefined}
            />
            <p className="text-xs text-muted-foreground">A square image works best for the tab icon. Without one, the drawn mark is used.</p>
          </Section>

          <Section title="Colour" lead="One colour for buttons, links and the sign-in page. Text on top of it is set to dark or light for you.">
            <div className="flex flex-wrap items-end gap-3">
              <div>
                <label htmlFor="brand-colour" className="text-sm font-medium block mb-1.5">Pick</label>
                <input
                  id="brand-colour"
                  type="color"
                  value={accent}
                  onChange={(e) => pickAccent(e.target.value)}
                  className="h-10 w-16 cursor-pointer rounded-md border border-input bg-background p-1"
                />
              </div>
              <div className="w-36">
                <Field label="Hex code" error={errors.hex}>
                  {(id, desc) => (
                    <Input
                      id={id}
                      aria-describedby={desc}
                      aria-invalid={!!errors.hex}
                      value={hexText}
                      spellCheck={false}
                      className="font-mono"
                      onChange={(e) => {
                        const v = e.target.value.trim()
                        setHexText(v)
                        if (/^#[0-9a-fA-F]{6}$/.test(v)) set("accent", v.toLowerCase() === NEUTRAL_ACCENT ? "" : v.toLowerCase())
                      }}
                    />
                  )}
                </Field>
              </div>
            </div>

            <div>
              <p className="text-sm font-medium mb-2">Ready-made</p>
              <div className="flex flex-wrap gap-2">
                {PRESETS.map((p) => (
                  <button
                    key={p.hex}
                    type="button"
                    aria-pressed={accent === p.hex}
                    onClick={() => pickAccent(p.hex)}
                    className={`group inline-flex items-center gap-2 rounded-full border py-1 pl-1 pr-3 text-xs font-medium cursor-pointer transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring ${accent === p.hex ? "border-foreground bg-muted" : "border-input hover:bg-muted/60"}`}
                  >
                    <span className="h-5 w-5 rounded-full border border-black/10" style={{ background: p.hex }} />
                    {p.name}
                  </button>
                ))}
              </div>
            </div>

            {fromLogo.length > 0 && (
              <div>
                <p className="text-sm font-medium mb-2 flex items-center gap-1.5"><Pipette className="h-4 w-4" aria-hidden="true" />From your logo</p>
                <div className="flex flex-wrap gap-2">
                  {fromLogo.map((hex) => (
                    <button
                      key={hex}
                      type="button"
                      aria-pressed={accent === hex}
                      aria-label={`Use ${hex} from your logo`}
                      onClick={() => pickAccent(hex)}
                      className={`inline-flex items-center gap-2 rounded-full border py-1 pl-1 pr-3 text-xs font-mono cursor-pointer transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring ${accent === hex ? "border-foreground bg-muted" : "border-input hover:bg-muted/60"}`}
                    >
                      <span className="h-5 w-5 rounded-full border border-black/10" style={{ background: hex }} />
                      {hex}
                    </button>
                  ))}
                </div>
              </div>
            )}

            <div className="rounded-md border divide-y px-3">
              <ContrastRow label={`Button text (${fg === "#fafafa" ? "light" : "dark"}) on your colour`} ratio={contrast(accent, fg)} need={4.5} />
              <ContrastRow label="Your colour as link text on white" ratio={contrast(accent, "#ffffff")} need={4.5} />
              <ContrastRow label="Your colour as a button edge on white" ratio={contrast(accent, "#ffffff")} need={3} />
            </div>
            {contrast(accent, "#ffffff") < 3 && (
              <p className="text-xs text-amber-700 dark:text-amber-400 flex gap-1.5">
                <TriangleAlert className="h-4 w-4 shrink-0" aria-hidden="true" />
                This colour is faint on a white page. Buttons stay readable because their text turns dark, but links and outlines in it will be hard to see. A deeper shade fixes that.
              </p>
            )}
          </Section>

          <Section title="Sign-in page" lead="The headline sits above the sign-in form. The message appears beside it as a notice from you.">
            <Field label="Headline" hint="Empty uses your tagline." count={`${draft.login_headline.length}/80`}>
              {(id, desc) => <Input id={id} aria-describedby={desc} maxLength={80} value={draft.login_headline} onChange={(e) => set("login_headline", e.target.value)} placeholder="Sign in to manage your servers" />}
            </Field>
            <Field label="Notice" hint="Optional. A sentence or two: planned maintenance, opening hours, a change customers should know about." count={`${draft.login_message.length}/240`}>
              {(id, desc) => <textarea id={id} aria-describedby={desc} rows={3} maxLength={240} value={draft.login_message} onChange={(e) => set("login_message", e.target.value)} className={fieldClass} />}
            </Field>
          </Section>

          <Section title="Help and footer" lead="Where customers go when they are stuck, and the line at the bottom of the sign-in page.">
            <Field label="Help link" error={errors.support_url} hint="Your help centre or ticket page.">
              {(id, desc) => <Input id={id} aria-describedby={desc} aria-invalid={!!errors.support_url} type="url" inputMode="url" value={draft.support_url} onChange={(e) => set("support_url", e.target.value.trim())} placeholder="https://help.example.com" />}
            </Field>
            <Field label="Support email" error={errors.support_email}>
              {(id, desc) => <Input id={id} aria-describedby={desc} aria-invalid={!!errors.support_email} type="email" inputMode="email" value={draft.support_email} onChange={(e) => set("support_email", e.target.value.trim())} placeholder="support@example.com" />}
            </Field>
            <Field label="Footer line" hint={`Empty shows “© ${year} ${draft.name.trim() || "your name"}”.`} count={`${draft.footer_text.length}/160`}>
              {(id, desc) => <Input id={id} aria-describedby={desc} maxLength={160} value={draft.footer_text} onChange={(e) => set("footer_text", e.target.value)} />}
            </Field>
          </Section>
        </div>

        {/* Stage */}
        <div className="xl:sticky xl:top-6">
          <div role="tablist" aria-label="Preview" onKeyDown={onTabKey} className="flex gap-1 mb-3">
            {SCENES.map((s) => (
              <button
                key={s.id}
                ref={(el) => { tabRefs.current[s.id] = el }}
                type="button"
                role="tab"
                id={`scene-tab-${s.id}`}
                aria-selected={scene === s.id}
                aria-controls="scene-panel"
                tabIndex={scene === s.id ? 0 : -1}
                onClick={() => setScene(s.id)}
                className={`rounded-md px-3 py-1.5 text-sm font-medium cursor-pointer transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring ${scene === s.id ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted"}`}
              >
                {s.label}
              </button>
            ))}
          </div>

          {/* The preview carries the draft colours on its own wrapper, so the real page keeps the saved ones. */}
          <div
            id="scene-panel"
            role="tabpanel"
            aria-labelledby={`scene-tab-${scene}`}
            style={themeVars(draft) as React.CSSProperties}
            className="rounded-xl border bg-muted/40 shadow-sm overflow-hidden"
          >
            {/* Browser frame: this is where the tab icon and title show up. */}
            <div className="flex items-center gap-3 border-b bg-muted px-3 py-2">
              <div className="flex gap-1.5" aria-hidden="true">
                <span className="h-2.5 w-2.5 rounded-full bg-foreground/15" />
                <span className="h-2.5 w-2.5 rounded-full bg-foreground/15" />
                <span className="h-2.5 w-2.5 rounded-full bg-foreground/15" />
              </div>
              <div className="flex items-center gap-2 rounded-t-md bg-background px-3 py-1.5 text-xs font-medium max-w-[60%] -mb-2 border border-b-0">
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img src={faviconOf(draft)} alt="" className="h-4 w-4 rounded-sm object-contain" />
                <span className="truncate">{draft.name.trim() || "Your panel"}</span>
              </div>
            </div>

            <div aria-hidden="true" className="bg-background select-none pointer-events-none">
              {scene === "signin" && (
                <div className={`${brandFont.className} flex min-h-[420px] flex-col overflow-hidden bg-[color-mix(in_oklab,hsl(var(--primary))_5%,hsl(var(--card)))]`}>
                  <div className="flex items-center justify-between gap-4 px-6 pt-5">
                    <div className="flex items-center gap-2 min-w-0">
                      {/* eslint-disable-next-line @next/next/no-img-element */}
                      <img src={markOf(draft)} alt="" className="h-6 w-6 rounded object-contain" />
                      <span className="truncate text-xs font-semibold tracking-tight">{draft.name.trim() || "Your panel"}</span>
                    </div>
                    <div className="flex gap-3 text-[10px] underline decoration-foreground/30 underline-offset-2">
                      {draft.support_url && <span>Get help</span>}
                      {draft.support_email && <span className="truncate max-w-[140px]">{draft.support_email}</span>}
                    </div>
                  </div>
                  <div className="flex-1 px-6 pt-5">
                    <p className="max-w-[18ch] text-[26px] font-semibold leading-[0.98] tracking-[-0.02em] text-balance" style={{ fontStretch: "84%" }}>
                      {draft.login_headline || draft.tagline || "Sign in to manage your servers"}
                    </p>
                    <div className="mt-4 grid grid-cols-[150px_1fr] items-start gap-5">
                      <div className="space-y-2">
                        <p className="text-xs font-semibold">Sign in</p>
                        <div className="h-6 rounded border bg-background px-2 flex items-center text-[10px] text-muted-foreground">you@company.com</div>
                        <div className="h-6 rounded border bg-background px-2 flex items-center text-[10px] text-muted-foreground">••••••••</div>
                        <div className="h-7 rounded bg-primary text-primary-foreground flex items-center justify-center text-[10px] font-medium">Sign in</div>
                        <p className="text-[9px] text-primary">Reset password</p>
                      </div>
                      {draft.login_message && (
                        <div className="rounded-md border border-[color-mix(in_oklab,hsl(var(--primary))_30%,transparent)] bg-background/70 px-3 py-2">
                          <p className="text-[10px] font-semibold">Notice from {draft.name.trim() || "Your panel"}</p>
                          <p className="mt-1 text-[10px] leading-relaxed text-foreground/80">{draft.login_message}</p>
                        </div>
                      )}
                    </div>
                  </div>
                  <p className="px-6 pb-1 text-[9px] text-foreground/60">{draft.footer_text || `© ${year} ${draft.name.trim() || "Your panel"}`}</p>
                  <div className="whitespace-nowrap pl-[22px] -mb-[0.2em] text-[84px] font-extrabold leading-[0.78] tracking-[-0.045em] text-primary" style={{ fontStretch: "125%" }}>
                    {draft.name.trim() || "Your panel"}
                  </div>
                </div>
              )}

              {scene === "dashboard" && (
                <div className="grid grid-cols-[150px_1fr] min-h-[420px]">
                  <div className="border-r bg-card p-3">
                    <div className="flex items-center gap-2 pb-3 mb-3 border-b">
                      {/* eslint-disable-next-line @next/next/no-img-element */}
                      <img src={markOf(draft)} alt="" className="h-7 w-7 rounded-md object-contain" />
                      <div className="min-w-0">
                        <p className="text-xs font-bold leading-none truncate">{draft.name.trim() || "Your panel"}</p>
                        <p className="text-[9px] text-muted-foreground truncate mt-0.5">{draft.tagline || "Client area"}</p>
                      </div>
                    </div>
                    {["Dashboard", "Servers", "Networks", "Billing"].map((item, i) => (
                      <div key={item} className={`rounded-md px-2 py-1.5 text-[11px] font-medium mb-1 ${i === 1 ? "bg-primary text-primary-foreground" : "text-muted-foreground"}`}>{item}</div>
                    ))}
                  </div>
                  <div className="p-5">
                    <div className="flex items-center justify-between mb-4">
                      <p className="text-sm font-semibold">Servers</p>
                      <div className="rounded-md bg-primary text-primary-foreground px-3 py-1.5 text-[11px] font-medium">New server</div>
                    </div>
                    {["web-01", "db-01", "mail-01"].map((vm, i) => (
                      <div key={vm} className="flex items-center justify-between rounded-md border px-3 py-2 mb-2 text-[11px]">
                        <span className="font-medium">{vm}</span>
                        <span className="text-muted-foreground">{i === 2 ? "Stopped" : "Running"}</span>
                        <span className="text-primary underline underline-offset-2">Manage</span>
                      </div>
                    ))}
                    <div className="mt-4 rounded-md border border-primary px-3 py-2 text-[11px] text-primary font-medium inline-block">Outlined button</div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Appears only when there is something to save. */}
      {dirty && (
        <div className="fixed inset-x-0 bottom-0 z-40 border-t bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80 pb-[env(safe-area-inset-bottom,0px)]">
          <div className="mx-auto flex max-w-screen-2xl flex-wrap items-center justify-end gap-3 px-6 py-3 lg:pl-72">
            <p className="mr-auto text-sm font-medium" role="status">
              {invalid ? "Fix the marked fields to save." : "You have changes that are not live yet."}
            </p>
            <Button variant="ghost" onClick={() => { setDraft(saved); setHexText(accentOf(saved)) }} disabled={save.isPending}>Discard</Button>
            <Button onClick={handleSave} disabled={save.isPending || invalid}>
              {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              Save and publish
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
