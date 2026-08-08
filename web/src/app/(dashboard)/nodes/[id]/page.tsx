"use client"

import { useState, useEffect, useCallback } from "react"
import Link from "next/link"
import { useParams } from "next/navigation"
import {
  Server,
  ArrowLeft,
  Play,
  Square,
  RotateCcw,
  Terminal,
  Edit2,
  RefreshCw,
  HardDrive,
  Cpu,
  Clock,
  AlertTriangle,
  XCircle,
  Database,
  MemoryStick,
  Loader2,
  Copy,
  Check,
  Search,
  FolderOpen,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { useNode, useUpdateNode, useNodeMetrics, useNodeToken, useRegenerateNodeToken, useScanVMs, useSyncNodeVMs, useImportVMs } from "@/lib/hooks/use-nodes"
import { useVMs } from "@/lib/hooks/use-vms"
import { useVMActions } from "@/lib/hooks/use-vms"
import { useUsers } from "@/lib/hooks/use-users"
import { useTemplates } from "@/lib/hooks/use-templates"
import type { NodeStatus, VMStatus } from "@/types"

// Status indicator component
function StatusIndicator({ status }: { status: NodeStatus }) {
  const config: Record<string, { bg: string; text: string; label: string }> = {
    active: { bg: "bg-emerald-500", text: "text-emerald-600", label: "Active" },
    offline: { bg: "bg-red-500", text: "text-red-600", label: "Offline" },
    maintenance: { bg: "bg-amber-500", text: "text-amber-600", label: "Maintenance" },
  }

  const { bg, text, label } = config[status] || config.offline

  return (
    <div className="flex items-center gap-2">
      <span className={`w-2.5 h-2.5 rounded-full ${bg} ${status === "active" ? "animate-pulse" : ""}`} />
      <span className={`text-sm font-medium ${text}`}>{label}</span>
    </div>
  )
}

// VM Status badge
function VMStatusBadge({ status }: { status: VMStatus }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900",
    stopped: "bg-muted text-muted-foreground",
    suspended: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
    creating: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-300 dark:border-blue-900",
    deleting: "bg-orange-50 text-orange-700 border-orange-200 dark:bg-orange-950 dark:text-orange-300 dark:border-orange-900",
    error: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900",
  }

  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[10px] font-medium rounded-full border ${colors[status] || colors.stopped}`}>
      <span className={`w-1.5 h-1.5 mr-1.5 rounded-full ${status === "running" ? "bg-emerald-500 animate-pulse" : "bg-current"}`} />
      {status}
    </span>
  )
}

// Resource gauge component
function ResourceGauge({ value, label, detail, icon: Icon, color }: { value: number; label: string; detail?: string; icon: React.ElementType; color: string }) {
  const getColorClass = () => {
    if (value >= 90) return "bg-red-500"
    if (value >= 70) return "bg-amber-500"
    return color
  }

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 bg-muted text-muted-foreground flex items-center justify-center rounded-md">
            <Icon className="w-4 h-4" />
          </div>
          <div>
            <span className="text-xs font-medium text-muted-foreground">{label}</span>
            {detail && <span className="text-[10px] text-muted-foreground block">{detail}</span>}
          </div>
        </div>
        <span className="text-xl font-semibold">{value}%</span>
      </div>
      <div className="h-2 bg-muted rounded-full overflow-hidden">
        <div
          className={`h-full ${getColorClass()} transition-all duration-500`}
          style={{ width: `${Math.min(value, 100)}%` }}
        />
      </div>
    </div>
  )
}

function formatDate(dateString: string) {
  return new Date(dateString).toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

function formatBytesPerSec(bytes: number): string {
  return `${formatBytes(bytes)}/s`
}

// Toast notification
function Toast({ message, type, onClose }: { message: string, type: "success" | "error", onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 px-4 py-3 rounded-lg border shadow-md ${
      type === "success"
        ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900"
        : "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900"
    }`}>
      <p className="font-medium text-sm">{message}</p>
    </div>
  )
}

