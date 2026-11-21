"use client"

import { useState, useCallback } from "react"
import Link from "next/link"
import { useParams, useRouter } from "next/navigation"
import { 
  ArrowLeft, 
  Edit2, 
  Trash2, 
  Shield, 
  Mail,
  Calendar,
  Monitor,
  Globe,
  Loader2,
  AlertCircle,
  Users,
  Server,
} from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import {
  useUser,
  useUpdateUser,
  useDeleteUser,
  useSetUserSuspended,
  useResetUserPassword,
  useResetUserTwoFactor,
  useUnlockUser,
} from "@/lib/hooks/use-users"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useVMs } from "@/lib/hooks/use-vms"
import { useCopy } from "@/lib/use-feedback"
import { isSignInLocked, type VM } from "@/types"
import { StatusBadge, vmStatus } from "@/components/status-badge"
import { useConfirm } from "@/components/confirm-provider"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" })
}

function formatTime(dateString: string): string {
  return new Date(dateString).toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
}

export default function UserDetailPage() {
  const confirm = useConfirm()
  const params = useParams()
  const router = useRouter()
  const userId = params.id as string

  const [editOpen, setEditOpen] = useState(false)
  const [editName, setEditName] = useState("")
  const [editEmail, setEditEmail] = useState("")
  const [editRole, setEditRole] = useState<string>("")

  // Data hooks
  const { data: user, isLoading, error, refetch } = useUser(userId)
  const { data: me } = useCurrentUser()
  const { data: vmsData, isLoading: vmsLoading } = useVMs({ userId, pageSize: 100 })
  const updateUser = useUpdateUser(userId)
  const deleteUser = useDeleteUser()
  const setSuspended = useSetUserSuspended(userId)
  const resetPassword = useResetUserPassword(userId)
  const resetTwoFactor = useResetUserTwoFactor(userId)
  const unlock = useUnlockUser(userId)
  const { copy, copied } = useCopy()
  // One's own password and two-factor are changed under Profile, with the
  // current password or a code; the panel refuses these actions on oneself.
  const isMe = !!me && me.id === userId

  // Password reset: an emailed link, or a temporary password shown once.
  const [resetOpen, setResetOpen] = useState(false)
  const [resetMethod, setResetMethod] = useState<"email" | "temporary">("email")
  const [tempPassword, setTempPassword] = useState("")

  const handleResetPassword = useCallback(async () => {
    try {
      const result = await resetPassword.mutateAsync(resetMethod)
      if (result.temporary_password) {
        setTempPassword(result.temporary_password)
        return
      }
      toast.success(`Reset link sent to ${result.sent_to ?? user?.email}`)
      setResetOpen(false)
    } catch (err) {
      // The panel's message says what failed and what to do instead.
      toast.error((err as Error).message)
    }
  }, [resetPassword, resetMethod, user])

  const handleResetTwoFactor = useCallback(async () => {
    const ok = await confirm({
      title: `Turn off two-factor for ${user?.email ?? ""}?`,
      description:
        "For someone who lost their authenticator app and their backup codes. They sign in with their password alone until they turn two-factor on again.",
      confirmLabel: "Turn off two-factor",
      destructive: true,
      action: () => resetTwoFactor.mutateAsync(),
    })
    if (ok) toast.success("Two-factor is off for this account")
  }, [confirm, user, resetTwoFactor])

  const handleUnlock = useCallback(async () => {
    try {
      await unlock.mutateAsync()
      toast.success("Account unlocked. They can sign in again.")
    } catch (err) {
      toast.error(`Not unlocked: ${(err as Error).message}`)
    }
  }, [unlock])

  const userVMs = vmsData?.data || []

  // Suspending and restoring share one dialog: the same two choices either way.
  const [suspendOpen, setSuspendOpen] = useState(false)
  const [withVms, setWithVms] = useState(false)
  const [reason, setReason] = useState("")
  const suspended = !!user?.suspended_at
  // What "with their servers" would touch: on suspend the ones not yet
  // suspended, on restore the ones that are.
  const affectedVMs = userVMs.filter((vm: VM) => vm.status !== "deleting" && !!vm.suspended === suspended).length

  const handleSuspend = useCallback(async () => {
    try {
      const result = await setSuspended.mutateAsync({ suspend: !suspended, withVms, reason: reason.trim() || undefined })
      const done = suspended ? "Account restored" : "Account suspended"
      if (result.vms_failed.length > 0) {
        toast.warning(`${done}. ${suspended ? "Not started" : "Not powered off"}: ${result.vms_failed.join(", ")}. Check ${result.vms_failed.length === 1 ? "that server" : "those servers"}.`)
      } else {
        toast.success(done)
      }
      setSuspendOpen(false)
    } catch (err) {
      toast.error(`${suspended ? "Not restored" : "Not suspended"}: ${(err as Error).message}`)
    }
  }, [setSuspended, suspended, withVms, reason])

  // Delete handler
  const handleDelete = useCallback(async () => {
    // The VM count is the whole reason this confirmation exists: deleting an
    // account that still owns machines is the mistake worth interrupting.
    const ok = await confirm({
      title: `Delete user "${user?.email ?? ""}"?`,
      description:
        userVMs.length > 0
          ? `This account still owns ${userVMs.length} VM(s). They are not deleted with it, and will be left without an owner.`
          : "They lose access immediately. This cannot be undone.",
      confirmLabel: "Delete user",
      destructive: true,
      details: userVMs.length > 0 ? [{ label: "VMs owned", value: userVMs.length }] : undefined,
      action: () => deleteUser.mutateAsync(userId),
    })
    if (!ok) return
    toast.success("User deleted")
    setTimeout(() => router.push("/users"), 1000)
  }, [confirm, user, userVMs.length, deleteUser, userId, router])

  // Edit handler
  const handleEdit = useCallback(async () => {
    try {
      await updateUser.mutateAsync({
        name: editName,
        email: editEmail || undefined,
        role: (editRole as "admin" | "client") || undefined,
      })
      toast.success("User updated")
      setEditOpen(false)
      refetch()
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }, [updateUser, editName, editEmail, editRole, refetch])

  // Loading
  if (isLoading) {
    return (
      <div className="max-w-5xl mx-auto">
        <div className="flex items-center gap-4 mb-6">
          <Link href="/users"><Button variant="outline" size="sm"><ArrowLeft className="w-4 h-4" /></Button></Link>
          <Skeleton className="h-12 w-64" />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <Skeleton className="h-64 rounded-lg" />
          <Skeleton className="h-64 rounded-lg" />
        </div>
      </div>
    )
  }

  // Error / not found
  if (error || !user) {
    return (
      <div className="max-w-5xl mx-auto">
        <div className="rounded-lg border bg-card text-card-foreground p-12 shadow-sm text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" />
          <h2 className="text-xl font-semibold mb-2">User Not Found</h2>
          <p className="text-muted-foreground mb-6">{(error as Error)?.message || "The requested user does not exist."}</p>
          <Link href="/users"><Button className="gap-2"><ArrowLeft className="w-4 h-4" />Back to Users</Button></Link>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/users">
          <Button variant="outline" size="sm"><ArrowLeft className="w-4 h-4" /></Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <div className="w-12 h-12 rounded-md bg-muted flex items-center justify-center border text-2xl font-semibold text-muted-foreground">
              {user.email.charAt(0).toUpperCase()}
            </div>
            <div>
              <h1 className="text-3xl font-bold tracking-tight text-foreground">{user.email}</h1>
              <p className="text-muted-foreground text-sm">
                {user.name ? `${user.name} • ` : ""}{user.role} • Created {formatDate(user.created_at)}
              </p>
            </div>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Dialog open={editOpen} onOpenChange={(open) => {
            setEditOpen(open)
            if (open) { setEditName(user.name ?? ""); setEditEmail(user.email); setEditRole(user.role) }
          }}>
            <DialogTrigger asChild>
              <Button variant="outline" className="gap-2"><Edit2 className="w-4 h-4" />Edit</Button>
            </DialogTrigger>
            <DialogContent className="max-w-md">
              <DialogHeader>
                <DialogTitle className="text-lg font-semibold">Edit User</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div>
                  <label htmlFor="edit-name" className="block text-xs font-medium text-muted-foreground mb-1">Name</label>
                  <Input id="edit-name" value={editName} onChange={(e) => setEditName(e.target.value)} maxLength={255} />
                </div>
                <div>
                  <label htmlFor="edit-email" className="block text-xs font-medium text-muted-foreground mb-1">Email</label>
                  <Input id="edit-email" type="email" value={editEmail} onChange={(e) => setEditEmail(e.target.value)} />
                  {editEmail.trim().toLowerCase() !== user.email.toLowerCase() && (
                    <p className="mt-1 text-xs text-muted-foreground">
                      They sign in with the new address from now on. If email is set up, the old address is told about the change.
                    </p>
                  )}
                </div>
                <div>
                  <label htmlFor="edit-role" className="block text-xs font-medium text-muted-foreground mb-1">Role</label>
                  <select
                    id="edit-role"
                    value={editRole}
                    onChange={(e) => setEditRole(e.target.value)}
                    className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  >
                    <option value="admin">Admin</option>
                    <option value="client">Client</option>
                  </select>
                </div>
              </div>
              <DialogFooter>
                <Button variant="ghost" onClick={() => setEditOpen(false)}>Cancel</Button>
                <Button onClick={handleEdit} disabled={updateUser.isPending || !editEmail.trim()}>
                  {updateUser.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Save
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          {!isMe && (
            <Dialog open={resetOpen} onOpenChange={(open) => {
              setResetOpen(open)
              if (open) { setResetMethod("email"); setTempPassword("") }
            }}>
              <DialogTrigger asChild>
                <Button variant="outline">Reset password</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">
                    {tempPassword ? "Temporary password" : `Reset the password of ${user.email}?`}
                  </DialogTitle>
                  <DialogDescription>
                    {tempPassword
                      ? "Shown this once. Give it to them over a channel you trust; they can change it under Profile."
                      : "Their current password stops working only once a new one is set."}
                  </DialogDescription>
                </DialogHeader>
                {tempPassword ? (
                  <div className="space-y-3 py-2">
                    <p className="flex flex-wrap items-center gap-2">
                      <code className="break-all rounded-md border bg-muted px-2 py-1 font-mono text-sm">{tempPassword}</code>
                      <Button type="button" variant="ghost" size="sm" onClick={() => copy(tempPassword, "temp-password")}>
                        {copied("temp-password") ? "Copied" : "Copy"}
                      </Button>
                    </p>
                    <p className="text-sm text-muted-foreground">Every session of this account was signed out.</p>
                  </div>
                ) : (
                  <fieldset className="space-y-3 py-2">
                    <legend className="sr-only">How to reset it</legend>
                    <label className="flex items-start gap-3 text-sm cursor-pointer">
                      <input type="radio" name="reset-method" checked={resetMethod === "email"} onChange={() => setResetMethod("email")} className="mt-0.5 w-4 h-4 accent-primary" />
                      <span>
                        Email them a link to choose a new one
                        <span className="block text-xs text-muted-foreground">The link works once, for an hour, and goes to {user.email}.</span>
                      </span>
                    </label>
                    <label className="flex items-start gap-3 text-sm cursor-pointer">
                      <input type="radio" name="reset-method" checked={resetMethod === "temporary"} onChange={() => setResetMethod("temporary")} className="mt-0.5 w-4 h-4 accent-primary" />
                      <span>
                        Set a temporary password and show it to me
                        <span className="block text-xs text-muted-foreground">For when they cannot get the email. Their open sessions are signed out.</span>
                      </span>
                    </label>
                  </fieldset>
                )}
                <DialogFooter>
                  {tempPassword ? (
                    <Button onClick={() => setResetOpen(false)}>Done</Button>
                  ) : (
                    <>
                      <Button variant="ghost" onClick={() => setResetOpen(false)}>Cancel</Button>
                      <Button onClick={handleResetPassword} disabled={resetPassword.isPending}>
                        {resetPassword.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
                        {resetMethod === "email" ? "Send reset link" : "Set temporary password"}
                      </Button>
                    </>
                  )}
                </DialogFooter>
              </DialogContent>
            </Dialog>
          )}
          {user.role === "client" && (
            <Dialog open={suspendOpen} onOpenChange={(open) => {
              setSuspendOpen(open)
              if (open) { setWithVms(false); setReason("") }
            }}>
              <DialogTrigger asChild>
                <Button variant="outline">{suspended ? "Restore account" : "Suspend account"}</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">
                    {suspended ? `Restore ${user.email}?` : `Suspend ${user.email}?`}
                  </DialogTitle>
                </DialogHeader>
                <div className="space-y-4 py-2">
                  <p className="text-sm text-muted-foreground">
                    {suspended
                      ? "They can sign in again and their API keys work again. Sessions from before the suspension stay signed out."
                      : "They cannot sign in, and their open sessions and API keys stop working at once. Nothing is deleted, and you can restore the account at any time."}
                  </p>
                  {affectedVMs > 0 ? (
                    <label className="flex items-start gap-3 text-sm cursor-pointer">
                      <input type="checkbox" checked={withVms} onChange={(e) => setWithVms(e.target.checked)} className="mt-0.5 w-4 h-4 accent-primary" />
                      <span>
                        {suspended
                          ? `Also restore their ${affectedVMs} suspended ${affectedVMs === 1 ? "server" : "servers"} and start ${affectedVMs === 1 ? "it" : "them"}`
                          : `Also suspend their ${affectedVMs} ${affectedVMs === 1 ? "server" : "servers"}`}
                        <span className="block text-xs text-muted-foreground">
                          {suspended
                            ? "This lifts every suspension on the account, including one billing placed on a single service."
                            : "Each is powered off now, without a clean shutdown, and stays off until it is restored."}
                        </span>
                      </span>
                    </label>
                  ) : (
                    <p className="text-sm text-muted-foreground">
                      {suspended ? "None of their servers is suspended." : "They have no servers left to suspend."}
                    </p>
                  )}
                  <div>
                    <label htmlFor="suspend-reason" className="block text-xs font-medium text-muted-foreground mb-1">Reason, for the audit log (optional)</label>
                    <Input id="suspend-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} />
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setSuspendOpen(false)}>Cancel</Button>
                  <Button variant={suspended ? "default" : "destructive"} onClick={handleSuspend} disabled={setSuspended.isPending}>
                    {setSuspended.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
                    {suspended ? "Restore account" : "Suspend account"}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          )}
          <Button variant="destructive" onClick={() => handleDelete()} className="gap-2"><Trash2 className="w-4 h-4" />Delete</Button>
        </div>
      </div>

      {/* User Info */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6 mb-6">
        {/* Details Card */}
        <div className="rounded-lg border bg-card text-card-foreground p-6 shadow-sm">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Users className="w-5 h-5" />User Details</h2>
          <div className="space-y-1">
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Name</span>
              <span className="font-medium">{user.name || <span className="text-muted-foreground font-normal">Not set</span>}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground flex items-center gap-2"><Mail className="w-4 h-4" />Email</span>
              <span className="font-medium">{user.email}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground flex items-center gap-2"><Shield className="w-4 h-4" />Role</span>
              <Badge variant={user.role === "admin" ? "default" : "secondary"} className="font-medium">{user.role}</Badge>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Account</span>
              {suspended ? (
                <span className="flex items-center gap-2">
                  <StatusBadge status="suspended" />
                  <span className="text-xs text-muted-foreground">since {formatDate(user.suspended_at!)}</span>
                </span>
              ) : (
                <StatusBadge status="active" />
              )}
            </div>
            <div className="flex items-center justify-between gap-3 py-2 border-b">
              <span className="shrink-0 text-sm text-muted-foreground">Two-factor</span>
              <span className="flex items-center gap-3">
                <StatusBadge status={user.two_factor_enabled ? "on" : "off"} />
                {user.two_factor_enabled && !isMe && (
                  <Button variant="outline" size="sm" onClick={handleResetTwoFactor}>Turn off</Button>
                )}
              </span>
            </div>
            <div className="flex items-center justify-between gap-3 py-2 border-b">
              <span className="shrink-0 text-sm text-muted-foreground">Sign-in</span>
              {isSignInLocked(user) ? (
                <span className="flex flex-wrap items-center justify-end gap-3">
                  <StatusBadge status="locked" />
                  <span className="text-xs text-muted-foreground" title="Too many failed sign-ins in a row">until {formatTime(user.locked_until!)}</span>
                  {!isMe && (
                    <Button variant="outline" size="sm" onClick={handleUnlock} disabled={unlock.isPending}>
                      {unlock.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Unlock
                    </Button>
                  )}
                </span>
              ) : (
                <span className="text-xs font-medium">Not locked</span>
              )}
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground flex items-center gap-2"><Calendar className="w-4 h-4" />Created</span>
              <span className="font-medium">{formatDate(user.created_at)}</span>
            </div>
            <div className="flex items-center justify-between py-2">
              <span className="text-sm text-muted-foreground">Updated</span>
              <span className="font-medium">{formatDate(user.updated_at)}</span>
            </div>
          </div>

          {/* IP Whitelist */}
          {user.ip_whitelist && user.ip_whitelist.length > 0 && (
            <div className="mt-6 pt-4 border-t">
              <h3 className="text-xs font-medium text-muted-foreground mb-3 flex items-center gap-2"><Globe className="w-4 h-4" />IP Whitelist</h3>
              <div className="space-y-2">
                {user.ip_whitelist.map((ip) => (
                  <div key={ip} className="flex items-center gap-2 p-2 rounded-md bg-muted border">
                    <Globe className="w-3 h-3 text-muted-foreground" />
                    <span className="font-mono text-xs">{ip}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>

        {/* VMs Card */}
        <div className="rounded-lg border bg-card text-card-foreground p-6 shadow-sm">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Monitor className="w-5 h-5" />User&apos;s VMs ({userVMs.length})</h2>
          {vmsLoading ? (
            <div className="space-y-3">
              {[1,2,3].map(i => <Skeleton key={i} className="h-14 w-full" />)}
            </div>
          ) : userVMs.length === 0 ? (
            <div className="p-8 text-center">
              <Monitor className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
              <p className="text-muted-foreground text-sm">No VMs owned by this user</p>
            </div>
          ) : (
            <div className="space-y-3 max-h-96 overflow-y-auto">
              {userVMs.map((vm: VM) => (
                <Link key={vm.id} href={`/vms/${vm.id}`} className="flex items-center gap-3 p-3 rounded-md border bg-card hover:bg-muted/50 transition-colors">
                  <div className="w-8 h-8 rounded-md bg-muted flex items-center justify-center border text-muted-foreground">
                    <Server className="w-4 h-4" />
                  </div>
                  <div className="flex-1">
                    <p className="font-medium text-sm">{vm.hostname}</p>
                    <p className="text-xs text-muted-foreground">{vm.resources.cpu} vCPU • {Math.round(vm.resources.ram / 1024)} GB RAM • {vm.resources.disk} GB Disk</p>
                  </div>
                  <StatusBadge status={vmStatus(vm)} />
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>


    </div>
  )
}
