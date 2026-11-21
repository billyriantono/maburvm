"use client"

import { toast } from "sonner"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusBadge } from "@/components/status-badge"
import { useConfirm } from "@/components/confirm-provider"
import { useAccountSessions, useRevokeSession, useRevokeSessions, useSignInHistory } from "@/lib/hooks/use-staff"
import type { SignInEvent } from "@/types"

const METHODS: Record<string, string> = {
  password: "Password",
  sso: "SSO",
  billing_sso: "Billing account",
  impersonation: "Staff view",
  password_reset: "Password reset",
}

const FAILURES: Record<string, string> = {
  wrong_email_or_password: "Wrong email or password",
  wrong_two_factor_code: "Wrong two-factor code",
  ip_not_allowed: "Address not on the account's allow-list",
  account_locked: "Account locked after failed sign-ins",
  account_suspended: "Account suspended",
  error: "The panel could not finish the sign-in",
}

// describeAgent turns a user agent into "Firefox on Linux". The full string
// stays in the title.
export function describeAgent(ua: string): string {
  if (!ua) return "Unknown device"
  const browser =
    [["Edg/", "Edge"], ["OPR/", "Opera"], ["Firefox/", "Firefox"], ["Chrome/", "Chrome"], ["Safari/", "Safari"], ["curl/", "curl"]]
      .find(([k]) => ua.includes(k))?.[1] ?? ua.split(/[\s/]/)[0]
  const os =
    [["Android", "Android"], ["iPhone", "iPhone"], ["iPad", "iPad"], ["Windows", "Windows"], ["Mac OS X", "macOS"], ["Linux", "Linux"]]
      .find(([k]) => ua.includes(k))?.[1]
  return os ? `${browser} on ${os}` : browser
}

const when = (s: string) =>
  new Date(s).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })

function result(e: SignInEvent): string {
  if (e.method === "password_reset") return "reset_requested"
  return e.success ? "signed_in" : "refused"
}

function detail(e: SignInEvent): string {
  if (e.method === "impersonation") return e.note ? `Opened by ${e.note}` : "Opened by staff"
  if (!e.success) return FAILURES[e.note] ?? e.note
  return ""
}

/**
 * Sessions and sign-in history of an account: one's own (no userId), or for
 * staff, another account's. canRevoke shows the sign-out controls.
 */