// conntrackTone colours the connection-tracking bar. The thresholds are low on
// purpose: unlike CPU, a full table is not slow — it is an outage for every
// tenant on the node, so the warning has to arrive with room to act.
function conntrackTone(count: number, max: number): string {
  const used = max > 0 ? count / max : 0
  if (used >= 0.9) return "bg-red-500"
  if (used >= 0.7) return "bg-amber-500"
  return "bg-emerald-500"
}

export default function NodeDetailPage() {
  const params = useParams()
  const nodeId = params.id as string

  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [copied, setCopied] = useState(false)
  const [copiedToken, setCopiedToken] = useState(false)
  const [showToken, setShowToken] = useState(false)
  const [editDialogOpen, setEditDialogOpen] = useState(false)
  const [editName, setEditName] = useState("")
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [importDialogOpen, setImportDialogOpen] = useState(false)
  const [importVMUUID, setImportVMUUID] = useState<string>("")
  const [importUserId, setImportUserId] = useState("")
  const [importTemplateId, setImportTemplateId] = useState("")

  // Data hooks
  const { data: node, isLoading, error, refetch } = useNode(nodeId)
  const { data: metrics } = useNodeMetrics(nodeId)
  const { data: tokenData, refetch: refetchToken } = useNodeToken(nodeId)
  const regenerateToken = useRegenerateNodeToken(nodeId)
  const { data: scanData, refetch: refetchScan, isLoading: scanLoading, error: scanError } = useScanVMs(nodeId)
  const syncVMs = useSyncNodeVMs(nodeId)
  const importVMs = useImportVMs(nodeId)
  const { data: vmsData, isLoading: vmsLoading, refetch: refetchVMs } = useVMs({ nodeId, pageSize: 100 })
  const { data: usersData, error: usersError } = useUsers()
  const { data: templatesData, error: templatesError } = useTemplates()
  const updateNode = useUpdateNode(nodeId)
  const vmActions = useVMActions()

  const vms = vmsData?.data || []
  const users: any[] = Array.isArray(usersData) ? usersData : (usersData as any)?.data || []
  const templates: any[] = Array.isArray(templatesData) ? templatesData : (templatesData as any)?.data || []

  // VM action handler
  const handleVMAction = useCallback(async (vmId: string, action: string) => {
    setActionLoading(`${vmId}-${action}`)
    try {
      await vmActions.mutateAsync({ vmId, action })
      setToast({ message: `VM ${action} successful`, type: "success" })
      refetchVMs()
    } catch (err) {
      setToast({ message: `Failed to ${action} VM: ${(err as Error).message}`, type: "error" })
    } finally {
      setActionLoading(null)
    }
  }, [vmActions, refetchVMs])

  // Edit handler
  const handleEdit = useCallback(async () => {
    if (!editName.trim()) return
    try {
      await updateNode.mutateAsync({ name: editName })
      setToast({ message: "Node updated successfully", type: "success" })
      setEditDialogOpen(false)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to update node: ${(err as Error).message}`, type: "error" })
    }
  }, [updateNode, editName, refetch])

  // Copy to clipboard
  const copyToClipboard = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setToast({ message: "Copied to clipboard", type: "success" })
      setTimeout(() => setCopied(false), 2000)
    } catch {
      setCopied(false)
    }
  }

  // Loading state
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex items-center gap-4 mb-6">
          <Link href="/nodes">
            <Button variant="outline" size="icon">
              <ArrowLeft className="w-4 h-4" />
            </Button>
          </Link>
          <div className="flex-1">
            <Skeleton className="h-8 w-48 mb-2" />
            <Skeleton className="h-5 w-32" />
          </div>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-24 rounded-lg" />)}
        </div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6">
          {[1,2,3].map(i => <Skeleton key={i} className="h-32 rounded-lg" />)}
        </div>
      </div>
    )
  }

  // Error / not found
  if (error || !node) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-card text-card-foreground border rounded-lg p-12 shadow-sm text-center">
          <Server className="w-16 h-16 text-muted-foreground mx-auto mb-4" />
          <h2 className="text-lg font-semibold mb-2">Node Not Found</h2>
          <p className="text-muted-foreground text-sm mb-6">{(error as Error)?.message || "The requested node does not exist."}</p>
          <Link href="/nodes">
            <Button className="gap-2">
              <ArrowLeft className="w-4 h-4" />
              Back to Nodes
            </Button>
          </Link>
        </div>
      </div>
    )
  }

  const runningVMs = vms.filter(vm => vm.status === "running").length
  const stoppedVMs = vms.length - runningVMs

  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/nodes">
          <Button variant="outline" size="icon">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold text-foreground">
              {node.name}
            </h1>
            <StatusIndicator status={node.status} />
          </div>
          <p className="text-muted-foreground text-sm">
            {node.ip_address} • Created {formatDate(node.created_at)}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Dialog open={editDialogOpen} onOpenChange={(open) => { setEditDialogOpen(open); if (open) setEditName(node.name) }}>
            <DialogTrigger asChild>
              <Button variant="outline" className="gap-2">
                <Edit2 className="w-4 h-4" />
                Edit
              </Button>
            </DialogTrigger>
            <DialogContent className="max-w-md">
              <DialogHeader>
                <DialogTitle className="text-lg font-semibold">Edit Node</DialogTitle>
                <DialogDescription>Update node name</DialogDescription>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div>
                  <label htmlFor="edit-name" className="block text-xs font-medium text-muted-foreground mb-1">Name</label>
                  <Input id="edit-name" value={editName} onChange={(e) => setEditName(e.target.value)} />
                </div>
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={() => setEditDialogOpen(false)}>Cancel</Button>
                <Button onClick={handleEdit} disabled={updateNode.isPending || !editName.trim()}>
                  {updateNode.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
                  Save Changes
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <Tabs defaultValue="overview">
        <TabsList className="mb-6">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="import">Import VMs</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          {/* Quick Stats */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
            <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-medium text-muted-foreground">Total VMs</span>
                <Database className="w-4 h-4 text-muted-foreground" />
              </div>
              <p className="text-2xl font-semibold text-foreground">{metrics?.running_vm_count ?? vms.length}</p>
            </div>

            <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-medium text-muted-foreground">Running</span>
                <span className="w-2.5 h-2.5 bg-emerald-500 rounded-full animate-pulse" />
              </div>
              <p className="text-2xl font-semibold text-emerald-600">{runningVMs}</p>
            </div>

            <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-medium text-muted-foreground">Stopped</span>
                <span className="w-2.5 h-2.5 bg-red-500 rounded-full" />
              </div>
              <p className="text-2xl font-semibold text-red-600">{stoppedVMs}</p>
            </div>

            <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-medium text-muted-foreground">Status</span>
                <Clock className="w-4 h-4 text-muted-foreground" />
              </div>
              <StatusIndicator status={node.status} />
            </div>
          </div>

          {/* Resource Usage from NodeMetrics */}
          {metrics && node.status === "active" && (
            <>
              <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6">
                <ResourceGauge
                  value={Math.round(metrics.cpu_percent)}
                  label="CPU Usage"
                  detail={`${metrics.available_cpus} cores available`}
                  icon={Cpu}
                  color="bg-blue-500"
                />
                <ResourceGauge
                  value={Math.round(metrics.memory_used_percent)}
                  label="Memory Usage"
                  detail={`${metrics.available_memory_mb} MB available`}
                  icon={MemoryStick}
                  color="bg-violet-500"
                />
                <ResourceGauge
                  value={Math.round(metrics.disk_used_percent)}
                  label="Disk Usage"
                  detail={`${metrics.available_disk_gb} GB available`}
                  icon={HardDrive}
                  color="bg-teal-500"
                />
              </div>

              {/* Network + I/O Stats */}
              <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
                <h2 className="text-lg font-semibold text-foreground mb-4">
                  Network & I/O Throughput
                </h2>
                <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
                  <div className="p-3 rounded-md border bg-muted/50">
                    <span className="text-xs font-medium text-muted-foreground block">Network RX</span>
                    <span className="text-lg font-semibold text-emerald-600">{formatBytesPerSec(metrics.network_rx_bytes_per_sec)}</span>
                  </div>
                  <div className="p-3 rounded-md border bg-muted/50">
                    <span className="text-xs font-medium text-muted-foreground block">Network TX</span>
                    <span className="text-lg font-semibold text-blue-600">{formatBytesPerSec(metrics.network_tx_bytes_per_sec)}</span>
                  </div>
                  <div className="p-3 rounded-md border bg-muted/50">
                    <span className="text-xs font-medium text-muted-foreground block">Disk Read</span>
                    <span className="text-lg font-semibold">{formatBytesPerSec(metrics.disk_read_bytes_per_sec)}</span>
                  </div>
                  <div className="p-3 rounded-md border bg-muted/50">
                    <span className="text-xs font-medium text-muted-foreground block">Disk Write</span>
                    <span className="text-lg font-semibold">{formatBytesPerSec(metrics.disk_write_bytes_per_sec)}</span>
                  </div>
                </div>
                {/* Connection tracking. Shown next to throughput because that is
                    where an operator looks when tenants report dropped
                    connections — and because it is the one figure here that can
                    be at its limit while every other reads healthy. */}
                {metrics.conntrack_max > 0 && (
                  <div className="mt-4 pt-4 border-t">
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-xs font-medium text-muted-foreground">
                        Connection tracking
                      </span>
                      <span className="font-mono text-xs">
                        {metrics.conntrack_count.toLocaleString()} / {metrics.conntrack_max.toLocaleString()}
                      </span>
                    </div>
                    <div className="h-2 rounded-full bg-muted overflow-hidden">
                      <div
                        className={`h-full rounded-full ${conntrackTone(metrics.conntrack_count, metrics.conntrack_max)}`}
                        style={{ width: `${Math.min(100, (metrics.conntrack_count / metrics.conntrack_max) * 100)}%` }}
                      />
                    </div>
                    {metrics.conntrack_count / metrics.conntrack_max >= 0.7 && (
                      <p className="text-xs text-amber-600 mt-2">
                        Nearing the limit. When this table fills, the node refuses new
                        connections for every VM on it — check Abuse for a guest opening
                        connections faster than it should.
                      </p>
                    )}
                  </div>
                )}
                {metrics.load_avg && metrics.load_avg.length >= 3 && (
                  <div className="mt-4 pt-4 border-t">
                    <span className="text-xs font-medium text-muted-foreground">Load Average: </span>
                    <span className="font-mono font-semibold">
                      {metrics.load_avg[0].toFixed(2)} / {metrics.load_avg[1].toFixed(2)} / {metrics.load_avg[2].toFixed(2)}
                    </span>
                  </div>
                )}
              </div>
            </>
          )}

          {/* No metrics for non-active nodes */}
          {node.status !== "active" && (
            <>
              {node.status === "maintenance" && (
                <div className="bg-amber-50 border border-amber-200 rounded-lg p-6 mb-6 dark:bg-amber-950 dark:border-amber-900">
                  <div className="flex items-center gap-3">
                    <AlertTriangle className="w-6 h-6 text-amber-600" />
                    <div>
                      <p className="font-semibold text-amber-700 dark:text-amber-300">Node in Maintenance Mode</p>
                      <p className="text-sm text-muted-foreground">VM operations are disabled while in maintenance mode.</p>
                    </div>
                  </div>
                </div>
              )}

              {node.status === "offline" && (
                <div className="bg-red-50 border border-red-200 rounded-lg p-6 mb-6 dark:bg-red-950 dark:border-red-900">
                  <div className="flex items-center gap-3">
                    <XCircle className="w-6 h-6 text-red-600" />
                    <div>
                      <p className="font-semibold text-red-700 dark:text-red-300">Node Offline</p>
                      <p className="text-sm text-muted-foreground">Cannot connect to this node. Check network connectivity and node status.</p>
                    </div>
                  </div>
                </div>
              )}
            </>
          )}

          {/* Node Details */}
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
            <h2 className="text-lg font-semibold text-foreground mb-4">
              Node Details
            </h2>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
              <div className="p-3 rounded-md border">
                <span className="text-xs font-medium text-muted-foreground block">Node ID</span>
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm font-medium">{node.id.slice(0, 16)}...</span>
                  <button onClick={() => copyToClipboard(node.id)} className="p-1 rounded hover:bg-muted" aria-label="Copy node ID">
                    {copied ? <Check className="w-3 h-3 text-emerald-600" /> : <Copy className="w-3 h-3" />}
                  </button>
                </div>
              </div>
              <div className="p-3 rounded-md border">
                <span className="text-xs font-medium text-muted-foreground block">IP Address</span>
                <span className="font-mono text-sm font-medium">{node.ip_address}</span>
              </div>
              <div className="p-3 rounded-md border">
                <span className="text-xs font-medium text-muted-foreground block">Created</span>
                <span className="text-sm font-medium">{formatDate(node.created_at)}</span>
              </div>
            </div>
          </div>

          {/* Agent Token */}
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
            <h2 className="text-lg font-semibold text-foreground mb-2">
              Agent Token
            </h2>
            <p className="text-xs text-muted-foreground mb-4">Use this token to authenticate the node agent with the control panel.</p>

            <div className="flex items-center gap-2 mb-4">
              <div className="flex-1 p-3 rounded-md border bg-muted font-mono text-sm overflow-hidden">
                {showToken && tokenData?.token ? (
                  <span className="break-all">{tokenData.token}</span>
                ) : (
                  <span>••••••••••••••••••••••••••••••••</span>
                )}
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  if (!showToken) {
                    refetchToken()
                  }
                  setShowToken(!showToken)
                }}
              >
                {showToken ? 'Hide' : 'Show'}
              </Button>
            </div>

            <div className="flex items-center gap-2">
              <Button
                variant="secondary"
                size="sm"
                className="gap-2"
                onClick={async () => {
                  if (!tokenData?.token) {
                    const result = await refetchToken()
                    if (result.data?.token) {
                      await navigator.clipboard.writeText(result.data.token)
                      setCopiedToken(true)
                      setToast({ message: "Token copied to clipboard", type: "success" })
                      setTimeout(() => setCopiedToken(false), 2000)
                    }
                  } else {
                    await navigator.clipboard.writeText(tokenData.token)
                    setCopiedToken(true)
                    setToast({ message: "Token copied to clipboard", type: "success" })
                    setTimeout(() => setCopiedToken(false), 2000)
                  }
                }}
              >
                {copiedToken ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
                {copiedToken ? 'Copied' : 'Copy'}
              </Button>
              <Button
                variant="destructive"
                size="sm"
                className="gap-2"
                onClick={async () => {
                  try {
                    await regenerateToken.mutateAsync()
                    setShowToken(true)
                    refetchToken()
                    setToast({ message: "Token regenerated. Update the agent config.", type: "success" })
                  } catch (err) {
                    setToast({ message: `Failed to regenerate token: ${(err as Error).message}`, type: "error" })
                  }
                }}
                disabled={regenerateToken.isPending}
              >
                {regenerateToken.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
                Regenerate
              </Button>
            </div>

            <div className="mt-4 p-3 rounded-md bg-amber-50 border border-amber-200 text-xs dark:bg-amber-950 dark:border-amber-900">
              <p className="font-medium text-amber-700 dark:text-amber-300">Important</p>
              <p className="text-muted-foreground mt-1">If you regenerate the token, update the agent configuration on the node server and restart the agent.</p>
            </div>
          </div>

          {/* Agent installation */}
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
            <h2 className="text-lg font-semibold text-foreground mb-2 flex items-center gap-2">
              <Terminal className="w-5 h-5" />Install / Re-install the Agent
            </h2>
            <div className="p-4 rounded-md bg-amber-50 border border-amber-200 dark:bg-amber-950 dark:border-amber-900">
              <div className="flex items-start gap-3">
                <AlertTriangle className="w-5 h-5 text-amber-600 shrink-0 mt-0.5" />
                <div className="space-y-2">
                  <p className="font-medium text-sm text-foreground">
                    Automatic agent installation is temporarily unavailable while node deployment security is being completed.
                  </p>
                  <p className="text-xs text-muted-foreground">
                    Installing or re-installing the agent on a hypervisor host manually is an administrator-only operational procedure. See the deployment documentation (<code className="bg-muted px-1 rounded border">docs/DEPLOYMENT.md</code>, agent deployment) for details.
                  </p>
                  <p className="text-xs text-muted-foreground">
                    The agent token above is configured on the host when the agent is deployed or its token is rotated.
                  </p>
                </div>
              </div>
            </div>
          </div>

          {/* Virtual Machines on this Node */}
          <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
            <div className="p-4 border-b bg-muted/50">
              <h2 className="text-lg font-semibold text-foreground">
                Virtual Machines ({vms.length})
              </h2>
            </div>

            {vmsLoading ? (
              <div className="p-6 space-y-4">
                {[1,2,3].map(i => <Skeleton key={i} className="h-16 w-full" />)}
              </div>
            ) : vms.length === 0 ? (
              <div className="p-12 text-center">
                <Database className="w-12 h-12 text-muted-foreground mx-auto mb-4" />
                <p className="text-muted-foreground font-medium">No VMs on this node</p>
              </div>
            ) : (
              <div className="divide-y">
                {vms.map((vm) => (
                  <div key={vm.id} className="p-4 flex items-center justify-between hover:bg-muted/50">
                    <div className="flex items-center gap-4">
                      <div className="w-10 h-10 bg-muted text-muted-foreground flex items-center justify-center rounded-md">
                        <Server className="w-5 h-5" />
                      </div>
                      <div>
                        <Link href={`/vms/${vm.id}`} className="font-semibold hover:text-primary transition-colors">
                          {vm.hostname}
                        </Link>
                        <p className="text-xs text-muted-foreground">
                          {vm.resources.cpu} vCPU • {Math.round(vm.resources.ram / 1024 * 10) / 10} GB RAM • {vm.resources.disk} GB Disk
                        </p>
                      </div>
                    </div>

                    <div className="flex items-center gap-4">
                      {/* Resources */}
                      <div className="hidden md:flex items-center gap-3">
                        <div className="flex items-center gap-1">
                          <div className="w-6 h-6 bg-muted text-muted-foreground flex items-center justify-center rounded text-[10px] font-semibold">
                            {vm.resources.cpu}
                          </div>
                          <span className="text-xs text-muted-foreground">CPU</span>
                        </div>
                        <div className="flex items-center gap-1">
                          <div className="w-6 h-6 bg-muted text-muted-foreground flex items-center justify-center rounded text-[10px] font-semibold">
                            {Math.round(vm.resources.ram / 1024)}
                          </div>
                          <span className="text-xs text-muted-foreground">GB</span>
                        </div>
                      </div>

                      {/* Status */}
                      <VMStatusBadge status={vm.status} />

                      {/* Actions */}
                      <div className="flex items-center gap-1">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "start")}
                          disabled={vm.status === "running" || node.status !== "active" || !!actionLoading}
                          className="h-8 w-8 p-0"
                          title="Start"
                        >
                          {actionLoading === `${vm.id}-start` ? (
                            <Loader2 className="w-4 h-4 animate-spin" />
                          ) : (
                            <Play className="w-4 h-4" />
                          )}
                        </Button>

                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "stop")}
                          disabled={vm.status === "stopped" || node.status !== "active" || !!actionLoading}
                          className="h-8 w-8 p-0 text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950"
                          title="Stop"
                        >
                          {actionLoading === `${vm.id}-stop` ? (
                            <Loader2 className="w-4 h-4 animate-spin" />
                          ) : (
                            <Square className="w-4 h-4" />
                          )}
                        </Button>

                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "restart")}
                          disabled={vm.status !== "running" || node.status !== "active" || !!actionLoading}
                          className="h-8 w-8 p-0"
                          title="Restart"
                        >
                          {actionLoading === `${vm.id}-restart` ? (
                            <Loader2 className="w-4 h-4 animate-spin" />
                          ) : (
                            <RotateCcw className="w-4 h-4" />
                          )}
                        </Button>

                        <Link href={`/vms/${vm.id}`}>
                          <Button
                            variant="outline"
                            size="sm"
                            className="h-8 w-8 p-0"
                            title="Details"
                          >
                            <Terminal className="w-4 h-4" />
                          </Button>
                        </Link>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </TabsContent>

        <TabsContent value="import">
          <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
            <div className="p-4 border-b bg-muted/50 flex items-center justify-between">
              <div>
                <h2 className="text-lg font-semibold text-foreground">
                  Import Virtual Machines
                </h2>
                <p className="text-xs text-muted-foreground">
                  Scan this node for existing VMs to import
                </p>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  disabled={node.status !== "active" || scanLoading}
                  className="gap-2"
                  onClick={() => refetchScan()}
                >
                  {scanLoading ? <Loader2 className="w-4 h-4 animate-spin" /> : <Search className="w-4 h-4" />}
                  {scanLoading ? 'Scanning...' : 'Scan for VMs'}
                </Button>
                <Button
                  disabled={node.status !== "active" || syncVMs.isPending}
                  className="gap-2"
                  variant="outline"
                  onClick={() => syncVMs.mutate(undefined, {
                  onSuccess: () => {
                    refetchVMs()
                  }
                })}
              >
                {syncVMs.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
                {syncVMs.isPending ? 'Syncing...' : 'Sync VM Info'}
              </Button>
              </div>
            </div>

            {syncVMs.isSuccess && syncVMs.data && (
              <div className="p-4 bg-emerald-50 border-b dark:bg-emerald-950">
                <p className="font-medium text-sm text-emerald-700 dark:text-emerald-300">
                  Sync completed: {syncVMs.data.data.updated} updated, {syncVMs.data.data.unchanged} unchanged, {syncVMs.data.data.skipped} skipped
                  {syncVMs.data.data.errors > 0 && <span className="text-red-600">, {syncVMs.data.data.errors} errors</span>}
                </p>
              </div>
            )}

            {syncVMs.isError && (
              <div className="p-4 bg-red-50 border-b dark:bg-red-950">
                <p className="font-medium text-sm text-red-700 dark:text-red-300">
                  Sync failed: {(syncVMs.error as Error).message}
                </p>
              </div>
            )}

            {scanError ? (
              <div className="p-8 text-center">
                <AlertTriangle className="w-10 h-10 mx-auto text-red-500 mb-3" />
                <p className="font-medium text-sm mb-1">Scan Failed</p>
                <p className="text-muted-foreground text-xs">{(scanError as Error).message}</p>
              </div>
            ) : scanData?.vms && scanData.vms.length > 0 ? (
              <div className="divide-y">
                <div className="p-3 bg-muted/50 flex items-center justify-between">
                  <span className="text-xs font-medium text-muted-foreground">
                    Found {scanData.total_found} VMs
                  </span>
                </div>
                {scanData.vms.map((vm) => (
                  <div key={vm.uuid} className="p-4 flex items-center justify-between hover:bg-muted/50">
                    <div className="flex items-center gap-4">
                      <div className="w-10 h-10 bg-muted text-muted-foreground flex items-center justify-center rounded-md">
                        <Server className="w-5 h-5" />
                      </div>
                      <div>
                        <span className="font-semibold block">
                          {vm.hostname || vm.name}
                        </span>
                        <div className="flex items-center gap-3 text-xs text-muted-foreground">
                          <span>{vm.cpu} vCPU</span>
                          <span>{Math.round(vm.memory_mb / 1024 * 10) / 10} GB RAM</span>
                          <span className={`font-medium ${vm.status === 'running' ? 'text-emerald-600' : 'text-muted-foreground'}`}>
                            {vm.status}
                          </span>
                          {vm.hostname && vm.hostname !== vm.name && (
                            <span className="text-muted-foreground">({vm.name})</span>
                          )}
                        </div>
                        <span className="text-[10px] text-muted-foreground font-mono">{vm.uuid}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {vm.conflicts ? (
                        <span className="text-xs font-medium text-amber-700 border border-amber-200 bg-amber-50 rounded-full px-2 py-1 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900">Already Imported</span>
                      ) : (
                        <>
                          <span className="text-xs font-medium text-emerald-700 border border-emerald-200 bg-emerald-50 rounded-full px-2 py-1 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900">Available</span>
                          <Button
                            size="sm"
                            className="text-xs gap-1"
                            onClick={() => {
                              setImportVMUUID(vm.uuid)
                              setImportDialogOpen(true)
                            }}
                          >
                            Import
                          </Button>
                        </>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="p-12 text-center">
                <FolderOpen className="w-12 h-12 text-muted-foreground mx-auto mb-4" />
                <p className="text-muted-foreground font-medium">VM Import</p>
                <p className="text-xs text-muted-foreground mt-2">Click &quot;Scan for VMs&quot; to discover VMs on this node</p>
              </div>
            )}
          </div>
        </TabsContent>
      </Tabs>

      {/* Toast */}
      {toast && (
        <Toast
          message={toast.message}
          type={toast.type}
          onClose={() => setToast(null)}
        />
      )}

      {/* Import VM Dialog */}
      <Dialog open={importDialogOpen} onOpenChange={setImportDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Import Virtual Machine</DialogTitle>
            <DialogDescription>
              Select owner and OS template for this VM
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div>
              <label className="text-sm font-medium block mb-1">Owner</label>
              {usersError && <p className="text-xs text-red-500 mb-1">Error: {(usersError as Error).message}</p>}
              <select
                className="w-full rounded-md border border-input bg-background p-2 text-sm"
                value={importUserId}
                onChange={(e) => setImportUserId(e.target.value)}
              >
                <option value="">Select user...</option>
                {users.map((user: any) => (
                  <option key={user.id} value={user.id}>{user.email}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-sm font-medium block mb-1">OS Template</label>
              {templatesError && <p className="text-xs text-red-500 mb-1">Error: {(templatesError as Error).message}</p>}
              <select
                className="w-full rounded-md border border-input bg-background p-2 text-sm"
                value={importTemplateId}
                onChange={(e) => setImportTemplateId(e.target.value)}
              >
                <option value="">Select template...</option>
                {templates.map((tpl: any) => (
                  <option key={tpl.id} value={tpl.id}>{tpl.name}</option>
                ))}
              </select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              disabled={!importUserId || !importTemplateId || importVMs.isPending}
              onClick={() => {
                importVMs.mutate(
                  { vm_uuids: [importVMUUID], user_id: importUserId, os_template_id: importTemplateId },
                  {
                    onSuccess: (data) => {
                      setImportDialogOpen(false)
                      setToast({ message: `Import successful: ${data.success_count} imported`, type: "success" })
                      refetchScan()
                      refetchVMs()
                    },
                    onError: (err) => {
                      setToast({ message: `Import failed: ${err.message}`, type: "error" })
                    },
                  }
                )
              }}
            >
              {importVMs.isPending ? <Loader2 className="w-4 h-4 animate-spin mr-2" /> : null}
              Import VM
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
