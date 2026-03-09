"use client"

import { useState, useEffect, useCallback } from "react"
import Link from "next/link"
import { useParams, useRouter } from "next/navigation"
import { 
  Server, 
  ArrowLeft,
  Play,
  Square,
  RotateCcw,
  Terminal,
  Edit2,
  RefreshCw,
  Activity,
  HardDrive,
  Cpu,
  Network,
  Clock,
  AlertTriangle,
  CheckCircle,
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
import { useNode, useUpdateNode, useNodeMetrics } from "@/lib/hooks/use-nodes"
import { useVMs } from "@/lib/hooks/use-vms"
import { useVMActions } from "@/lib/hooks/use-vms"
import type { NodeStatus, VMStatus } from "@/types"

// Status indicator component
function StatusIndicator({ status }: { status: NodeStatus }) {
  const config: Record<string, { bg: string; text: string; label: string }> = {
    active: { bg: "bg-success", text: "text-success", label: "Active" },
    offline: { bg: "bg-danger", text: "text-danger", label: "Offline" },
    maintenance: { bg: "bg-warning", text: "text-warning", label: "Maintenance" },
  }
  
  const { bg, text, label } = config[status] || config.offline
  
  return (
    <div className="flex items-center gap-2">
      <span className={`w-3 h-3 rounded-full ${bg} ${status === "active" ? "animate-pulse" : ""}`} />
      <span className={`text-sm font-black uppercase ${text}`}>{label}</span>
    </div>
  )
}

// VM Status badge
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

// Resource gauge component
function ResourceGauge({ value, label, detail, icon: Icon, color }: { value: number; label: string; detail?: string; icon: React.ElementType; color: string }) {
  const getColorClass = () => {
    if (value >= 90) return "bg-danger"
    if (value >= 70) return "bg-warning"
    return color
  }
  
  return (
    <div className="bg-white border-4 border-black p-4 shadow-neo">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <div className={`w-8 h-8 ${color} flex items-center justify-center border-2 border-black`}>
            <Icon className="w-4 h-4" />
          </div>
          <div>
            <span className="text-xs font-black uppercase text-gray-500">{label}</span>
            {detail && <span className="text-[10px] text-gray-400 block">{detail}</span>}
          </div>
        </div>
        <span className="text-2xl font-black">{value}%</span>
      </div>
      <div className="h-3 bg-gray-200 border-2 border-black">
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
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

export default function NodeDetailPage() {
  const params = useParams()
  const router = useRouter()
  const nodeId = params.id as string
  
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [copied, setCopied] = useState(false)
  const [editDialogOpen, setEditDialogOpen] = useState(false)
  const [editName, setEditName] = useState("")
  const [actionLoading, setActionLoading] = useState<string | null>(null)

  // Data hooks
  const { data: node, isLoading, error, refetch } = useNode(nodeId)
  const { data: metrics } = useNodeMetrics(nodeId)
  const { data: vmsData, isLoading: vmsLoading, refetch: refetchVMs } = useVMs({ nodeId, pageSize: 100 })
  const updateNode = useUpdateNode(nodeId)
  const vmActions = useVMActions()

  const vms = vmsData?.data || []

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
            <Button variant="ghost" size="icon" className="border-2 border-black">
              <ArrowLeft className="w-4 h-4" />
            </Button>
          </Link>
          <div className="flex-1">
            <Skeleton className="h-8 w-48 mb-2" />
            <Skeleton className="h-5 w-32" />
          </div>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-24 border-4 border-black" />)}
        </div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6">
          {[1,2,3].map(i => <Skeleton key={i} className="h-32 border-4 border-black" />)}
        </div>
      </div>
    )
  }

  // Error / not found
  if (error || !node) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <Server className="w-16 h-16 text-gray-300 mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Node Not Found</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error)?.message || "The requested node does not exist."}</p>
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
          <Button variant="ghost" size="icon" className="border-2 border-black">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <h1 className="text-3xl font-black uppercase tracking-tight text-black">
              {node.name}
            </h1>
            <StatusIndicator status={node.status} />
          </div>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
            {node.ip_address} • Created {formatDate(node.created_at)}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Dialog open={editDialogOpen} onOpenChange={(open) => { setEditDialogOpen(open); if (open) setEditName(node.name) }}>
            <DialogTrigger asChild>
              <Button variant="ghost" className="border-2 border-black gap-2">
                <Edit2 className="w-4 h-4" />
                Edit
              </Button>
            </DialogTrigger>
            <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
              <DialogHeader>
                <DialogTitle className="text-lg font-black uppercase">Edit Node</DialogTitle>
                <DialogDescription>Update node name</DialogDescription>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div>
                  <label htmlFor="edit-name" className="block text-xs font-black uppercase text-gray-500 mb-1">Name</label>
                  <Input id="edit-name" value={editName} onChange={(e) => setEditName(e.target.value)} className="border-2 border-black" />
                </div>
              </div>
              <DialogFooter>
                <Button variant="ghost" onClick={() => setEditDialogOpen(false)}>Cancel</Button>
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
            <div className="bg-white border-4 border-black p-4 shadow-neo">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-black uppercase text-gray-500">Total VMs</span>
                <Database className="w-4 h-4 text-gray-400" />
              </div>
              <p className="text-3xl font-black text-black">{metrics?.running_vm_count ?? vms.length}</p>
            </div>
            
            <div className="bg-white border-4 border-black p-4 shadow-neo">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-black uppercase text-gray-500">Running</span>
                <span className="w-3 h-3 bg-success rounded-full animate-pulse" />
              </div>
              <p className="text-3xl font-black text-success">{runningVMs}</p>
            </div>
            
            <div className="bg-white border-4 border-black p-4 shadow-neo">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-black uppercase text-gray-500">Stopped</span>
                <span className="w-3 h-3 bg-danger rounded-full" />
              </div>
              <p className="text-3xl font-black text-danger">{stoppedVMs}</p>
            </div>
            
            <div className="bg-white border-4 border-black p-4 shadow-neo">
              <div className="flex items-center justify-between mb-2">
                <span className="text-xs font-black uppercase text-gray-500">Status</span>
                <Clock className="w-4 h-4 text-gray-400" />
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
                  color="bg-primary" 
                />
                <ResourceGauge 
                  value={Math.round(metrics.memory_used_percent)} 
                  label="Memory Usage" 
                  detail={`${metrics.available_memory_mb} MB available`}
                  icon={MemoryStick} 
                  color="bg-secondary" 
                />
                <ResourceGauge 
                  value={Math.round(metrics.disk_used_percent)} 
                  label="Disk Usage" 
                  detail={`${metrics.available_disk_gb} GB available`}
                  icon={HardDrive} 
                  color="bg-accent" 
                />
              </div>

              {/* Network + I/O Stats */}
              <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
                <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
                  Network & I/O Throughput
                </h2>
                <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
                  <div className="p-3 border-2 border-black bg-gray-50">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Network RX</span>
                    <span className="text-lg font-black text-success">{formatBytesPerSec(metrics.network_rx_bytes_per_sec)}</span>
                  </div>
                  <div className="p-3 border-2 border-black bg-gray-50">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Network TX</span>
                    <span className="text-lg font-black text-primary">{formatBytesPerSec(metrics.network_tx_bytes_per_sec)}</span>
                  </div>
                  <div className="p-3 border-2 border-black bg-gray-50">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Disk Read</span>
                    <span className="text-lg font-black">{formatBytesPerSec(metrics.disk_read_bytes_per_sec)}</span>
                  </div>
                  <div className="p-3 border-2 border-black bg-gray-50">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Disk Write</span>
                    <span className="text-lg font-black">{formatBytesPerSec(metrics.disk_write_bytes_per_sec)}</span>
                  </div>
                </div>
                {metrics.load_avg && metrics.load_avg.length >= 3 && (
                  <div className="mt-4 pt-4 border-t-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500">Load Average: </span>
                    <span className="font-mono font-bold">
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
                <div className="bg-warning/20 border-4 border-warning p-6 mb-6">
                  <div className="flex items-center gap-3">
                    <AlertTriangle className="w-6 h-6 text-warning" />
                    <div>
                      <p className="font-black uppercase text-warning">Node in Maintenance Mode</p>
                      <p className="text-sm font-medium">VM operations are disabled while in maintenance mode.</p>
                    </div>
                  </div>
                </div>
              )}
              
              {node.status === "offline" && (
                <div className="bg-danger/10 border-4 border-danger p-6 mb-6">
                  <div className="flex items-center gap-3">
                    <XCircle className="w-6 h-6 text-danger" />
                    <div>
                      <p className="font-black uppercase text-danger">Node Offline</p>
                      <p className="text-sm font-medium">Cannot connect to this node. Check network connectivity and node status.</p>
                    </div>
                  </div>
                </div>
              )}
            </>
          )}

          {/* Node Details */}
          <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
            <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
              Node Details
            </h2>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
              <div className="p-3 border-2 border-black">
                <span className="text-xs font-bold uppercase text-gray-500 block">Node ID</span>
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm font-bold">{node.id.slice(0, 16)}...</span>
                  <button onClick={() => copyToClipboard(node.id)} className="p-1 hover:bg-gray-100" aria-label="Copy node ID">
                    {copied ? <Check className="w-3 h-3 text-success" /> : <Copy className="w-3 h-3" />}
                  </button>
                </div>
              </div>
              <div className="p-3 border-2 border-black">
                <span className="text-xs font-bold uppercase text-gray-500 block">IP Address</span>
                <span className="font-mono text-sm font-bold">{node.ip_address}</span>
              </div>
              <div className="p-3 border-2 border-black">
                <span className="text-xs font-bold uppercase text-gray-500 block">Created</span>
                <span className="text-sm font-bold">{formatDate(node.created_at)}</span>
              </div>
            </div>
          </div>

          {/* Virtual Machines on this Node */}
          <div className="bg-white border-4 border-black shadow-neo">
            <div className="p-4 border-b-4 border-black bg-gray-50">
              <h2 className="text-lg font-black uppercase tracking-tight text-black">
                Virtual Machines ({vms.length})
              </h2>
            </div>
            
            {vmsLoading ? (
              <div className="p-6 space-y-4">
                {[1,2,3].map(i => <Skeleton key={i} className="h-16 w-full" />)}
              </div>
            ) : vms.length === 0 ? (
              <div className="p-12 text-center">
                <Database className="w-12 h-12 text-gray-300 mx-auto mb-4" />
                <p className="text-gray-500 font-bold uppercase">No VMs on this node</p>
              </div>
            ) : (
              <div className="divide-y-2 divide-black">
                {vms.map((vm) => (
                  <div key={vm.id} className="p-4 flex items-center justify-between hover:bg-gray-50">
                    <div className="flex items-center gap-4">
                      <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                        <Server className="w-5 h-5" />
                      </div>
                      <div>
                        <Link href={`/vms/${vm.id}`} className="font-black text-black hover:text-primary transition-colors">
                          {vm.hostname}
                        </Link>
                        <p className="text-xs text-gray-500 font-medium">
                          {vm.resources.cpu} vCPU • {Math.round(vm.resources.ram / 1024 * 10) / 10} GB RAM • {vm.resources.disk} GB Disk
                        </p>
                      </div>
                    </div>
                    
                    <div className="flex items-center gap-4">
                      {/* Resources */}
                      <div className="hidden md:flex items-center gap-3">
                        <div className="flex items-center gap-1">
                          <div className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-[10px] font-black">
                            {vm.resources.cpu}
                          </div>
                          <span className="text-xs text-gray-500">CPU</span>
                        </div>
                        <div className="flex items-center gap-1">
                          <div className="w-6 h-6 bg-secondary flex items-center justify-center border border-black text-[10px] font-black">
                            {Math.round(vm.resources.ram / 1024)}
                          </div>
                          <span className="text-xs text-gray-500">GB</span>
                        </div>
                      </div>
                      
                      {/* Status */}
                      <VMStatusBadge status={vm.status} />
                      
                      {/* Actions */}
                      <div className="flex items-center gap-1">
                        <Button
                          variant="default"
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
                          variant="destructive"
                          size="sm"
                          onClick={() => handleVMAction(vm.id, "stop")}
                          disabled={vm.status === "stopped" || node.status !== "active" || !!actionLoading}
                          className="h-8 w-8 p-0"
                          title="Stop"
                        >
                          {actionLoading === `${vm.id}-stop` ? (
                            <Loader2 className="w-4 h-4 animate-spin" />
                          ) : (
                            <Square className="w-4 h-4" />
                          )}
                        </Button>
                        
                        <Button
                          variant="secondary"
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
                            variant="secondary"
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
          <div className="bg-white border-4 border-black shadow-neo">
            <div className="p-4 border-b-4 border-black bg-gray-50 flex items-center justify-between">
              <div>
                <h2 className="text-lg font-black uppercase tracking-tight text-black">
                  Import Virtual Machines
                </h2>
                <p className="text-xs text-gray-500 font-medium">
                  Scan this node for existing Virtualizor VMs to import
                </p>
              </div>
              <Button disabled={node.status !== "active"} className="gap-2">
                <Search className="w-4 h-4" />
                Scan for VMs
              </Button>
            </div>
            
            <div className="p-12 text-center">
              <FolderOpen className="w-12 h-12 text-gray-300 mx-auto mb-4" />
              <p className="text-gray-500 font-bold uppercase">VM Import</p>
              <p className="text-xs text-gray-400 mt-2">Import scanning feature coming soon</p>
            </div>
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
    </div>
  )
}
