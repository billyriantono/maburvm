"use client"

import { useState, useCallback } from "react"
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
import { UserPicker } from "@/components/user-picker"
import { useTemplates } from "@/lib/hooks/use-templates"
import { toast } from "sonner"
import { StatusBadge, useVMTransitions } from "@/components/status-badge"
import { useCopy } from "@/lib/use-feedback"

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

  const { copy, copied } = useCopy()
  const [showToken, setShowToken] = useState(false)
  const [editDialogOpen, setEditDialogOpen] = useState(false)
  const [editName, setEditName] = useState("")
  // vmId -> action in flight, so only the acted-on row is disabled.
  const [pendingVMs, setPendingVMs] = useState<Record<string, string>>({})
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
  const { data: templatesData, error: templatesError } = useTemplates()
  const updateNode = useUpdateNode(nodeId)
  const vmActions = useVMActions()

  const vms = vmsData?.data || []
  const transitions = useVMTransitions(vms)
  const templates: any[] = Array.isArray(templatesData) ? templatesData : (templatesData as any)?.data || []

  // VM action handler
  const handleVMAction = useCallback(async (vmId: string, action: string, fromStatus: string) => {
    setPendingVMs((p) => ({ ...p, [vmId]: action }))
    transitions.begin(vmId, action, fromStatus)
    try {
      await vmActions.mutateAsync({ vmId, action })
      toast.success(`VM ${action} successful`)
      refetchVMs()
    } catch (err) {
      transitions.cancel(vmId)
      toast.error(`Failed to ${action} VM: ${(err as Error).message}`)
    } finally {
      setPendingVMs((p) => {
        const next = { ...p }
        delete next[vmId]
        return next
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [vmActions, refetchVMs, transitions.begin, transitions.cancel])

  // Edit handler
  const handleEdit = useCallback(async () => {
    if (!editName.trim()) return
    try {
      await updateNode.mutateAsync({ name: editName })
      toast.success("Node updated successfully")
      setEditDialogOpen(false)
      refetch()
    } catch (err) {
      toast.error(`Failed to update node: ${(err as Error).message}`)
    }
  }, [updateNode, editName, refetch])

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
            <StatusBadge status={node.status} />
            {node.creation_paused && (<span className="inline-flex items-center rounded-md border border-amber-300 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">New VMs paused</span>)}
          </div>
          <p className="text-muted-foreground text-sm">
            {node.ip_address} • Created {formatDate(node.created_at)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            disabled={updateNode.isPending}
            title="Stops new VMs being placed on this node. VMs already here keep running and can still be managed."
            onClick={async () => {
              const pausing = !node.creation_paused
              try {
                await updateNode.mutateAsync({ creation_paused: pausing })
                toast.success(pausing ? "New VMs are paused on this node. Existing VMs are not affected." : "This node takes new VMs again")
              } catch (err) {
                toast.error(`Could not change VM creation: ${(err as Error).message}`)
              }
            }}
          >
            {node.creation_paused ? "Resume new VMs" : "Pause new VMs"}
          </Button>
          <Button
            variant="outline"
            disabled={updateNode.isPending}
            onClick={async () => {
              const entering = node.status !== "maintenance"
              try {
                await updateNode.mutateAsync({ status: entering ? "maintenance" : "active" })
                toast.success(entering ? "Node is in maintenance. New VMs go elsewhere and VM actions are refused." : "Node is back in service")
              } catch (err) {
                toast.error(`Could not change maintenance mode: ${(err as Error).message}`)
              }
            }}
          >
            {node.status === "maintenance" ? "End maintenance" : "Start maintenance"}
          </Button>
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
                <span className="w-2.5 h-2.5 bg-emerald-500 rounded-full" aria-hidden="true" />
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
              <StatusBadge status={node.status} />
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
                        className={`h-full rounded-full transition-[width] duration-500 ease-out ${conntrackTone(metrics.conntrack_count, metrics.conntrack_max)}`}
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
                  <button onClick={() => copy(node.id, "node-id")} className="p-1 rounded hover:bg-muted" aria-label={copied("node-id") ? "Node ID copied" : "Copy node ID"}>
                    {copied("node-id") ? <Check className="w-3 h-3 text-emerald-600" aria-hidden="true" /> : <Copy className="w-3 h-3" aria-hidden="true" />}
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
                    if (result.data?.token) await copy(result.data.token, "token")
                  } else {
                    await copy(tokenData.token, "token")
                  }
                }}
              >
                {copied("token") ? <Check className="w-4 h-4" aria-hidden="true" /> : <Copy className="w-4 h-4" aria-hidden="true" />}
                {copied("token") ? 'Copied' : 'Copy'}
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
                    toast.success("Token regenerated. Update the agent config.")
                  } catch (err) {
                    toast.error(`Failed to regenerate token: ${(err as Error).message}`)
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
                      <StatusBadge status={vm.status} transition={transitions.transitionOf(vm.id)} />

                      {/* Actions */}
                      <div className="flex items-center gap-1">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "start", vm.status)}
                          disabled={vm.status === "running" || node.status !== "active" || !!pendingVMs[vm.id]}
                          className="h-8 w-8 p-0"
                          title="Start"
                          aria-label="Start {vm.hostname}"
                        >
                          {pendingVMs[vm.id] === "start" ? (
                            <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
                          ) : (
                            <Play className="w-4 h-4" aria-hidden="true" />
                          )}
                        </Button>

                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "stop", vm.status)}
                          disabled={vm.status === "stopped" || node.status !== "active" || !!pendingVMs[vm.id]}
                          className="h-8 w-8 p-0 text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950"
                          title="Stop"
                          aria-label="Stop {vm.hostname}"
                        >
                          {pendingVMs[vm.id] === "stop" ? (
                            <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
                          ) : (
                            <Square className="w-4 h-4" aria-hidden="true" />
                          )}
                        </Button>

                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "restart", vm.status)}
                          disabled={vm.status !== "running" || node.status !== "active" || !!pendingVMs[vm.id]}
                          className="h-8 w-8 p-0"
                          title="Restart"
                          aria-label="Restart {vm.hostname}"
                        >
                          {pendingVMs[vm.id] === "restart" ? (
                            <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
                          ) : (
                            <RotateCcw className="w-4 h-4" aria-hidden="true" />
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
              <label htmlFor="import-owner" className="text-sm font-medium block mb-1">Owner</label>
              <UserPicker id="import-owner" value={importUserId} onChange={(id) => setImportUserId(id)} />
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
                      toast.success(`Import successful: ${data.success_count} imported`)
                      refetchScan()
                      refetchVMs()
                    },
                    onError: (err) => {
                      toast.error(`Import failed: ${err.message}`)
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
