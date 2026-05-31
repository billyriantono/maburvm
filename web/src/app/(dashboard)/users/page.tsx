"use client"

import { useState, useMemo, useEffect, useCallback } from "react"
import Link from "next/link"
import { 
  UserPlus, 
  Search, 
  MoreHorizontal, 
  Shield, 
  ShieldOff,
  Mail,
  Edit,
  Trash2,
  RotateCcw,
  Eye,
  CheckCircle,
  XCircle,
  Loader2,
  AlertCircle,
  Users,
  X,
  Gauge
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useUsers, useDeleteUser } from "@/lib/hooks/use-users"
import { useUserQuota, useSetUserQuota } from "@/lib/hooks/use-quota"
import type { SetQuotaRequest } from "@/types/quota"
import type { User } from "@/types"

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success text-black" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

function ConfirmDialog({ open, title, message, loading, confirmLabel, variant, onConfirm, onCancel }: { 
  open: boolean; title: string; message: string; loading?: boolean; confirmLabel?: string; variant?: "destructive" | "default"; onConfirm: () => void; onCancel: () => void 
}) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Confirm dialog">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onCancel} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
        <h3 className="text-xl font-black uppercase mb-4">{title}</h3>
        <p className="text-gray-600 font-medium mb-6">{message}</p>
        <div className="flex gap-3 justify-end">
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black" disabled={loading}>Cancel</Button>
          <Button variant={variant === "destructive" ? "destructive" : "default"} onClick={onConfirm} disabled={loading}>
            {loading && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
            {confirmLabel || "Confirm"}
          </Button>
        </div>
      </div>
    </div>
  )
}

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" })
}

