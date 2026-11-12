"use client"

import { useState, useEffect, useCallback, useRef } from "react"
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
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
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

const PAGE_SIZE = 20

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" })
}

// QuotaDialog lets an admin view a user's current usage and set their resource
// limits (0 = unlimited). Fetches per-user quota lazily when opened.
function QuotaDialog({ user: openUser, onClose }: {
  user: User | null
  onClose: () => void
}) {
  // Keep the last user while the dialog fades out so the content does not vanish first.
  const lastUser = useRef(openUser)
  if (openUser) lastUser.current = openUser
  const user = openUser ?? lastUser.current
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
    { key: "max_vms", label: "Max VMs", used: status?.usage?.vms },
    { key: "max_vcpu", label: "Max vCPU", used: status?.usage?.vcpu },
    { key: "max_ram_mb", label: "Max RAM (MB)", used: status?.usage?.ram_mb },
    { key: "max_disk_gb", label: "Max Disk (GB)", used: status?.usage?.disk_gb },
  ]

  const handleSave = async () => {
    try {
      await setQuota.mutateAsync({ userId: user.id, data: form })
      toast.success(`Quota updated for ${user.email}`)
      onClose()
    } catch (err) {
      toast.error(`Failed to update quota: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open={!!openUser} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2"><Gauge className="w-5 h-5" aria-hidden="true" />Resource Quota</DialogTitle>
          <DialogDescription className="truncate">{user.email}, 0 = unlimited</DialogDescription>
        </DialogHeader>
        {isLoading ? (
          <div className="flex items-center justify-center py-10"><Loader2 className="w-6 h-6 animate-spin" /></div>
        ) : (
          <div className="space-y-4">
            {fields.map((f) => (
              <div key={f.key} className="grid grid-cols-3 items-center gap-3">
                <label htmlFor={`quota-${f.key}`} className="col-span-1 text-xs font-medium text-muted-foreground">{f.label}</label>
                <Input
                  id={`quota-${f.key}`}
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
        <DialogFooter className="gap-3 sm:gap-0 mt-2">
          <Button variant="outline" onClick={onClose} disabled={setQuota.isPending}>Cancel</Button>
          <Button onClick={handleSave} disabled={setQuota.isPending || isLoading}>
            {setQuota.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
            Save Quota
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default function UsersPage() {
  const confirm = useConfirm()
  const [searchQuery, setSearchQuery] = useState("")
  const [roleFilter, setRoleFilter] = useState<string>("")
  const [quotaUser, setQuotaUser] = useState<User | null>(null)

  // Data hooks
  const [page, setPage] = useState(1)
  const [debouncedSearch, setDebouncedSearch] = useState("")
  useEffect(() => {
    const t = setTimeout(() => { setDebouncedSearch(searchQuery.trim()); setPage(1) }, 250)
    return () => clearTimeout(t)
  }, [searchQuery])

  const { data: usersData, isLoading, isFetching, error, refetch } = useUsers({
    page,
    pageSize: PAGE_SIZE,
    search: debouncedSearch,
    role: roleFilter || undefined,
  })
  const deleteUser = useDeleteUser()

  const filteredUsers = usersData?.data ?? []
  const total = usersData?.total ?? 0
  const totalPages = Math.max(1, usersData?.totalPages ?? Math.ceil(total / PAGE_SIZE))

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
    toast.success(`User "${user.email}" deleted`)
    refetch()
  }, [confirm, deleteUser, refetch])

  const clearFilters = () => { setSearchQuery(""); setRoleFilter(""); setPage(1) }
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

  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between mb-8">
        <div>
          <h1 className="text-3xl font-bold tracking-tight text-foreground">
            Users
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            {total} {total === 1 ? "user" : "users"}
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
              placeholder="Search users by email or name"
              aria-label="Search users"
              type="search"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10"
            />
          </div>
          <select
            value={roleFilter}
            onChange={(e) => { setRoleFilter(e.target.value); setPage(1) }}
            aria-label="Filter by role"
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
      <div className={`rounded-lg border bg-card text-card-foreground shadow-sm overflow-hidden transition-opacity ${isFetching ? "opacity-60" : ""}`}>
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

      <div className="mt-4 flex items-center justify-between text-sm text-muted-foreground">
        <span>Page {page} of {totalPages}, {total} {total === 1 ? "user" : "users"}</span>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>Previous</Button>
          <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>Next</Button>
        </div>
      </div>

      <QuotaDialog
        user={quotaUser}
        onClose={() => setQuotaUser(null)}
      />
    </div>
  )
}
