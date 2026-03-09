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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { useUser, useUpdateUser, useDeleteUser } from "@/lib/hooks/use-users"
import { useVMs } from "@/lib/hooks/use-vms"
import type { VM, VMStatus } from "@/types"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" })
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${type === "success" ? "bg-success text-black" : "bg-danger text-white"}`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

function VMStatusBadge({ status }: { status: VMStatus }) {
  const colors: Record<string, string> = {
    running: "bg-[#CCFF00] text-black",
    stopped: "bg-[#FF4444] text-white",
    suspended: "bg-[#FFAA00] text-black",
    creating: "bg-[#00AAFF] text-white",
    error: "bg-[#FF0000] text-white",
  }
  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black ${colors[status] || colors.stopped}`}>
      <span className={`w-1.5 h-1.5 mr-1.5 rounded-full ${status === "running" ? "bg-black animate-pulse" : "bg-current"}`} />
      {status}
    </span>
  )
}

export default function UserDetailPage() {
  const params = useParams()
  const router = useRouter()
  const userId = params.id as string

  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState(false)
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
    try {
      await deleteUser.mutateAsync(userId)
      setToast({ message: "User deleted", type: "success" })
      setDeleteConfirm(false)
      setTimeout(() => router.push("/users"), 1000)
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteUser, userId, router])

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
          <Link href="/users"><Button variant="ghost" size="sm" className="border-2 border-black"><ArrowLeft className="w-4 h-4" /></Button></Link>
          <Skeleton className="h-12 w-64" />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <Skeleton className="h-64 border-4 border-black" />
          <Skeleton className="h-64 border-4 border-black" />
        </div>
      </div>
    )
  }

  // Error / not found
  if (error || !user) {
    return (
      <div className="max-w-5xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">User Not Found</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error)?.message || "The requested user does not exist."}</p>
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
          <Button variant="ghost" size="sm" className="border-2 border-black"><ArrowLeft className="w-4 h-4" /></Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <div className="w-12 h-12 bg-primary flex items-center justify-center border-2 border-black text-2xl font-black">
              {user.email.charAt(0).toUpperCase()}
            </div>
            <div>
              <h1 className="text-3xl font-black uppercase tracking-tight text-black">{user.email}</h1>
              <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
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
              <Button variant="ghost" className="border-2 border-black gap-2"><Edit2 className="w-4 h-4" />Edit</Button>
            </DialogTrigger>
            <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
              <DialogHeader>
                <DialogTitle className="text-lg font-black uppercase">Edit User</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div>
                  <label htmlFor="edit-email" className="block text-xs font-black uppercase text-gray-500 mb-1">Email</label>
                  <Input id="edit-email" type="email" value={editEmail} onChange={(e) => setEditEmail(e.target.value)} className="border-2 border-black" />
                </div>
                <div>
                  <label htmlFor="edit-role" className="block text-xs font-black uppercase text-gray-500 mb-1">Role</label>
                  <select
                    id="edit-role"
                    value={editRole}
                    onChange={(e) => setEditRole(e.target.value)}
                    className="w-full h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none"
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
          <Button variant="destructive" onClick={() => setDeleteConfirm(true)} className="gap-2"><Trash2 className="w-4 h-4" />Delete</Button>
        </div>
      </div>

      {/* User Info */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6 mb-6">
        {/* Details Card */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2"><Users className="w-5 h-5" />User Details</h2>
          <div className="space-y-4">
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500 flex items-center gap-2"><Mail className="w-4 h-4" />Email</span>
              <span className="font-black">{user.email}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500 flex items-center gap-2"><Shield className="w-4 h-4" />Role</span>
              <span className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-black uppercase border border-black ${
                user.role === "admin" ? "bg-secondary" : "bg-gray-100"
              }`}>{user.role}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">2FA</span>
              {user.two_factor_secret ? (
                <div className="flex items-center gap-1 text-success">
                  <CheckCircle className="w-4 h-4" />
                  <span className="text-xs font-bold uppercase">Enabled</span>
                </div>
              ) : (
                <div className="flex items-center gap-1 text-gray-400">
                  <XCircle className="w-4 h-4" />
                  <span className="text-xs font-bold uppercase">Disabled</span>
                </div>
              )}
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500 flex items-center gap-2"><Calendar className="w-4 h-4" />Created</span>
              <span className="font-bold">{formatDate(user.created_at)}</span>
            </div>
            <div className="flex items-center justify-between py-2">
              <span className="text-sm font-bold uppercase text-gray-500">Updated</span>
              <span className="font-bold">{formatDate(user.updated_at)}</span>
            </div>
          </div>

          {/* IP Whitelist */}
          {user.ip_whitelist && user.ip_whitelist.length > 0 && (
            <div className="mt-6 pt-4 border-t-2 border-black">
              <h3 className="text-xs font-black uppercase text-gray-500 mb-3 flex items-center gap-2"><Globe className="w-4 h-4" />IP Whitelist</h3>
              <div className="space-y-2">
                {user.ip_whitelist.map((ip) => (
                  <div key={ip} className="flex items-center gap-2 p-2 bg-gray-50 border-2 border-black">
                    <Globe className="w-3 h-3 text-gray-500" />
                    <span className="font-mono text-xs font-bold">{ip}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>

        {/* VMs Card */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2"><Monitor className="w-5 h-5" />User&apos;s VMs ({userVMs.length})</h2>
          {vmsLoading ? (
            <div className="space-y-3">
              {[1,2,3].map(i => <Skeleton key={i} className="h-14 w-full" />)}
            </div>
          ) : userVMs.length === 0 ? (
            <div className="p-8 text-center">
              <Monitor className="w-10 h-10 text-gray-300 mx-auto mb-3" />
              <p className="text-gray-500 text-sm font-medium">No VMs owned by this user</p>
            </div>
          ) : (
            <div className="space-y-3 max-h-96 overflow-y-auto">
              {userVMs.map((vm: VM) => (
                <Link key={vm.id} href={`/vms/${vm.id}`} className="flex items-center gap-3 p-3 bg-gray-50 border-2 border-black hover:bg-primary/20 transition-colors">
                  <div className="w-8 h-8 bg-primary flex items-center justify-center border-2 border-black">
                    <Server className="w-4 h-4" />
                  </div>
                  <div className="flex-1">
                    <p className="font-black text-sm">{vm.hostname}</p>
                    <p className="text-xs text-gray-500">{vm.resources.cpu} vCPU • {Math.round(vm.resources.ram / 1024)} GB RAM • {vm.resources.disk} GB Disk</p>
                  </div>
                  <VMStatusBadge status={vm.status} />
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Delete Confirmation */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Delete confirmation">
          <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={() => setDeleteConfirm(false)} aria-label="Close dialog" />
          <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
            <h3 className="text-xl font-black uppercase mb-4 flex items-center gap-2"><AlertCircle className="w-6 h-6 text-warning" />Delete User</h3>
            {userVMs.length > 0 ? (
              <p className="text-gray-600 font-medium mb-6">
                <span className="text-danger font-bold">WARNING:</span> This user owns {userVMs.length} VM(s). Deleting this user may affect those VMs. Are you sure?
              </p>
            ) : (
              <p className="text-gray-600 font-medium mb-6">Are you sure you want to delete &quot;{user.email}&quot;? This action cannot be undone.</p>
            )}
            <div className="flex gap-3 justify-end">
              <Button variant="ghost" onClick={() => setDeleteConfirm(false)} className="border-2 border-black" disabled={deleteUser.isPending}>Cancel</Button>
              <Button variant="destructive" onClick={handleDelete} disabled={deleteUser.isPending}>
                {deleteUser.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Delete User
              </Button>
            </div>
          </div>
        </div>
      )}

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
