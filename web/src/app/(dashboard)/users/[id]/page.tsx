"use client"

import { useState, useCallback, useEffect } from "react"
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
  CheckCircle,
  XCircle,
  Loader2,
  AlertCircle,
  Users,
  Server,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { useUser, useUpdateUser, useDeleteUser } from "@/lib/hooks/use-users"
import { useVMs } from "@/lib/hooks/use-vms"
import type { VM, VMStatus } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" })
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  return (
    <div className={`fixed bottom-4 right-4 z-50 px-5 py-3 rounded-lg border shadow-md ${type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"}`}>
      <p className="font-medium text-sm">{message}</p>
    </div>
  )
}

function VMStatusBadge({ status }: { status: VMStatus }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900",
    stopped: "bg-muted text-muted-foreground border-border",
    suspended: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
    creating: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-300 dark:border-blue-900",
    deleting: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
    error: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900",
  }
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-[10px] font-medium border ${colors[status] || colors.stopped}`}>
      <span className={`w-1.5 h-1.5 mr-1.5 rounded-full ${status === "running" ? "bg-current animate-pulse" : "bg-current"}`} />
      {status}
    </span>
  )
}

export default function UserDetailPage() {
  const confirm = useConfirm()
  const params = useParams()
  const router = useRouter()
  const userId = params.id as string

  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [editOpen, setEditOpen] = useState(false)
  const [editEmail, setEditEmail] = useState("")
  const [editRole, setEditRole] = useState<string>("")

  // Data hooks
  const { data: user, isLoading, error, refetch } = useUser(userId)
  const { data: vmsData, isLoading: vmsLoading } = useVMs({ userId, pageSize: 100 })
  const updateUser = useUpdateUser(userId)
  const deleteUser = useDeleteUser()

  const userVMs = vmsData?.data || []

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
    })
    if (!ok) return
    try {
      await deleteUser.mutateAsync(userId)
      setToast({ message: "User deleted", type: "success" })
      setTimeout(() => router.push("/users"), 1000)
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [confirm, user, userVMs.length, deleteUser, userId, router])

  // Edit handler
  const handleEdit = useCallback(async () => {
    try {
      await updateUser.mutateAsync({
        email: editEmail || undefined,
        role: (editRole as "admin" | "client") || undefined,
      })
      setToast({ message: "User updated", type: "success" })
      setEditOpen(false)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to update: ${(err as Error).message}`, type: "error" })
    }
  }, [updateUser, editEmail, editRole, refetch])

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
                {user.role} • Created {formatDate(user.created_at)}
              </p>
            </div>
          </div>
        </div>
        <div className="flex gap-2">
          <Dialog open={editOpen} onOpenChange={(open) => {
            setEditOpen(open)
            if (open) { setEditEmail(user.email); setEditRole(user.role) }
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
                  <label htmlFor="edit-email" className="block text-xs font-medium text-muted-foreground mb-1">Email</label>
                  <Input id="edit-email" type="email" value={editEmail} onChange={(e) => setEditEmail(e.target.value)} />
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
              <span className="text-sm text-muted-foreground flex items-center gap-2"><Mail className="w-4 h-4" />Email</span>
              <span className="font-medium">{user.email}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground flex items-center gap-2"><Shield className="w-4 h-4" />Role</span>
              <Badge variant={user.role === "admin" ? "default" : "secondary"} className="font-medium">{user.role}</Badge>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">2FA</span>
              {user.two_factor_secret ? (
                <div className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                  <CheckCircle className="w-4 h-4" />
                  <span className="text-xs font-medium">Enabled</span>
                </div>
              ) : (
                <div className="flex items-center gap-1 text-muted-foreground">
                  <XCircle className="w-4 h-4" />
                  <span className="text-xs font-medium">Disabled</span>
                </div>
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
                  <VMStatusBadge status={vm.status} />
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>


      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