export function AccountSessions({ userId, canRevoke = true }: { userId?: string; canRevoke?: boolean }) {
  const confirm = useConfirm()
  const sessions = useAccountSessions(userId)
  const history = useSignInHistory(userId)
  const revoke = useRevokeSession(userId)
  const revokeMany = useRevokeSessions(userId)
  const own = !userId
  const others = (sessions.data ?? []).filter((s) => !s.current)

  const signOut = async (id: string) => {
    try {
      await revoke.mutateAsync(id)
      toast.success("Session signed out")
    } catch (err) {
      toast.error(`Not signed out: ${(err as Error).message}`)
    }
  }

  const signOutMany = async () => {
    const ok = await confirm({
      title: own ? "Sign out everywhere else?" : "Sign this account out everywhere?",
      description: own
        ? "Every other browser and device signed in to your account is signed out. This one stays signed in."
        : "Every session of this account ends at once. Its API keys keep working.",
      confirmLabel: own ? "Sign out everywhere else" : "Sign out everywhere",
      destructive: true,
      action: () => revokeMany.mutateAsync(),
    })
    if (ok) toast.success(own ? "Signed out everywhere else" : "Every session of this account was signed out")
  }

  return (
    <div className="space-y-6">
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4 border-b">
          <div>
            <h2 className="text-lg font-semibold">Active sessions</h2>
            <p className="text-xs text-muted-foreground">
              {own ? "Where your account is signed in." : "Where this account is signed in."} API keys are listed separately.
            </p>
          </div>
          {canRevoke && (own ? others.length > 0 : (sessions.data ?? []).length > 0) && (
            <Button variant="outline" size="sm" onClick={signOutMany} disabled={revokeMany.isPending}>
              {revokeMany.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
              {own ? "Sign out everywhere else" : "Sign out everywhere"}
            </Button>
          )}
        </div>
        {sessions.isLoading ? (
          <p className="p-5 text-sm text-muted-foreground">Loading sessions…</p>
        ) : sessions.isError ? (
          <div className="p-5 flex flex-wrap items-center gap-3 text-sm">
            <span className="text-destructive">Sessions could not be loaded: {(sessions.error as Error).message}</span>
            <Button variant="outline" size="sm" onClick={() => sessions.refetch()}>Try again</Button>
          </div>
        ) : (sessions.data ?? []).length === 0 ? (
          <p className="p-5 text-sm text-muted-foreground">
            No active sessions. Sign-ins from before this panel version are not listed; they end when they expire.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground border-b">
                  <th className="px-5 py-2 font-medium">Device</th>
                  <th className="px-5 py-2 font-medium">Address</th>
                  <th className="px-5 py-2 font-medium">Signed in</th>
                  <th className="px-5 py-2 font-medium">Last active</th>
                  <th className="px-5 py-2" />
                </tr>
              </thead>
              <tbody>
                {sessions.data!.map((s) => (
                  <tr key={s.id} className="border-b last:border-0">
                    <td className="px-5 py-2" title={s.user_agent}>
                      {describeAgent(s.user_agent)}
                      {s.current && <span className="ml-2 text-xs font-medium text-muted-foreground">This session</span>}
                    </td>
                    <td className="px-5 py-2 font-mono text-xs">{s.ip_address || "—"}</td>
                    <td className="px-5 py-2 tabular-nums">
                      {when(s.created_at)}
                      <span className="block text-xs text-muted-foreground">
                        {METHODS[s.method] ?? s.method}
                        {s.impersonator_email ? ` by ${s.impersonator_email}` : ""}
                      </span>
                    </td>
                    <td className="px-5 py-2 tabular-nums">{when(s.last_seen_at)}</td>
                    <td className="px-5 py-2 text-right">
                      {canRevoke && !(own && s.current) && (
                        <Button variant="ghost" size="sm" onClick={() => signOut(s.id)} disabled={revoke.isPending}>
                          Sign out
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">Sign-in history</h2>
          <p className="text-xs text-muted-foreground">The latest attempts, good and refused, and password reset requests.</p>
        </div>
        {history.isLoading ? (
          <p className="p-5 text-sm text-muted-foreground">Loading history…</p>
        ) : history.isError ? (
          <div className="p-5 flex flex-wrap items-center gap-3 text-sm">
            <span className="text-destructive">History could not be loaded: {(history.error as Error).message}</span>
            <Button variant="outline" size="sm" onClick={() => history.refetch()}>Try again</Button>
          </div>
        ) : (history.data ?? []).length === 0 ? (
          <p className="p-5 text-sm text-muted-foreground">No sign-ins recorded yet. Recording started with this panel version.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground border-b">
                  <th className="px-5 py-2 font-medium">When</th>
                  <th className="px-5 py-2 font-medium">Result</th>
                  <th className="px-5 py-2 font-medium">How</th>
                  <th className="px-5 py-2 font-medium">Address</th>
                  <th className="px-5 py-2 font-medium">Device</th>
                </tr>
              </thead>
              <tbody>
                {history.data!.map((e) => (
                  <tr key={e.id} className="border-b last:border-0">
                    <td className="px-5 py-2 tabular-nums whitespace-nowrap">{when(e.created_at)}</td>
                    <td className="px-5 py-2">
                      <StatusBadge status={result(e)} />
                      {detail(e) && <span className="block text-xs text-muted-foreground">{detail(e)}</span>}
                    </td>
                    <td className="px-5 py-2">{METHODS[e.method] ?? e.method}</td>
                    <td className="px-5 py-2 font-mono text-xs">{e.ip_address || "—"}</td>
                    <td className="px-5 py-2" title={e.user_agent}>{describeAgent(e.user_agent)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