// QuotaDialog lets an admin view a user's current usage and set their resource
// limits (0 = unlimited). Fetches per-user quota lazily when opened.
function QuotaDialog({ user, onClose, onNotify }: {
  user: User | null
  onClose: () => void
  onNotify: (message: string, type: "success" | "error") => void
}) {
  const { data: status, isLoading } = useUserQuota(user?.id ?? "")
  const setQuota = useSetUserQuota()
  const [form, setForm] = useState<SetQuotaRequest>({ max_vms: 0, max_vcpu: 0, max_ram_mb: 0, max_disk_gb: 0 })

  useEffect(() => {
    if (status) {
      setForm({
        max_vms: status.quota.max_vms,
        max_vcpu: status.quota.max_vcpu,
        max_ram_mb: status.quota.max_ram_mb,
        max_disk_gb: status.quota.max_disk_gb,
      })
    }
  }, [status])

  if (!user) return null

  const fields: { key: keyof SetQuotaRequest; label: string; used?: number }[] = [
    { key: "max_vms", label: "Max VMs", used: status?.usage.vms },
    { key: "max_vcpu", label: "Max vCPU", used: status?.usage.vcpu },
    { key: "max_ram_mb", label: "Max RAM (MB)", used: status?.usage.ram_mb },
    { key: "max_disk_gb", label: "Max Disk (GB)", used: status?.usage.disk_gb },
  ]

  const handleSave = async () => {
    try {
      await setQuota.mutateAsync({ userId: user.id, data: form })
      onNotify(`Quota updated for ${user.email}`, "success")
      onClose()
    } catch (err) {
      onNotify(`Failed to update quota: ${(err as Error).message}`, "error")
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Edit quota">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onClose} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-lg w-full mx-4">
        <h3 className="text-xl font-black uppercase mb-1 flex items-center gap-2"><Gauge className="w-5 h-5" />Resource Quota</h3>
        <p className="text-gray-500 font-medium text-sm mb-5 truncate">{user.email} — 0 = unlimited</p>
        {isLoading ? (
          <div className="flex items-center justify-center py-10"><Loader2 className="w-6 h-6 animate-spin" /></div>
        ) : (
          <div className="space-y-4">
            {fields.map((f) => (
              <div key={f.key} className="grid grid-cols-3 items-center gap-3">
                <label className="col-span-1 text-xs font-black uppercase text-gray-500">{f.label}</label>
                <Input
                  type="number"
                  min={0}
                  value={form[f.key]}
                  onChange={(e) => setForm({ ...form, [f.key]: Number(e.target.value) })}
                  className="col-span-1 border-2 border-black"
                />
                <span className="col-span-1 text-xs font-bold text-gray-500">used: {f.used ?? 0}</span>
              </div>
            ))}
          </div>
        )}
        <div className="flex gap-3 justify-end mt-6">
          <Button variant="ghost" onClick={onClose} className="border-2 border-black" disabled={setQuota.isPending}>Cancel</Button>
          <Button onClick={handleSave} disabled={setQuota.isPending || isLoading}>
            {setQuota.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
            Save Quota
          </Button>
        </div>
      </div>
    </div>
  )
}

export default function UsersPage() {
  const [searchQuery, setSearchQuery] = useState("")
  const [roleFilter, setRoleFilter] = useState<string>("")
  const [deleteConfirm, setDeleteConfirm] = useState<{ id: string; email: string } | null>(null)
  const [quotaUser, setQuotaUser] = useState<User | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)

  // Data hooks
  const { data: usersData, isLoading, error, refetch } = useUsers({ pageSize: 100 })
  const deleteUser = useDeleteUser()

  const users = useMemo(() => usersData?.data || [], [usersData?.data])

  // Filter users
  const filteredUsers = useMemo(() => {
    let result = [...users]

    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(u =>
        u.email.toLowerCase().includes(query) ||
        u.id.toLowerCase().includes(query)
      )
    }

    if (roleFilter) {
      result = result.filter(u => u.role === roleFilter)
    }

    return result
  }, [users, searchQuery, roleFilter])

  // Delete handler
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    try {
      await deleteUser.mutateAsync(deleteConfirm.id)
      setToast({ message: `User "${deleteConfirm.email}" deleted`, type: "success" })
      setDeleteConfirm(null)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteConfirm, deleteUser, refetch])

  const clearFilters = () => { setSearchQuery(""); setRoleFilter("") }
  const hasFilters = searchQuery || roleFilter

  // Loading
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex items-center justify-between mb-8">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black">Users</h1>
            <Skeleton className="h-5 w-48 mt-1" />
          </div>
        </div>
        <Skeleton className="h-16 border-4 border-black mb-6" />
        <div className="space-y-4">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-16 border-4 border-black" />)}
        </div>
      </div>
    )
  }

  // Error
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Failed to load users</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()}>Retry</Button>
        </div>
      </div>
    )
  }

  const adminCount = users.filter(u => u.role === "admin").length
  const clientCount = users.filter(u => u.role === "client").length

  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between mb-8">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            Users
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            {users.length} users • {adminCount} admins • {clientCount} clients
          </p>
        </div>
        <Link href="/users/new">
          <Button className="flex items-center gap-2">
            <UserPlus className="w-4 h-4" />
            Add User
          </Button>
        </Link>
      </div>

      {/* Search and Filters */}
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
            <Input
              placeholder="Search users by email..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10 border-2 border-black"
            />
          </div>
          <select
            value={roleFilter}
            onChange={(e) => setRoleFilter(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Roles</option>
            <option value="admin">Admin</option>
            <option value="client">Client</option>
          </select>
          {hasFilters && (
            <Button variant="ghost" onClick={clearFilters} className="border-2 border-black gap-1"><X className="w-4 h-4" />Clear</Button>
          )}
        </div>
      </div>

      {/* Users Table */}
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
          <div className="col-span-4">User</div>
          <div className="col-span-2">Role</div>
          <div className="col-span-2">2FA</div>
          <div className="col-span-2">Created</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {filteredUsers.length === 0 ? (
          <div className="p-12 text-center">
            <Users className="w-12 h-12 text-gray-500 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase">No users found</p>
            {hasFilters && (
              <Button variant="ghost" onClick={clearFilters} className="mt-4 border-2 border-black">Clear filters</Button>
            )}
          </div>
        ) : (
          filteredUsers.map((user, index) => (
            <div key={user.id} className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${index % 2 === 0 ? "bg-white" : "bg-gray-50"}`}>
              {/* User Info */}
              <div className="col-span-4">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                    <span className="text-sm font-black">
                      {user.email.charAt(0).toUpperCase()}
                    </span>
                  </div>
                  <div>
                    <Link
                      href={`/users/${user.id}`}
                      className="font-black text-black hover:text-primary transition-colors"
                    >
                      {user.email}
                    </Link>
                    <p className="text-xs text-gray-500 font-medium font-mono">{user.id.slice(0, 16)}...</p>
                  </div>
                </div>
              </div>

              {/* Role */}
              <div className="col-span-2">
                <span className={`inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase border border-black ${
                  user.role === "admin" ? "bg-secondary" : "bg-gray-100"
                }`}>
                  <Shield className="w-3 h-3" />
                  {user.role}
                </span>
              </div>

              {/* 2FA */}
              <div className="col-span-2">
                {user.two_factor_secret ? (
                  <div className="flex items-center gap-1 text-success">
                    <CheckCircle className="w-4 h-4" />
                    <span className="text-xs font-bold uppercase">Enabled</span>
                  </div>
                ) : (
                  <div className="flex items-center gap-1 text-gray-600">
                    <XCircle className="w-4 h-4" />
                    <span className="text-xs font-bold uppercase">Disabled</span>
                  </div>
                )}
              </div>

              {/* Created */}
              <div className="col-span-2">
                <span className="text-sm font-medium">{formatDate(user.created_at)}</span>
              </div>

              {/* Actions */}
              <div className="col-span-2 flex items-center justify-end gap-2">
                <Link href={`/users/${user.id}`}>
                  <Button variant="secondary" size="sm" className="h-8">Details</Button>
                </Link>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setQuotaUser(user)}
                  className="h-8 w-8 p-0 border-2 border-black hover:bg-primary"
                  title="Edit quota"
                >
                  <Gauge className="w-4 h-4" />
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirm({ id: user.id, email: user.email })}
                  disabled={deleteUser.isPending}
                  className="h-8 w-8 p-0 border-2 border-black hover:bg-danger hover:text-white"
                  title="Delete"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>

      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete User"
        message={`Are you sure you want to delete "${deleteConfirm?.email}"? This action cannot be undone.`}
        loading={deleteUser.isPending}
        confirmLabel="Delete User"
        variant="destructive"
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />

      <QuotaDialog
        user={quotaUser}
        onClose={() => setQuotaUser(null)}
        onNotify={(message, type) => setToast({ message, type })}
      />

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
