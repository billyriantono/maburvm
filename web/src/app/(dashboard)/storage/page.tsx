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
import { useConfirm } from "@/components/confirm-provider"
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
  const confirm = useConfirm()
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
    const ok = await confirm({
      title: `Delete volume "${volumeName}"?`,
      description:
        "Its disk image is removed from the node and the data on it is gone. This cannot be undone.",
      confirmLabel: "Delete volume",
      destructive: true,
      action: () => deleteVolume.mutateAsync(volumeId),
    })
    if (!ok) return
    toast.success(`Volume "${volumeName}" deleted`)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Manage volumes">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onClose} aria-label="Close dialog" />
      <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-2xl w-full mx-4 max-h-[85vh] overflow-y-auto">
        <h3 className="text-lg font-semibold mb-1 flex items-center gap-2"><HardDrive className="w-5 h-5" />Volumes — {pool.name}</h3>
        <p className="text-muted-foreground text-sm mb-5">{pool.path}</p>

        {/* Create form */}
        <div className="grid grid-cols-1 md:grid-cols-4 gap-3 mb-5">
          <Input placeholder="Volume name" value={name} onChange={(e) => setName(e.target.value)} className="md:col-span-2" />
          <Input type="number" min={1} value={sizeGB} onChange={(e) => setSizeGB(Number(e.target.value))} title="Size (GB)" />
          <select value={format} onChange={(e) => setFormat(e.target.value)} className="h-10 px-3 rounded-md border border-input bg-background text-sm">
            <option value="qcow2">qcow2</option>
            <option value="raw">raw</option>
          </select>
        </div>
        <Button onClick={handleCreate} disabled={createVolume.isPending} className="mb-5">
          {createVolume.isPending ? <Loader2 className="w-4 h-4 animate-spin mr-2" /> : <Plus className="w-4 h-4 mr-2" />}
          Provision Volume
        </Button>

        {/* Volume list */}
        <div className="border rounded-md overflow-hidden">
          <div className="grid grid-cols-12 gap-2 p-3 bg-muted text-muted-foreground font-medium text-[11px]">
            <div className="col-span-4">Name</div>
            <div className="col-span-2">Size</div>
            <div className="col-span-2">Format</div>
            <div className="col-span-3">Path</div>
            <div className="col-span-1 text-right">·</div>
          </div>
          {isLoading ? (
            <div className="p-6 text-center"><Loader2 className="w-6 h-6 animate-spin mx-auto" /></div>
          ) : !volumes || volumes.length === 0 ? (
            <div className="p-6 text-center text-muted-foreground font-medium text-sm">No volumes yet</div>
          ) : (
            volumes.map((v) => (
              <div key={v.id} className="grid grid-cols-12 gap-2 p-3 items-center border-t">
                <div className="col-span-4 font-medium truncate">{v.name}</div>
                <div className="col-span-2 font-mono text-xs">{formatBytes(v.size)}</div>
                <div className="col-span-2"><span className="text-[10px] font-medium border rounded-sm px-1.5 py-0.5 text-muted-foreground">{v.format}</span></div>
                <div className="col-span-3 font-mono text-[10px] truncate" title={v.path}>{v.path || "—"}</div>
                <div className="col-span-1 flex justify-end">
                  <Button variant="outline" size="sm" className="h-7 w-7 p-0 text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950" title="Delete volume" onClick={() => handleDelete(v.id, v.name)}>
                    <Trash2 className="w-3 h-3" />
                  </Button>
                </div>
              </div>
            ))
          )}
        </div>

        <div className="flex justify-end mt-6">
          <Button variant="outline" onClick={onClose}>Close</Button>
        </div>
      </div>
    </div>
  )
}

