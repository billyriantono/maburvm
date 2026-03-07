"use client"

import { useState } from "react"
import {
  Database,
  Plus,
  Search,
  X,
  Settings,
  Maximize2,
  Trash2,
  Loader2,
  HardDrive,
  Server,
  Folder,
  AlertCircle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useStoragePools } from "@/lib/hooks/use-storage"
import { toast } from "sonner"

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB", "PB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i]
}

function formatDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString("en-US", { 
    year: "numeric", 
    month: "short", 
    day: "numeric" 
  })
}

function StatusBadge({ status }: { status: string }) {
  const config = {
    online: { bg: "bg-success", text: "text-black", label: "Online" },
    offline: { bg: "bg-danger", text: "text-white", label: "Offline" },
    degraded: { bg: "bg-warning", text: "text-black", label: "Degraded" },
  }

  const { bg, text, label } = config[status as keyof typeof config] || config.online

  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black ${bg} ${text}`}>
      {label}
    </span>
  )
}

function TypeBadge({ type }: { type: string }) {
  const config: Record<string, { bg: string; text: string; label: string }> = {
    lvm: { bg: "bg-primary", text: "text-black", label: "LVM" },
    zfs: { bg: "bg-secondary", text: "text-black", label: "ZFS" },
    dir: { bg: "bg-muted", text: "text-gray-700", label: "DIR" },
    nfs: { bg: "bg-accent", text: "text-white", label: "NFS" },
    ceph: { bg: "bg-primary", text: "text-black", label: "Ceph" },
  }

  const { bg, text, label } = config[type] || config.dir

  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black ${bg} ${text}`}>
      {label}
    </span>
  )
}

function StorageBar({ used, total }: { used: number; total: number }) {
  const percentage = total > 0 ? Math.round((used / total) * 100) : 0

  const getColorClass = () => {
    if (percentage >= 90) return "bg-danger"
    if (percentage >= 75) return "bg-warning"
    if (percentage >= 50) return "bg-secondary"
    return "bg-success"
  }

  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-xs font-medium">
        <span className="text-gray-500 uppercase">Usage</span>
        <span className="font-black">{percentage}%</span>
      </div>
      <div className="h-3 bg-gray-200 border border-black">
        <div
          className={`h-full ${getColorClass()} transition-all duration-300`}
          style={{ width: `${percentage}%` }}
        />
      </div>
      <div className="flex items-center justify-between text-[10px] text-gray-500">
        <span>{formatBytes(used)} used</span>
        <span>{formatBytes(total)} total</span>
      </div>
    </div>
  )
}

function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = "Confirm",
  onConfirm,
  onCancel
}: {
  open: boolean
  title: string
  message: string
  confirmLabel?: string
  onConfirm: () => void
  onCancel: () => void
}) {
  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Confirm dialog">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onCancel} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
        <h3 className="text-xl font-black uppercase mb-4">{title}</h3>
        <p className="text-gray-600 font-medium mb-6">{message}</p>
        <div className="flex gap-3 justify-end">
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black">
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}

