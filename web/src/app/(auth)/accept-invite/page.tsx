"use client"

import { Suspense, useState } from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { AlertTriangle, CheckCircle2, Loader2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { useAcceptInvitation, useInvitation } from "@/lib/hooks/use-team"

const heading = "text-[2rem] font-semibold leading-tight tracking-[-0.01em] text-balance text-foreground"

function AcceptInviteForm() {
  const token = useSearchParams().get("token") ?? ""
  const { data: invite, isLoading, error } = useInvitation(token)
  const accept = useAcceptInvitation()
  const [name, setName] = useState("")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [formError, setFormError] = useState<string | null>(null)

  if (!token || error) {
    return (
      <div className="w-full space-y-4 text-center">
        <AlertTriangle className="mx-auto h-10 w-10 text-destructive" aria-hidden="true" />
        <h1 className={heading}>This invitation does not work</h1>
        <p className="text-sm text-muted-foreground">
          It was revoked, already accepted, or is more than 7 days old. Ask the person who invited you to send a new one.
        </p>
        <Link href="/login" className="inline-block text-sm font-semibold text-foreground underline underline-offset-4">Go to sign in</Link>
      </div>
    )
  }
  if (isLoading || !invite) {
    return <p className="text-sm text-muted-foreground" aria-busy="true">Reading the invitation…</p>
  }
  if (accept.isSuccess) {
    return (
      <div className="w-full space-y-4 text-center">
        <CheckCircle2 className="mx-auto h-10 w-10 text-primary" aria-hidden="true" />
        <h1 className={heading}>You are in</h1>
        <p className="text-sm text-muted-foreground">
          Sign in as {invite.email} to work in {invite.account_name}. Its servers appear in My VMs.
        </p>
        <Button asChild size="xl" className="w-full"><Link href="/login">Sign in</Link></Button>
      </div>
    )
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setFormError(null)
    if (!invite.has_account && password !== confirm) {
      setFormError("The passwords do not match.")
      return
    }
    accept.mutate(
      invite.has_account ? { token } : { token, name, password },
      { onError: (err) => setFormError(err.message || "The invitation could not be accepted.") },
    )
  }

  return (
    <div className="w-full">
      <h1 className={`${heading} mb-2`}>Join {invite.account_name}</h1>
      <p className="mb-6 text-sm text-muted-foreground">
        You were invited as <span className="font-medium text-foreground">{invite.role_words}</span>, with the address {invite.email}.
        {invite.has_account
          ? " You already have an account here: accept, then sign in with your usual password."
          : " Choose a password for your own account. You can turn on two-factor sign-in after you sign in."}
      </p>

      {formError && (
        <div role="alert" className="mb-6 rounded-lg border border-destructive/20 bg-destructive/10 p-4">
          <p className="text-sm font-medium text-destructive">{formError}</p>
        </div>
      )}

      <form onSubmit={submit} className="space-y-5">
        {!invite.has_account && (
          <>
            <div>
              <label htmlFor="name" className="mb-2 block text-sm font-medium text-foreground">Your name</label>
              <Input id="name" value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" disabled={accept.isPending} />
            </div>
            <div>
              <label htmlFor="password" className="mb-2 block text-sm font-medium text-foreground">Password</label>
              <Input id="password" type="password" required value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" disabled={accept.isPending} />
            </div>
            <div>
              <label htmlFor="confirm" className="mb-2 block text-sm font-medium text-foreground">Confirm the password</label>
              <Input id="confirm" type="password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" disabled={accept.isPending} />
            </div>
          </>
        )}
        <Button type="submit" size="xl" className="w-full text-base font-medium" disabled={accept.isPending || (!invite.has_account && (!password || !confirm))}>
          {accept.isPending && <Loader2 className="mr-2 h-5 w-5 animate-spin" aria-hidden="true" />}
          Accept the invitation
        </Button>
      </form>
    </div>
  )
}

export default function AcceptInvitePage() {
  return (
    <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
      <AcceptInviteForm />
    </Suspense>
  )
}
