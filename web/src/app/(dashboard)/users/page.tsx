"use client"

import { useState, useMemo, useEffect, useCallback } from "react"
import Link from "next/link"
import { 
  UserPlus,
  Search,
  Shield,
  Trash2,
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
import { useConfirm } from "@/components/confirm-provider"

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 px-5 py-3 rounded-lg border shadow-md ${
      type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"
    }`}>
      <p className="font-medium text-sm">{message}</p>
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
      <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-lg w-full mx-4">
        <h3 className="text-lg font-semibold mb-1 flex items-center gap-2"><Gauge className="w-5 h-5" />Resource Quota</h3>
        <p className="text-muted-foreground text-sm mb-5 truncate">{user.email} — 0 = unlimited</p>
        {isLoading ? (
          <div className="flex items-center justify-center py-10"><Loader2 className="w-6 h-6 animate-spin" /></div>
        ) : (
          <div className="space-y-4">
            {fields.map((f) => (
              <div key={f.key} className="grid grid-cols-3 items-center gap-3">
                <label className="col-span-1 text-xs font-medium text-muted-foreground">{f.label}</label>
                <Input
                  type="number"
                  min={0}
                  value={form[f.key]}
                  onChange={(e) => setForm({ ...form, [f.key]: Number(e.target.value) })}
                  className="col-span-1"
                />
                <span className="col-span-1 text-xs text-muted-foreground">used: {f.used ?? 0}</span>
              </div>
            ))}
          </div>
        )}
        <div className="flex gap-3 justify-end mt-6">
          <Button variant="outline" onClick={onClose} disabled={setQuota.isPending}>Cancel</Button>
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
  const confirm = useConfirm()
  const [searchQuery, setSearchQuery] = useState("")
  const [roleFilter, setRoleFilter] = useState<string>("")
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
  const handleDelete = useCallback(async (user: { id: string; email: string }) => {
    const ok = await confirm({
      title: `Delete user "${user.email}"?`,
      description:
        "They lose access immediately. Resources they own are not deleted with them, so check what they still hold first.",
      confirmLabel: "Delete user",
      destructive: true,
      action: () => deleteUser.mutateAsync(user.id),
    })
    if (!ok) return
    setToast({ message: `User "${user.email}" deleted`, type: "success" })
    refetch()
  }, [confirm, deleteUser, refetch])

  const clearFilters = () => { setSearchQuery(""); setRoleFilter("") }
  const hasFilters = searchQuery || roleFilter

  // Loading
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex items-center justify-between mb-8">
          <div>
            <h1 className="text-3xl font-bold tracking-tight text-foreground">Users</h1>
            <Skeleton className="h-5 w-48 mt-1" />
          </div>
        </div>
        <Skeleton className="h-16 rounded-lg mb-6" />
        <div className="space-y-4">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-16 rounded-lg" />)}
        </div>
      </div>
    )
  }

  // Error
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="rounded-lg border bg-card text-card-foreground p-12 shadow-sm text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" />
          <h2 className="text-xl font-semibold mb-2">Failed to load users</h2>
          <p className="text-muted-foreground mb-6">{(error as Error).message}</p>
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
          <h1 className="text-3xl font-bold tracking-tight text-foreground">
            Users
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
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
      <div className="rounded-lg border bg-card text-card-foreground p-4 shadow-sm mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input
              placeholder="Search users by email..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10"
            />
          </div>
          <select
            value={roleFilter}
            onChange={(e) => setRoleFilter(e.target.value)}
            className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <option value="">All Roles</option>
            <option value="admin">Admin</option>
            <option value="client">Client</option>
          </select>
          {hasFilters && (
            <Button variant="outline" onClick={clearFilters} className="gap-1"><X className="w-4 h-4" />Clear</Button>
          )}
        </div>
      </div>

      {/* Users Table */}
      <div className="rounded-lg border bg-card text-card-foreground shadow-sm overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-muted text-muted-foreground font-medium text-xs">
          <div className="col-span-4">User</div>
          <div className="col-span-2">Role</div>
          <div className="col-span-2">2FA</div>
          <div className="col-span-2">Created</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {filteredUsers.length === 0 ? (
          <div className="p-12 text-center">
            <Users className="w-12 h-12 text-muted-foreground mx-auto mb-4" />
            <p className="text-muted-foreground font-medium">No users found</p>
            {hasFilters && (
              <Button variant="outline" onClick={clearFilters} className="mt-4">Clear filters</Button>
            )}
          </div>
        ) : (
          filteredUsers.map((user) => (
            <div key={user.id} className="grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0 hover:bg-muted/50 transition-colors">
              {/* User Info */}
              <div className="col-span-4">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 rounded-md bg-muted flex items-center justify-center border">
                    <span className="text-sm font-semibold text-muted-foreground">
                      {user.email.charAt(0).toUpperCase()}
                    </span>
                  </div>
                  <div>
                    <Link
                      href={`/users/${user.id}`}
                      className="font-medium text-foreground hover:text-primary transition-colors"
                    >
                      {user.email}
                    </Link>
                    <p className="text-xs text-muted-foreground font-mono">{user.id.slice(0, 16)}...</p>
                  </div>
                </div>
              </div>

              {/* Role */}
              <div className="col-span-2">
                <Badge variant={user.role === "admin" ? "default" : "secondary"} className="gap-1 font-medium">
                  <Shield className="w-3 h-3" />
                  {user.role}
                </Badge>
              </div>

              {/* 2FA */}
              <div className="col-span-2">
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

              {/* Created */}
              <div className="col-span-2">
                <span className="text-sm text-muted-foreground">{formatDate(user.created_at)}</span>
              </div>

              {/* Actions */}
              <div className="col-span-2 flex items-center justify-end gap-2">
                <Link href={`/users/${user.id}`}>
                  <Button variant="outline" size="sm" className="h-8">Details</Button>
                </Link>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setQuotaUser(user)}
                  className="h-8 w-8 p-0"
                  title="Edit quota"
                >
                  <Gauge className="w-4 h-4" />
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleDelete({ id: user.id, email: user.email })}
                  disabled={deleteUser.isPending}
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title="Delete"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>


      <QuotaDialog
        user={quotaUser}
        onClose={() => setQuotaUser(null)}
        onNotify={(message, type) => setToast({ message, type })}
      />

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