export default function StorageListPage() {
  const { data: pools, isLoading, error } = useStoragePools()
  const [searchQuery, setSearchQuery] = useState("")
  const [typeFilter, setTypeFilter] = useState<string>("")
  const [deleteConfirm, setDeleteConfirm] = useState<NonNullable<typeof pools>[number] | null>(null)

  const filteredPools = pools?.filter(pool =>
    pool.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
    pool.path.toLowerCase().includes(searchQuery.toLowerCase()) ||
    pool.node_id?.toLowerCase().includes(searchQuery.toLowerCase())
  ).filter(pool => !typeFilter || pool.type === typeFilter) ?? []

  const hasFilters = searchQuery || typeFilter

  const clearFilters = () => {
    setSearchQuery("")
    setTypeFilter("")
  }

  const stats = {
    totalPools: pools?.length ?? 0,
    totalCapacity: pools?.reduce((sum, p) => sum + (p.total_space ?? 0), 0) ?? 0,
    usedStorage: pools?.reduce((sum, p) => sum + (p.used_space ?? 0), 0) ?? 0,
    availableStorage: pools?.reduce((sum, p) => sum + (p.available_space ?? 0), 0) ?? 0,
  }

  const handleDelete = () => {
    if (!deleteConfirm) return
    toast.success(`Storage pool ${deleteConfirm.name} deleted`)
    setDeleteConfirm(null)
  }

  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
              <Database className="w-8 h-8" />
              Storage
            </h1>
          </div>
        </div>
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
          <span className="ml-3 font-bold uppercase">Loading storage pools...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
              <Database className="w-8 h-8" />
              Storage
            </h1>
          </div>
        </div>
        <div className="bg-danger/10 border-4 border-danger p-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-danger" />
            <div>
              <p className="font-black uppercase">Error loading storage pools</p>
              <p className="text-sm font-medium">{error.message}</p>
            </div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
            <Database className="w-8 h-8" />
            Storage
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
            {stats.totalPools} pools
          </p>
        </div>
        <Button className="gap-2" onClick={() => toast.info("Add pool coming soon")}>
          <Plus className="w-4 h-4" />
          Add Pool
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total Pools</span>
            <HardDrive className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{stats.totalPools}</p>
        </div>

        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total Capacity</span>
            <Database className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{formatBytes(stats.totalCapacity)}</p>
        </div>

        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Used Storage</span>
            <Server className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-warning">{formatBytes(stats.usedStorage)}</p>
        </div>

        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Available Storage</span>
            <Folder className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-success">{formatBytes(stats.availableStorage)}</p>
        </div>
      </div>

      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input
              type="text"
              placeholder="Search by name, path, or node..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10 border-2 border-black"
            />
          </div>

          <select
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Types</option>
            <option value="lvm">LVM</option>
            <option value="zfs">ZFS</option>
            <option value="dir">Directory</option>
            <option value="nfs">NFS</option>
            <option value="ceph">Ceph</option>
          </select>

          {hasFilters && (
            <Button variant="ghost" onClick={clearFilters} className="border-2 border-black gap-1">
              <X className="w-4 h-4" />
              Clear
            </Button>
          )}
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {filteredPools.length === 0 ? (
          <div className="col-span-full bg-white border-4 border-black p-12 shadow-neo text-center">
            <Database className="w-16 h-16 text-gray-300 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase">No storage pools found</p>
            {hasFilters && (
              <Button variant="ghost" onClick={clearFilters} className="mt-4 border-2 border-black">
                Clear filters
              </Button>
            )}
          </div>
        ) : (
          filteredPools.map((pool) => (
            <div key={pool.id} className="bg-white border-4 border-black shadow-neo">
              <div className="p-4 border-b-4 border-black bg-gray-50">
                <div className="flex items-start justify-between">
                  <div>
                    <h3 className="text-xl font-black uppercase">{pool.name}</h3>
                    <p className="text-sm font-mono font-medium text-gray-500">{pool.path}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    <StatusBadge status={pool.status === 'online' ? 'online' : pool.status === 'offline' ? 'offline' : 'degraded'} />
                    <TypeBadge type={pool.type} />
                  </div>
                </div>
              </div>

              <div className="p-4">
                <div className="flex items-center gap-4 mb-4">
                  <div className="flex items-center gap-2">
                    <div className="w-8 h-8 bg-primary flex items-center justify-center border-2 border-black">
                      <Server className="w-4 h-4" />
                    </div>
                    <div>
                      <p className="text-lg font-black">{pool.node_id || 'N/A'}</p>
                      <p className="text-[10px] font-bold text-gray-500 uppercase">Node</p>
                    </div>
                  </div>
                </div>

                <div className="mb-4">
                  <StorageBar used={pool.used_space ?? 0} total={pool.total_space ?? 0} />
                </div>

                <div className="bg-gray-100 border-2 border-black p-2 mb-4">
                  <div className="flex items-center justify-between">
                    <span className="text-[10px] font-bold text-gray-500 uppercase">Created</span>
                    <span className="font-mono text-xs">{formatDate(pool.created_at)}</span>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-9 border-2 border-black"
                    title="Manage"
                  >
                    <Settings className="w-4 h-4" />
                    <span className="ml-1">Manage</span>
                  </Button>

                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-9 border-2 border-black"
                    title="Resize"
                    onClick={() => toast.info("Resize coming soon")}
                  >
                    <Maximize2 className="w-4 h-4" />
                    <span className="ml-1">Resize</span>
                  </Button>

                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setDeleteConfirm(pool)}
                    className="h-9 border-2 border-black hover:bg-danger hover:text-white"
                    title="Delete"
                  >
                    <Trash2 className="w-4 h-4" />
                  </Button>
                </div>
              </div>
            </div>
          ))
        )}
      </div>

      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete Storage Pool"
        message={`Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />
    </div>
  )
}