"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertTriangle, CheckCircle2, Loader2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"

const ENDPOINT = "/api/v1/auth/first-run"

// The first administrator of a new install. The one-time token comes in the
// link scripts/install-panel.sh prints, after the #, so it never reaches a
// server log.
export default function FirstRunPage() {
  const [token, setToken] = useState<string | null>(null)
  const [open, setOpen] = useState<boolean | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [name, setName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [created, setCreated] = useState<string | null>(null)

  useEffect(() => {
    setToken(new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "")
    fetch(ENDPOINT)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`the panel answered ${r.status}`))))
      .then((r) => setOpen(!!r?.data?.open))
      .catch((e: Error) => setLoadError(e.message))
  }, [])

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    if (password !== confirm) {
      setError("The passwords do not match.")
      return
    }
    setBusy(true)
    try {
      const res = await fetch(ENDPOINT, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, name, email, password }),
      })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) {
        setError(body.error || `The panel answered ${res.status}.`)
        return
      }
      // Drop the token from the address bar and history.
      window.history.replaceState(null, "", "/first-run")
      setCreated(body?.data?.email ?? email)
    } catch {
      setError("The panel could not be reached. Try again.")
    } finally {
      setBusy(false)
    }
  }

  const heading = "text-[2rem] font-semibold leading-tight tracking-[-0.01em] text-balance text-foreground"

  if (created) {
    return (
      <div className="w-full space-y-4 text-center">
        <CheckCircle2 className="mx-auto h-10 w-10 text-primary" aria-hidden="true" />
        <h1 className={heading}>Administrator created</h1>
        <p className="text-sm text-muted-foreground">
          Sign in as <span className="font-medium text-foreground">{created}</span>, then turn on two-step sign-in under
          your profile.
        </p>
        <Link href="/login" className="inline-block text-sm font-semibold text-foreground underline underline-offset-4">
          Sign in
        </Link>
      </div>
    )
  }

  if (loadError || open === false || token === "") {
    const [title, text] = loadError
      ? ["The panel did not answer", `It could not be asked whether setup is open: ${loadError}.`]
      : open === false
        ? ["Setup is finished", "This panel already has an administrator."]
        : ["This link has no token", "Use the whole link the installer printed, or read first-run-token on the panel's data volume."]
    return (
      <div className="w-full space-y-4 text-center">
        <AlertTriangle className="mx-auto h-10 w-10 text-destructive" aria-hidden="true" />
        <h1 className={heading}>{title}</h1>
        <p className="text-sm text-muted-foreground">{text}</p>
        <Link href="/login" className="inline-block text-sm font-semibold text-foreground underline underline-offset-4">
          Go to sign in
        </Link>
      </div>
    )
  }

  if (open === null || token === null) {
    return (
      <div className="flex justify-center py-8">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" aria-label="Loading" />
      </div>
    )
  }

  return (
    <div className="w-full">
      <h1 className={`${heading} mb-2`}>Create the first administrator</h1>
      <p className="mb-6 text-sm text-muted-foreground">
        This account owns the panel: it manages staff, nodes and settings. This link works once.
      </p>

      {error && (
        <div role="alert" className="mb-6 flex items-start gap-3 rounded-lg border border-destructive/20 bg-destructive/10 p-4">
          <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-destructive" aria-hidden="true" />
          <p className="text-sm font-medium text-destructive">{error}</p>
        </div>
      )}

      <form onSubmit={onSubmit} className="space-y-5">
        <div>
          <label htmlFor="name" className="mb-2 block text-sm font-medium text-foreground">Name</label>
          <Input id="name" required autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} disabled={busy} />
        </div>
        <div>
          <label htmlFor="email" className="mb-2 block text-sm font-medium text-foreground">Email</label>
          <Input id="email" type="email" required autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} disabled={busy} />
        </div>
        <div>
          <label htmlFor="password" className="mb-2 block text-sm font-medium text-foreground">Password</label>
          <Input id="password" type="password" required autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} disabled={busy} />
          <p className="mt-2 text-xs text-muted-foreground">At least 12 characters with an uppercase letter, a number and a symbol.</p>
        </div>
        <div>
          <label htmlFor="confirm" className="mb-2 block text-sm font-medium text-foreground">Password again</label>
          <Input id="confirm" type="password" required autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} disabled={busy} />
        </div>
        <Button type="submit" size="xl" className="w-full text-base font-medium" disabled={busy || !name || !email || !password || !confirm}>
          {busy ? <><Loader2 className="mr-2 h-5 w-5 animate-spin" aria-hidden="true" />Creating…</> : "Create administrator"}
        </Button>
      </form>
    </div>
  )
}