function StatusBadge({ status }: { status: string }) {
  const config: Record<string, string> = {
    online: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900",
    offline: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900",
    degraded: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
  }
  const labels: Record<string, string> = { online: "Online", offline: "Offline", degraded: "Degraded" }

  const cls = config[status] || config.online
  const label = labels[status] || labels.online

  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[10px] font-medium border rounded-full ${cls}`}>
      {label}
    </span>
  )
}

function TypeBadge({ type }: { type: string }) {
  const labels: Record<string, string> = {
    lvm: "LVM",
    lvmthin: "Thin LVM",
    zfs: "ZFS",
    zfsthin: "ZFS Thin",
    zfscompressed: "ZFS Comp",
    dir: "DIR",
    nfs: "NFS",
    ceph: "Ceph",
  }

  const label = labels[type] || type.toUpperCase()

  return (
    <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-medium border rounded-full bg-muted text-muted-foreground">
      {label}
    </span>
  )
}

// StorageBar shows how full a pool is, and — more usefully — how much is left.
//
// Free space is the number an operator acts on: "76% used" and "214 GB left"
// answer different questions, and only the second one tells you whether the
// next VM fits. The threshold comes from the pool rather than being hardcoded,
// because what counts as alarming differs between a scratch pool and the volume
// holding every customer's disk.
function StorageBar({
  used,
  total,
  available,
  threshold,
}: {
  used: number
  total: number
  available: number
  threshold?: number
}) {
  const percentage = total > 0 ? Math.round((used / total) * 100) : 0
  const alertAt = threshold && threshold > 0 ? threshold : 90
  const warnAt = Math.max(alertAt - 15, 50)

  const getColorClass = () => {
    if (percentage >= alertAt) return "bg-red-500"
    if (percentage >= warnAt) return "bg-amber-500"
    if (percentage >= 50) return "bg-blue-500"
    return "bg-emerald-500"
  }

  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-xs font-medium">
        <span className="text-muted-foreground">Usage</span>
        <span className="font-semibold">{percentage}%</span>
      </div>
      <div className="h-2 bg-muted rounded-full overflow-hidden">
        <div
          className={`h-full ${getColorClass()} transition-all duration-300`}
          style={{ width: `${percentage}%` }}
        />
      </div>
      <div className="flex items-center justify-between text-[10px] text-muted-foreground">
        <span>{formatBytes(used)} used</span>
        <span>{formatBytes(total)} total</span>
      </div>
      {percentage >= warnAt && (
        <p
          className={`text-[11px] font-medium ${
            percentage >= alertAt ? "text-red-600" : "text-amber-600"
          }`}
        >
          {formatBytes(available)} left
        </p>
      )}
    </div>
  )
}

export default function StorageListPage() {
  const confirm = useConfirm()
  const { data: pools, isLoading, error } = useStoragePools()
  const { data: nodes, isLoading: isLoadingNodes } = useNodes()
  const createPool = useCreateStoragePool()
  const resizePool = useResizeStoragePool()
  const deletePool = useDeleteStoragePool()
  const [searchQuery, setSearchQuery] = useState("")
  const [typeFilter, setTypeFilter] = useState<string>("")
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

  const handleDelete = async (pool: NonNullable<typeof pools>[number]) => {
    const ok = await confirm({
      title: `Delete storage pool "${pool.name}"?`,
      description:
        "The pool is removed from the panel. Volumes it holds are no longer manageable from here.",
      confirmLabel: "Delete pool",
      destructive: true,
      details: [
        { label: "Type", value: pool.type },
        { label: "Path", value: pool.path ?? "—" },
      ],
      action: () => deletePool.mutateAsync(pool.id),
    })
    if (!ok) return
    toast.success(`Storage pool "${pool.name}" deleted`)
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
            <h1 className="text-2xl font-semibold text-foreground flex items-center gap-3">
              <Database className="w-7 h-7" />
              Storage
            </h1>
          </div>
        </div>
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
          <span className="ml-3 font-medium text-muted-foreground">Loading storage pools...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
          <div>
            <h1 className="text-2xl font-semibold text-foreground flex items-center gap-3">
              <Database className="w-7 h-7" />
              Storage
            </h1>
          </div>
        </div>
        <div className="bg-red-50 border border-red-200 rounded-lg p-6 dark:bg-red-950 dark:border-red-900">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-red-600" />
            <div>
              <p className="font-semibold text-red-700 dark:text-red-300">Error loading storage pools</p>
              <p className="text-sm text-muted-foreground">{error.message}</p>
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
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-3">
            <Database className="w-7 h-7" />
            Storage
          </h1>
          <p className="text-muted-foreground text-sm">
            {stats.totalPools} pools
          </p>
        </div>
        <Button className="gap-2" onClick={() => setShowAddPool(true)}>
          <Plus className="w-4 h-4" />
          Add Pool
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Total Pools</span>
            <HardDrive className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-2xl font-semibold text-foreground">{stats.totalPools}</p>
        </div>

        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Total Capacity</span>
            <Database className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-2xl font-semibold text-foreground">{formatBytes(stats.totalCapacity)}</p>
        </div>

        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Used Storage</span>
            <Server className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-2xl font-semibold text-amber-600">{formatBytes(stats.usedStorage)}</p>
        </div>

        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Available Storage</span>
            <Folder className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-2xl font-semibold text-emerald-600">{formatBytes(stats.availableStorage)}</p>
        </div>
      </div>

      <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input
              type="text"
              placeholder="Search by name, path, or node..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10"
            />
          </div>

          <select
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
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
            <Button variant="outline" onClick={clearFilters} className="gap-1">
              <X className="w-4 h-4" />
              Clear
            </Button>
          )}
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {filteredPools.length === 0 ? (
          <div className="col-span-full bg-card text-card-foreground border rounded-lg p-12 shadow-sm text-center">
            <Database className="w-16 h-16 text-muted-foreground mx-auto mb-4" />
            <p className="text-muted-foreground font-medium">No storage pools found</p>
            {hasFilters && (
              <Button variant="outline" onClick={clearFilters} className="mt-4">
                Clear filters
              </Button>
            )}
          </div>
        ) : (
          filteredPools.map((pool) => (
            <div key={pool.id} className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
              <div className="p-4 border-b bg-muted/50">
                <div className="flex items-start justify-between">
                  <div>
                    <h3 className="text-lg font-semibold">{pool.name}</h3>
                    <p className="text-sm font-mono text-muted-foreground">{pool.path}</p>
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
                    <div className="w-8 h-8 bg-muted text-muted-foreground flex items-center justify-center rounded-md">
                      <Server className="w-4 h-4" />
                    </div>
                    <div>
                      <p className="text-sm font-semibold">{pool.node_id || 'N/A'}</p>
                      <p className="text-[10px] font-medium text-muted-foreground">Node</p>
                    </div>
                  </div>
                </div>

                <div className="mb-4">
                  <StorageBar
                    used={pool.used_space ?? 0}
                    total={pool.total_space ?? 0}
                    available={pool.available_space ?? 0}
                    threshold={pool.alert_threshold}
                  />
                </div>

                <div className="bg-muted rounded-md p-2 mb-4">
                  <div className="flex items-center justify-between">
                    <span className="text-[10px] font-medium text-muted-foreground">Created</span>
                    <span className="font-mono text-xs">{formatDate(pool.created_at)}</span>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    title="Manage volumes"
                    onClick={() => setVolumesPool(pool)}
                  >
                    <Settings className="w-4 h-4" />
                    <span className="ml-1">Volumes</span>
                  </Button>

                  <Button
                    variant="outline"
                    size="sm"
                    title="Resize"
                    onClick={() => { setResizeTarget(pool); setResizeBytes(pool.total_space ?? 0) }}
                  >
                    <Maximize2 className="w-4 h-4" />
                    <span className="ml-1">Resize</span>
                  </Button>

                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => handleDelete(pool)}
                    className="text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950"
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


      {/* Add Pool Dialog */}
      {showAddPool && (
        <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true">
          <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={() => setShowAddPool(false)} aria-label="Close" />
          <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-md w-full mx-4">
            <h3 className="text-lg font-semibold mb-4">Add Storage Pool</h3>
            <div className="space-y-4">
              <div>
                <label className="text-xs font-medium text-muted-foreground">Name</label>
                <Input value={newPool.name} onChange={(e) => setNewPool({...newPool, name: e.target.value})} placeholder="e.g., local-lvm" />
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">Path</label>
                <Input value={newPool.path} onChange={(e) => setNewPool({...newPool, path: e.target.value})} placeholder="e.g., /var/lib/vz" />
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">Type</label>
                <select value={newPool.type} onChange={(e) => setNewPool({...newPool, type: e.target.value})} className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
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
                <label className="text-xs font-medium text-muted-foreground">Node</label>
                <select value={newPool.node_id} onChange={(e) => setNewPool({...newPool, node_id: e.target.value})} className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
                  <option value="">Select a node</option>
                  {nodes?.map((node) => (
                    <option key={node.id} value={node.id}>{node.name}</option>
                  ))}
                </select>
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">Size (GB)</label>
                <Input type="number" value={Math.round(newPool.total_space / 1073741824)} onChange={(e) => setNewPool({...newPool, total_space: parseInt(e.target.value) * 1073741824})} />
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">File Format</label>
                <select value={newPool.file_format} onChange={(e) => setNewPool({...newPool, file_format: e.target.value})} className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
                  <option value="raw">RAW</option>
                  <option value="qcow2">QCOW2</option>
                </select>
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">Alert Threshold (%)</label>
                <Input type="number" min={0} max={100} value={newPool.alert_threshold} onChange={(e) => setNewPool({...newPool, alert_threshold: parseInt(e.target.value) || 0})} />
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">Overcommit (GB, 0 = disabled)</label>
                <Input type="number" min={0} value={Math.round(newPool.overcommit / 1073741824)} onChange={(e) => setNewPool({...newPool, overcommit: parseInt(e.target.value) * 1073741824})} />
              </div>
              <div className="flex items-center gap-3">
                <input type="checkbox" id="is_primary" checked={newPool.is_primary} onChange={(e) => setNewPool({...newPool, is_primary: e.target.checked})} className="w-4 h-4 rounded border-input" />
                <label htmlFor="is_primary" className="text-xs font-medium text-muted-foreground">Primary Storage</label>
              </div>
            </div>
            <div className="flex gap-3 justify-end mt-6">
              <Button variant="outline" onClick={() => setShowAddPool(false)}>Cancel</Button>
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
          <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-md w-full mx-4">
            <h3 className="text-lg font-semibold mb-4">Resize Pool: {resizeTarget.name}</h3>
            <div className="space-y-4">
              <div>
                <label className="text-xs font-medium text-muted-foreground">Current Size</label>
                <p className="font-semibold">{formatBytes(resizeTarget.total_space ?? 0)}</p>
              </div>
              <div>
                <label className="text-xs font-medium text-muted-foreground">New Size (GB)</label>
                <Input type="number" value={Math.round(resizeBytes / 1073741824)} onChange={(e) => setResizeBytes(parseInt(e.target.value) * 1073741824)} />
              </div>
            </div>
            <div className="flex gap-3 justify-end mt-6">
              <Button variant="outline" onClick={() => setResizeTarget(null)}>Cancel</Button>
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
