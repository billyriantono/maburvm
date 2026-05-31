"use client"

import { useState, useCallback } from "react"
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
import {
  useStoragePools,
  useCreateStoragePool,
  useResizeStoragePool,
  useDeleteStoragePool,
  useStorageVolumes,
  useCreateStorageVolume,
  useDeleteStorageVolume,
} from "@/lib/hooks/use-storage"
import { useNodes } from "@/lib/hooks/use-nodes"
import type { StoragePool } from "@/types"
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

const GIB = 1024 * 1024 * 1024

// VolumesDialog lists, provisions, and deletes real disk volumes in a pool.
function VolumesDialog({ pool, onClose }: { pool: StoragePool | null; onClose: () => void }) {
  const poolId = pool?.id ?? ""
  const { data: volumes, isLoading } = useStorageVolumes(poolId)
  const createVolume = useCreateStorageVolume(poolId)
  const deleteVolume = useDeleteStorageVolume(poolId)
  const [name, setName] = useState("")
  const [sizeGB, setSizeGB] = useState(10)
  const [format, setFormat] = useState("qcow2")

  if (!pool) return null

  const handleCreate = async () => {
    const trimmed = name.trim()
    if (!trimmed) { toast.error("Volume name is required"); return }
    if (sizeGB <= 0) { toast.error("Size must be at least 1 GB"); return }
    try {
      await createVolume.mutateAsync({ name: trimmed, pool_id: pool.id, size: sizeGB * GIB, format })
      toast.success(`Volume "${trimmed}" provisioned`)
      setName("")
    } catch (err) {
      toast.error(`Failed to create volume: ${(err as Error).message}`)
    }
  }

  const handleDelete = async (volumeId: string, volumeName: string) => {
    if (!window.confirm(`Delete volume "${volumeName}"? Its disk image will be removed from the node.`)) return
    try {
      await deleteVolume.mutateAsync(volumeId)
      toast.success(`Volume "${volumeName}" deleted`)
    } catch (err) {
      toast.error(`Failed to delete volume: ${(err as Error).message}`)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Manage volumes">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onClose} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-2xl w-full mx-4 max-h-[85vh] overflow-y-auto">
        <h3 className="text-xl font-black uppercase mb-1 flex items-center gap-2"><HardDrive className="w-5 h-5" />Volumes — {pool.name}</h3>
        <p className="text-gray-500 font-medium text-sm mb-5">{pool.path}</p>

        {/* Create form */}
        <div className="grid grid-cols-1 md:grid-cols-4 gap-3 mb-5">
          <Input placeholder="Volume name" value={name} onChange={(e) => setName(e.target.value)} className="border-2 border-black md:col-span-2" />
          <Input type="number" min={1} value={sizeGB} onChange={(e) => setSizeGB(Number(e.target.value))} className="border-2 border-black" title="Size (GB)" />
          <select value={format} onChange={(e) => setFormat(e.target.value)} className="h-12 px-3 border-2 border-black font-medium bg-white">
            <option value="qcow2">qcow2</option>
            <option value="raw">raw</option>
          </select>
        </div>
        <Button onClick={handleCreate} disabled={createVolume.isPending} className="mb-5">
          {createVolume.isPending ? <Loader2 className="w-4 h-4 animate-spin mr-2" /> : <Plus className="w-4 h-4 mr-2" />}
          Provision Volume
        </Button>

        {/* Volume list */}
        <div className="border-2 border-black">
          <div className="grid grid-cols-12 gap-2 p-3 bg-black text-white font-black uppercase text-[10px] tracking-wider">
            <div className="col-span-4">Name</div>
            <div className="col-span-2">Size</div>
            <div className="col-span-2">Format</div>
            <div className="col-span-3">Path</div>
            <div className="col-span-1 text-right">·</div>
          </div>
          {isLoading ? (
            <div className="p-6 text-center"><Loader2 className="w-6 h-6 animate-spin mx-auto" /></div>
          ) : !volumes || volumes.length === 0 ? (
            <div className="p-6 text-center text-gray-500 font-bold uppercase text-sm">No volumes yet</div>
          ) : (
            volumes.map((v, i) => (
              <div key={v.id} className={`grid grid-cols-12 gap-2 p-3 items-center border-t-2 border-black ${i % 2 ? "bg-gray-50" : "bg-white"}`}>
                <div className="col-span-4 font-bold truncate">{v.name}</div>
                <div className="col-span-2 font-mono text-xs">{formatBytes(v.size)}</div>
                <div className="col-span-2"><span className="text-[10px] font-black uppercase border border-black px-1.5 py-0.5">{v.format}</span></div>
                <div className="col-span-3 font-mono text-[10px] truncate" title={v.path}>{v.path || "—"}</div>
                <div className="col-span-1 flex justify-end">
                  <Button variant="ghost" size="sm" className="h-7 w-7 p-0 border-2 border-black hover:bg-danger hover:text-white" title="Delete volume" onClick={() => handleDelete(v.id, v.name)}>
                    <Trash2 className="w-3 h-3" />
                  </Button>
                </div>
              </div>
            ))
          )}
        </div>

        <div className="flex justify-end mt-6">
          <Button variant="ghost" onClick={onClose} className="border-2 border-black">Close</Button>
        </div>
      </div>
    </div>
  )
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
    lvmthin: { bg: "bg-primary", text: "text-black", label: "Thin LVM" },
    zfs: { bg: "bg-secondary", text: "text-black", label: "ZFS" },
    zfsthin: { bg: "bg-secondary", text: "text-black", label: "ZFS Thin" },
    zfscompressed: { bg: "bg-secondary", text: "text-black", label: "ZFS Comp" },
    dir: { bg: "bg-muted", text: "text-gray-700", label: "DIR" },
    nfs: { bg: "bg-accent", text: "text-white", label: "NFS" },
    ceph: { bg: "bg-primary", text: "text-black", label: "Ceph" },
  }

  const { bg, text, label } = config[type] || { bg: "bg-muted", text: "text-gray-700", label: type.toUpperCase() }

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
  const { data: nodes, isLoading: isLoadingNodes } = useNodes()
  const createPool = useCreateStoragePool()
  const resizePool = useResizeStoragePool()
  const deletePool = useDeleteStoragePool()
  const [searchQuery, setSearchQuery] = useState("")
  const [typeFilter, setTypeFilter] = useState<string>("")
  const [deleteConfirm, setDeleteConfirm] = useState<NonNullable<typeof pools>[number] | null>(null)
  const [showAddPool, setShowAddPool] = useState(false)
  const [resizeTarget, setResizeTarget] = useState<NonNullable<typeof pools>[number] | null>(null)
  const [newPool, setNewPool] = useState({ name: "", path: "", type: "dir", node_id: "", total_space: 107374182400, file_format: "raw", alert_threshold: 90, overcommit: 0, is_primary: false })
  const [resizeBytes, setResizeBytes] = useState(0)
  const [volumesPool, setVolumesPool] = useState<StoragePool | null>(null)

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
    deletePool.mutate(deleteConfirm.id, {
      onSuccess: () => {
        toast.success(`Storage pool "${deleteConfirm.name}" deleted`)
        setDeleteConfirm(null)
      },
      onError: (err) => toast.error(`Failed to delete: ${err.message}`)
    })
  }

  const handleAddPool = () => {
    createPool.mutate(newPool, {
      onSuccess: () => {
        toast.success(`Storage pool "${newPool.name}" created`)
        setShowAddPool(false)
        setNewPool({ name: "", path: "", type: "dir", node_id: "", total_space: 107374182400, file_format: "raw", alert_threshold: 90, overcommit: 0, is_primary: false })
      },
      onError: (err) => toast.error(`Failed to create pool: ${err.message}`)
    })
  }

  const handleResize = () => {
    if (!resizeTarget) return
    resizePool.mutate({ id: resizeTarget.id, total_space: resizeBytes }, {
      onSuccess: () => {
        toast.success(`Storage pool "${resizeTarget.name}" resized`)
        setResizeTarget(null)
      },
      onError: (err) => toast.error(`Failed to resize: ${err.message}`)
    })
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
        <Button className="gap-2" onClick={() => setShowAddPool(true)}>
          <Plus className="w-4 h-4" />
          Add Pool
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total Pools</span>
            <HardDrive className="w-4 h-4 text-gray-600" />
          </div>
          <p className="text-3xl font-black text-black">{stats.totalPools}</p>
        </div>

        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total Capacity</span>
            <Database className="w-4 h-4 text-gray-600" />
          </div>
          <p className="text-3xl font-black text-black">{formatBytes(stats.totalCapacity)}</p>
        </div>

        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Used Storage</span>
            <Server className="w-4 h-4 text-gray-600" />
          </div>
          <p className="text-3xl font-black text-warning">{formatBytes(stats.usedStorage)}</p>
        </div>

        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Available Storage</span>
            <Folder className="w-4 h-4 text-gray-600" />
          </div>
          <p className="text-3xl font-black text-success">{formatBytes(stats.availableStorage)}</p>
        </div>
      </div>

      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
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
            <option value="dir">Directory (File)</option>
            <option value="lvm">LVM</option>
            <option value="lvmthin">Thin LVM</option>
            <option value="zfs">ZFS</option>
            <option value="zfsthin">ZFS Thin</option>
            <option value="zfscompressed">ZFS Compressed</option>
            <option value="ceph">Ceph Block Device</option>
            <option value="nfs">NFS</option>
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
            <Database className="w-16 h-16 text-gray-500 mx-auto mb-4" />
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
                    title="Manage volumes"
                    onClick={() => setVolumesPool(pool)}
                  >
                    <Settings className="w-4 h-4" />
                    <span className="ml-1">Volumes</span>
                  </Button>

                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-9 border-2 border-black"
                    title="Resize"
                    onClick={() => { setResizeTarget(pool); setResizeBytes(pool.total_space ?? 0) }}
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

      {/* Add Pool Dialog */}
      {showAddPool && (
        <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true">
          <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={() => setShowAddPool(false)} aria-label="Close" />
          <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
            <h3 className="text-xl font-black uppercase mb-4">Add Storage Pool</h3>
            <div className="space-y-4">
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Name</label>
                <Input value={newPool.name} onChange={(e) => setNewPool({...newPool, name: e.target.value})} placeholder="e.g., local-lvm" className="border-2 border-black" />
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Path</label>
                <Input value={newPool.path} onChange={(e) => setNewPool({...newPool, path: e.target.value})} placeholder="e.g., /var/lib/vz" className="border-2 border-black" />
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Type</label>
                <select value={newPool.type} onChange={(e) => setNewPool({...newPool, type: e.target.value})} className="w-full h-10 px-3 border-2 border-black font-medium bg-white">
                  <option value="dir">Directory (File)</option>
                  <option value="lvm">LVM</option>
                  <option value="lvmthin">Thin LVM</option>
                  <option value="zfs">ZFS</option>
                  <option value="zfsthin">ZFS Thin</option>
                  <option value="zfscompressed">ZFS Compressed</option>
                  <option value="ceph">Ceph Block Device</option>
                  <option value="nfs">NFS</option>
                </select>
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Node</label>
                <select value={newPool.node_id} onChange={(e) => setNewPool({...newPool, node_id: e.target.value})} className="w-full h-10 px-3 border-2 border-black font-medium bg-white">
                  <option value="">Select a node</option>
                  {nodes?.map((node) => (
                    <option key={node.id} value={node.id}>{node.name}</option>
                  ))}
                </select>
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Size (GB)</label>
                <Input type="number" value={Math.round(newPool.total_space / 1073741824)} onChange={(e) => setNewPool({...newPool, total_space: parseInt(e.target.value) * 1073741824})} className="border-2 border-black" />
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">File Format</label>
                <select value={newPool.file_format} onChange={(e) => setNewPool({...newPool, file_format: e.target.value})} className="w-full h-10 px-3 border-2 border-black font-medium bg-white">
                  <option value="raw">RAW</option>
                  <option value="qcow2">QCOW2</option>
                </select>
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Alert Threshold (%)</label>
                <Input type="number" min={0} max={100} value={newPool.alert_threshold} onChange={(e) => setNewPool({...newPool, alert_threshold: parseInt(e.target.value) || 0})} className="border-2 border-black" />
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Overcommit (GB, 0 = disabled)</label>
                <Input type="number" min={0} value={Math.round(newPool.overcommit / 1073741824)} onChange={(e) => setNewPool({...newPool, overcommit: parseInt(e.target.value) * 1073741824})} className="border-2 border-black" />
              </div>
              <div className="flex items-center gap-3">
                <input type="checkbox" id="is_primary" checked={newPool.is_primary} onChange={(e) => setNewPool({...newPool, is_primary: e.target.checked})} className="w-5 h-5 border-2 border-black" />
                <label htmlFor="is_primary" className="text-xs font-black uppercase text-gray-500">Primary Storage</label>
              </div>
            </div>
            <div className="flex gap-3 justify-end mt-6">
              <Button variant="ghost" onClick={() => setShowAddPool(false)} className="border-2 border-black">Cancel</Button>
              <Button onClick={handleAddPool} disabled={!newPool.name || !newPool.path || !newPool.node_id || isLoadingNodes || createPool.isPending}>
                {createPool.isPending ? "Creating..." : "Create Pool"}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* Resize Dialog */}
      {resizeTarget && (
        <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true">
          <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={() => setResizeTarget(null)} aria-label="Close" />
          <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
            <h3 className="text-xl font-black uppercase mb-4">Resize Pool: {resizeTarget.name}</h3>
            <div className="space-y-4">
              <div>
                <label className="text-xs font-black uppercase text-gray-500">Current Size</label>
                <p className="font-bold">{formatBytes(resizeTarget.total_space ?? 0)}</p>
              </div>
              <div>
                <label className="text-xs font-black uppercase text-gray-500">New Size (GB)</label>
                <Input type="number" value={Math.round(resizeBytes / 1073741824)} onChange={(e) => setResizeBytes(parseInt(e.target.value) * 1073741824)} className="border-2 border-black" />
              </div>
            </div>
            <div className="flex gap-3 justify-end mt-6">
              <Button variant="ghost" onClick={() => setResizeTarget(null)} className="border-2 border-black">Cancel</Button>
              <Button onClick={handleResize} disabled={resizePool.isPending}>
                {resizePool.isPending ? "Resizing..." : "Resize"}
              </Button>
            </div>
          </div>
        </div>
      )}

      <VolumesDialog pool={volumesPool} onClose={() => setVolumesPool(null)} />
    </div>
  )
}