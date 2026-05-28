"use client"

import { useState, useEffect, useCallback } from "react"
import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { 
  Play, Square, RotateCcw, Trash2, RefreshCw, ArrowLeft,
  Copy, Check, Cpu, HardDrive,
  Monitor, Server, Network, Database, Shield, FileText, Terminal, Activity, ExternalLink,
  Loader2, AlertCircle, Plus
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useVM, useVMMetrics, useVMAction, useDeleteVM } from "@/lib/hooks/use-vms"
import { useSnapshots, useCreateSnapshot, useRestoreSnapshot, useDeleteSnapshot } from "@/lib/hooks/use-snapshots"
import { useBackups, useCreateBackup, useDeleteBackup } from "@/lib/hooks/use-backups"
import { useFirewallRules, useDeleteFirewallRule } from "@/lib/hooks/use-networks"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useVMBandwidth } from "@/lib/hooks/use-bandwidth"
import type { VMStatus, Snapshot, Backup, FirewallRule } from "@/types"

// --- Utility Components ---

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

function ProgressBar({ value, max, label, unit, color = "primary" }: { value: number; max: number; label: string; unit?: string; color?: "primary" | "secondary" | "accent" | "success" | "danger" }) {
  const percentage = max > 0 ? Math.round((value / max) * 100) : 0
  const colorClasses = { primary: "bg-primary", secondary: "bg-secondary", accent: "bg-accent", success: "bg-success", danger: "bg-danger" }
  return (
    <div className="mb-4">
      <div className="flex justify-between items-center mb-1">
        <span className="text-xs font-bold uppercase text-gray-600">{label}</span>
        <span className="text-xs font-black">{unit ? `${value} / ${max} ${unit}` : `${percentage}%`}</span>
      </div>
      <div className="h-4 bg-gray-200 border-2 border-black relative">
        <div className={`h-full ${colorClasses[color]} transition-all duration-300`} style={{ width: `${Math.min(percentage, 100)}%` }} />
      </div>
    </div>
  )
}

function StatusBadge({ status }: { status: VMStatus }) {
  const statusConfig: Record<string, { bg: string; text: string; label: string }> = {
    running: { bg: "bg-success", text: "text-black", label: "Running" },
    stopped: { bg: "bg-gray-400", text: "text-white", label: "Stopped" },
    suspended: { bg: "bg-warning", text: "text-black", label: "Suspended" },
    creating: { bg: "bg-secondary", text: "text-black", label: "Creating" },
    error: { bg: "bg-danger", text: "text-white", label: "Error" },
  }
  const config = statusConfig[status] || statusConfig.stopped
  return (
    <span className={`inline-flex items-center px-3 py-1 border-2 border-black text-xs font-bold uppercase shadow-neo-sm ${config.bg} ${config.text}`}>
      <span className={`w-2 h-2 mr-2 border border-black ${status === 'running' ? 'bg-black animate-pulse' : 'bg-black/30'}`} />
      {config.label}
    </span>
  )
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
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

function SectionSkeleton() {
  return (
    <div className="space-y-4 p-6">
      <Skeleton className="h-6 w-48" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-4 w-1/2" />
    </div>
  )
}

function SectionError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="p-8 text-center">
      <AlertCircle className="w-10 h-10 mx-auto text-danger mb-3" />
      <p className="font-bold uppercase text-sm mb-1">Failed to load</p>
      <p className="text-gray-500 text-xs mb-3">{message}</p>
      <Button size="sm" onClick={onRetry} className="gap-2">
        <RotateCcw className="w-3 h-3" />
        Retry
      </Button>
    </div>
  )
}

function SectionEmpty({ icon: Icon, message }: { icon: React.ElementType; message: string }) {
  return (
    <div className="p-8 text-center">
      <Icon className="w-10 h-10 mx-auto text-gray-300 mb-3" />
      <p className="text-gray-500 text-sm font-medium">{message}</p>
    </div>
  )
}

function BandwidthUsageCard({ vmId }: { vmId: string }) {
  const { data: bandwidth, isLoading, error } = useVMBandwidth(vmId)

  if (isLoading) return <SectionSkeleton />
  if (error || !bandwidth) return null

  const usagePercent = bandwidth.quota_gb > 0 ? Math.min(bandwidth.usage_percent, 100) : 0
  const isWarning = bandwidth.quota_gb > 0 && bandwidth.usage_percent >= 80
  const isDanger = bandwidth.exceeded

  return (
    <div className={`bg-white border-4 p-6 shadow-neo ${isDanger ? 'border-danger' : 'border-black'}`}>
      <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6 flex items-center gap-2">
        <Activity className="w-5 h-5" />Bandwidth Usage
        {isDanger && <span className="text-xs bg-danger text-white px-2 py-0.5 border-2 border-black">EXCEEDED</span>}
      </h2>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
        <div className="p-4 border-2 border-black">
          <span className="text-xs font-bold uppercase text-gray-500 block">Download</span>
          <span className="text-sm font-mono font-bold">{formatBytes(bandwidth.rx_bytes)}</span>
        </div>
        <div className="p-4 border-2 border-black">
          <span className="text-xs font-bold uppercase text-gray-500 block">Upload</span>
          <span className="text-sm font-mono font-bold">{formatBytes(bandwidth.tx_bytes)}</span>
        </div>
        <div className="p-4 border-2 border-black">
          <span className="text-xs font-bold uppercase text-gray-500 block">Total Used</span>
          <span className="text-sm font-mono font-bold">{bandwidth.used_gb.toFixed(2)} GB</span>
        </div>
        <div className="p-4 border-2 border-black">
          <span className="text-xs font-bold uppercase text-gray-500 block">Quota</span>
          <span className="text-sm font-mono font-bold">{bandwidth.quota_gb > 0 ? `${bandwidth.quota_gb} GB` : 'Unlimited'}</span>
        </div>
      </div>

      {bandwidth.quota_gb > 0 && (
        <div>
          <div className="flex justify-between items-center mb-1">
            <span className="text-xs font-bold uppercase text-gray-600">Usage</span>
            <span className="text-xs font-black">{bandwidth.used_gb.toFixed(2)} / {bandwidth.quota_gb} GB ({bandwidth.usage_percent.toFixed(1)}%)</span>
          </div>
          <div className="h-4 bg-gray-200 border-2 border-black relative">
            <div
              className={`h-full transition-all duration-300 ${isDanger ? 'bg-danger' : isWarning ? 'bg-warning' : 'bg-success'}`}
              style={{ width: `${usagePercent}%` }}
            />
          </div>
          <div className="flex justify-between mt-1">
            <span className="text-xs text-gray-400">{bandwidth.period_start}</span>
            <span className="text-xs text-gray-400">{bandwidth.period_end}</span>
          </div>
        </div>
      )}
    </div>
  )
}

// --- Main Component ---

export default function VMDetailPage() {
  const params = useParams()
  const router = useRouter()
  const vmId = params.id as string

  // State
  const [copied, setCopied] = useState(false)
  const [rebuildDialogOpen, setRebuildDialogOpen] = useState(false)
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [selectedTemplate, setSelectedTemplate] = useState("")
  const [confirmVMName, setConfirmVMName] = useState("")
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [snapshotName, setSnapshotName] = useState("")
  const [createSnapshotOpen, setCreateSnapshotOpen] = useState(false)

  // Data hooks
  const { data: vm, isLoading: vmLoading, error: vmError, refetch: refetchVM } = useVM(vmId)
  const { data: metrics } = useVMMetrics(vmId)
  const vmAction = useVMAction(vmId)
  const deleteVM = useDeleteVM()
  const { data: snapshots, isLoading: snapshotsLoading, error: snapshotsError, refetch: refetchSnapshots } = useSnapshots(vmId)
  const createSnapshot = useCreateSnapshot(vmId)
  const restoreSnapshot = useRestoreSnapshot(vmId)
  const deleteSnapshot = useDeleteSnapshot(vmId)
  const { data: backups, isLoading: backupsLoading, error: backupsError, refetch: refetchBackups } = useBackups(vmId)
  const createBackup = useCreateBackup(vmId)
  const deleteBackupMutation = useDeleteBackup(vmId)
  const { data: firewallRules, isLoading: firewallLoading, error: firewallError, refetch: refetchFirewall } = useFirewallRules(vmId)
  const deleteFirewallRule = useDeleteFirewallRule(vmId)
  const { data: templates } = useTemplates()

  // Handlers
  const handleAction = useCallback(async (action: string) => {
    setActionLoading(action)
    try {
      await vmAction.mutateAsync(action)
      setToast({ message: `VM ${action} successful`, type: "success" })
      refetchVM()
    } catch (err) {
      setToast({ message: `Failed to ${action} VM: ${(err as Error).message}`, type: "error" })
    } finally {
      setActionLoading(null)
    }
  }, [vmAction, refetchVM])

  const handleDelete = useCallback(async () => {
    try {
      await deleteVM.mutateAsync(vmId)
      setToast({ message: "VM deleted", type: "success" })
      setDeleteDialogOpen(false)
      router.push("/vms")
    } catch (err) {
      setToast({ message: `Failed to delete VM: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteVM, vmId, router])

  const handleCreateSnapshot = useCallback(async () => {
    if (!snapshotName.trim()) return
    try {
      await createSnapshot.mutateAsync({ vm_id: vmId, name: snapshotName })
      setToast({ message: "Snapshot created", type: "success" })
      setSnapshotName("")
      setCreateSnapshotOpen(false)
      refetchSnapshots()
    } catch (err) {
      setToast({ message: `Failed to create snapshot: ${(err as Error).message}`, type: "error" })
    }
  }, [createSnapshot, vmId, snapshotName, refetchSnapshots])

  const handleRestoreSnapshot = useCallback(async (snapshotId: string) => {
    try {
      await restoreSnapshot.mutateAsync(snapshotId)
      setToast({ message: "Snapshot restored", type: "success" })
      refetchVM()
    } catch (err) {
      setToast({ message: `Failed to restore snapshot: ${(err as Error).message}`, type: "error" })
    }
  }, [restoreSnapshot, refetchVM])

  const handleDeleteSnapshot = useCallback(async (snapshotId: string) => {
    try {
      await deleteSnapshot.mutateAsync(snapshotId)
      setToast({ message: "Snapshot deleted", type: "success" })
      refetchSnapshots()
    } catch (err) {
      setToast({ message: `Failed to delete snapshot: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteSnapshot, refetchSnapshots])

  const handleCreateBackup = useCallback(async () => {
    try {
      await createBackup.mutateAsync()
      setToast({ message: "Backup started", type: "success" })
      refetchBackups()
    } catch (err) {
      setToast({ message: `Failed to create backup: ${(err as Error).message}`, type: "error" })
    }
  }, [createBackup, refetchBackups])

  const handleDeleteBackup = useCallback(async (backupId: string) => {
    try {
      await deleteBackupMutation.mutateAsync(backupId)
      setToast({ message: "Backup deleted", type: "success" })
      refetchBackups()
    } catch (err) {
      setToast({ message: `Failed to delete backup: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteBackupMutation, refetchBackups])

  const handleDeleteFirewallRule = useCallback(async (ruleId: string) => {
    try {
      await deleteFirewallRule.mutateAsync(ruleId)
      setToast({ message: "Firewall rule deleted", type: "success" })
      refetchFirewall()
    } catch (err) {
      setToast({ message: `Failed to delete rule: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteFirewallRule, refetchFirewall])

  const handleRebuild = () => {
    if (confirmVMName === vm?.hostname && selectedTemplate) {
      setRebuildDialogOpen(false)
      setConfirmVMName("")
      setSelectedTemplate("")
      setToast({ message: "Rebuild initiated", type: "success" })
    }
  }

  const copyToClipboard = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Fallback
      const textarea = document.createElement("textarea")
      textarea.value = text
      textarea.style.position = "fixed"
      textarea.style.opacity = "0"
      document.body.appendChild(textarea)
      textarea.select()
      document.execCommand("copy")
      document.body.removeChild(textarea)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  // Loading state
  if (vmLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <nav className="flex items-center gap-2 mb-6">
          <Link href="/vms" className="flex items-center gap-2 text-sm font-bold uppercase text-gray-500 hover:text-black transition-colors w-fit">
            <ArrowLeft className="w-4 h-4" />Back to VMs
          </Link>
        </nav>
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <div className="flex items-center gap-4">
            <Skeleton className="w-16 h-16" />
            <div>
              <Skeleton className="h-8 w-64 mb-2" />
              <Skeleton className="h-5 w-32" />
            </div>
          </div>
        </div>
        <SectionSkeleton />
      </div>
    )
  }

  // Error state
  if (vmError || !vm) {
    return (
      <div className="max-w-7xl mx-auto">
        <nav className="flex items-center gap-2 mb-6">
          <Link href="/vms" className="flex items-center gap-2 text-sm font-bold uppercase text-gray-500 hover:text-black transition-colors w-fit">
            <ArrowLeft className="w-4 h-4" />Back to VMs
          </Link>
        </nav>
        <SectionError message={(vmError as Error)?.message || "VM not found"} onRetry={() => refetchVM()} />
      </div>
    )
  }

  const ramGB = Math.round(vm.resources.ram / 1024 * 10) / 10

  return (
    <div className="max-w-7xl mx-auto">
      {/* Breadcrumb */}
      <nav className="flex items-center gap-2 mb-6">
        <Link href="/vms" className="flex items-center gap-2 text-sm font-bold uppercase text-gray-500 hover:text-black transition-colors w-fit">
          <ArrowLeft className="w-4 h-4" />Back to VMs
        </Link>
      </nav>

      {/* Header */}
      <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
        <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4">
          <div className="flex items-center gap-4">
            <div className="w-16 h-16 bg-primary flex items-center justify-center border-4 border-black shadow-neo">
              <Monitor className="w-8 h-8" />
            </div>
            <div>
              <h1 className="text-2xl lg:text-3xl font-black uppercase tracking-tight text-black">{vm.hostname}</h1>
              <div className="flex items-center gap-3 mt-2">
                <StatusBadge status={vm.status} />
                <span className="text-xs font-medium text-gray-500">ID: {vm.id.slice(0, 12)}</span>
              </div>
            </div>
          </div>
          <div className="flex flex-wrap gap-3">
            {vm.status === "running" ? (
              <>
                <Button variant="secondary" size="sm" onClick={() => handleAction("stop")} disabled={actionLoading !== null}>
                  {actionLoading === "stop" ? <Loader2 className="w-4 h-4 animate-spin" /> : <Square className="w-4 h-4" />}
                  Stop
                </Button>
                <Button variant="secondary" size="sm" onClick={() => handleAction("restart")} disabled={actionLoading !== null}>
                  {actionLoading === "restart" ? <Loader2 className="w-4 h-4 animate-spin" /> : <RotateCcw className="w-4 h-4" />}
                  Restart
                </Button>
              </>
            ) : (
              <Button variant="default" size="sm" onClick={() => handleAction("start")} disabled={actionLoading !== null}>
                {actionLoading === "start" ? <Loader2 className="w-4 h-4 animate-spin" /> : <Play className="w-4 h-4" />}
                Start
              </Button>
            )}

            {/* Rebuild Dialog */}
            <Dialog open={rebuildDialogOpen} onOpenChange={setRebuildDialogOpen}>
              <DialogTrigger asChild>
                <Button variant="secondary" size="sm"><RefreshCw className="w-4 h-4" />Rebuild</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Rebuild VM</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    This will destroy the current VM and create a new one from a template. All data will be lost.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">Select Template</span>
                    <div className="grid grid-cols-2 gap-2 max-h-48 overflow-y-auto">
                      {templates?.map((t) => (
                        <button key={t.id} type="button" onClick={() => setSelectedTemplate(t.id)}
                          className={`p-3 border-2 border-black text-left transition-all ${selectedTemplate === t.id ? "bg-primary shadow-neo" : "bg-white hover:bg-gray-50"}`}>
                          <span className="text-xs font-bold uppercase block">{t.name}</span>
                          <span className="text-[10px] text-gray-500">v{t.version}</span>
                        </button>
                      ))}
                    </div>
                  </div>
                  <div className="bg-danger/10 border-2 border-danger p-4">
                    <p className="text-xs font-bold text-danger uppercase mb-2">Warning: Data Loss</p>
                    <p className="text-xs text-gray-700">All data on this VM will be permanently deleted.</p>
                  </div>
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">
                      Type <span className="font-black">{vm.hostname}</span> to confirm
                    </span>
                    <Input value={confirmVMName} onChange={(e) => setConfirmVMName(e.target.value)} placeholder="Enter VM hostname" className="border-2 border-black" />
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setRebuildDialogOpen(false)}>Cancel</Button>
                  <Button variant="destructive" disabled={confirmVMName !== vm.hostname || !selectedTemplate} onClick={handleRebuild}>
                    <RefreshCw className="w-4 h-4" />Rebuild VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Delete Dialog */}
            <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
              <DialogTrigger asChild>
                <Button variant="destructive" size="sm"><Trash2 className="w-4 h-4" />Delete</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Delete VM</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">Are you sure you want to delete this VM?</DialogDescription>
                </DialogHeader>
                <div className="bg-danger/10 border-2 border-danger p-4">
                  <p className="text-xs font-bold text-danger uppercase mb-2">Permanent Data Loss</p>
                  <p className="text-xs text-gray-700">The VM &quot;{vm.hostname}&quot; will be permanently deleted.</p>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setDeleteDialogOpen(false)}>Cancel</Button>
                  <Button variant="destructive" onClick={handleDelete} disabled={deleteVM.isPending}>
                    {deleteVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Trash2 className="w-4 h-4" />}
                    Delete VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          </div>
        </div>
      </div>

      {/* Tabs */}
      <Tabs defaultValue="overview" className="space-y-6">
        <TabsList className="mb-2">
          <TabsTrigger value="overview"><Monitor className="w-4 h-4 mr-2" />Overview</TabsTrigger>
          <TabsTrigger value="console"><Terminal className="w-4 h-4 mr-2" />Console</TabsTrigger>
          <TabsTrigger value="network"><Network className="w-4 h-4 mr-2" />Network</TabsTrigger>
          <TabsTrigger value="snapshots"><Database className="w-4 h-4 mr-2" />Snapshots</TabsTrigger>
          <TabsTrigger value="backups"><HardDrive className="w-4 h-4 mr-2" />Backups</TabsTrigger>
          <TabsTrigger value="firewall"><Shield className="w-4 h-4 mr-2" />Firewall</TabsTrigger>
          <TabsTrigger value="logs"><FileText className="w-4 h-4 mr-2" />Logs</TabsTrigger>
        </TabsList>

        {/* Overview Tab */}
        <TabsContent value="overview" className="space-y-6">
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            {/* Resource Usage */}
            <div className="lg:col-span-2 bg-white border-4 border-black p-6 shadow-neo">
              <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6 flex items-center gap-2">
                <Activity className="w-5 h-5" />Resource Usage
              </h2>
              {metrics ? (
                <div className="space-y-6">
                  <ProgressBar
                    value={Math.round(metrics.cpu_percent)}
                    max={100}
                    label="CPU"
                    color={metrics.cpu_percent > 80 ? "danger" : "primary"}
                  />
                  <ProgressBar
                    value={Math.round(metrics.memory_used / (1024 * 1024))}
                    max={Math.round(metrics.memory_total / (1024 * 1024))}
                    label="Memory"
                    unit="MB"
                    color={metrics.memory_used_percent > 80 ? "danger" : "secondary"}
                  />
                  <ProgressBar
                    value={vm.resources.disk}
                    max={vm.resources.disk}
                    label="Disk (Allocated)"
                    unit="GB"
                    color="accent"
                  />
                  <div className="grid grid-cols-2 gap-4 mt-4 pt-4 border-t-2 border-black">
                    <div className="p-3 border-2 border-black bg-gray-50">
                      <span className="text-xs font-bold uppercase text-gray-500 block">Disk Read</span>
                      <span className="text-sm font-black">{formatBytesPerSec(metrics.disk_read_bytes_per_sec)}</span>
                    </div>
                    <div className="p-3 border-2 border-black bg-gray-50">
                      <span className="text-xs font-bold uppercase text-gray-500 block">Disk Write</span>
                      <span className="text-sm font-black">{formatBytesPerSec(metrics.disk_write_bytes_per_sec)}</span>
                    </div>
                    <div className="p-3 border-2 border-black bg-gray-50">
                      <span className="text-xs font-bold uppercase text-gray-500 block">Network RX</span>
                      <span className="text-sm font-black">{formatBytesPerSec(metrics.network_rx_bytes_per_sec)}</span>
                    </div>
                    <div className="p-3 border-2 border-black bg-gray-50">
                      <span className="text-xs font-bold uppercase text-gray-500 block">Network TX</span>
                      <span className="text-sm font-black">{formatBytesPerSec(metrics.network_tx_bytes_per_sec)}</span>
                    </div>
                  </div>
                </div>
              ) : (
                <div className="space-y-6">
                  <ProgressBar value={0} max={100} label="CPU" color="primary" />
                  <ProgressBar value={0} max={vm.resources.ram} label="Memory" unit="MB" color="secondary" />
                  <ProgressBar value={vm.resources.disk} max={vm.resources.disk} label="Disk (Allocated)" unit="GB" color="accent" />
                  <p className="text-xs text-gray-400 italic">Live metrics available when VM is running</p>
                </div>
              )}
            </div>

            {/* VM Details Sidebar */}
            <div className="bg-white border-4 border-black p-6 shadow-neo">
              <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6 flex items-center gap-2">
                <Server className="w-5 h-5" />VM Details
              </h2>
              <div className="space-y-4">
                <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500">Node</span>
                  <span className="text-sm font-black">{vm.node_id}</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500">OS Template</span>
                  <span className="text-sm font-black">{vm.os_template_id}</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500">CPU</span>
                  <span className="text-sm font-black">{vm.resources.cpu} vCPU</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500">RAM</span>
                  <span className="text-sm font-black">{ramGB} GB</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500">Disk</span>
                  <span className="text-sm font-black">{vm.resources.disk} GB</span>
                </div>
                {vm.vnc_port && (
                  <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500">VNC Port</span>
                    <div className="flex items-center gap-1">
                      <span className="text-sm font-black font-mono">{vm.vnc_port}</span>
                      <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => copyToClipboard(vm.vnc_port!.toString())}>
                        {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                      </Button>
                    </div>
                  </div>
                )}
                <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500">Created</span>
                  <span className="text-sm font-medium">{formatDate(vm.created_at)}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-xs font-bold uppercase text-gray-500">Updated</span>
                  <span className="text-sm font-medium">{formatDate(vm.updated_at)}</span>
                </div>
              </div>
            </div>
          </div>

          {/* VNC Access */}
          {vm.vnc_port && vm.status === "running" && (
            <div className="bg-white border-4 border-black p-6 shadow-neo">
              <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6 flex items-center gap-2">
                <Monitor className="w-5 h-5" />VNC Access
              </h2>
              <div className="flex items-center justify-between">
                <div>
                  <span className="text-xs font-bold uppercase text-gray-500 block mb-1">VNC Port</span>
                  <span className="text-xl font-black font-mono">{vm.vnc_port}</span>
                </div>
                <Link href={`/vms/${vm.id}/console`} target="_blank">
                  <Button variant="accent" className="gap-2">
                    <ExternalLink className="w-4 h-4" />
                    Open VNC Console
                  </Button>
                </Link>
              </div>
            </div>
          )}
        </TabsContent>

        {/* Console Tab */}
        <TabsContent value="console">
          <div className="bg-white border-4 border-black p-6 shadow-neo">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-black uppercase tracking-tight text-black flex items-center gap-2">
                <Terminal className="w-5 h-5" />Console
              </h2>
              <Link href={`/vms/${vm.id}/console`} target="_blank">
                <Button variant="secondary" size="sm">
                  <ExternalLink className="w-4 h-4 mr-2" />Open in New Tab
                </Button>
              </Link>
            </div>
            {vm.status === "running" ? (
              <div className="bg-black border-4 border-black p-8 h-64 flex items-center justify-center">
                <div className="text-center">
                  <Terminal className="w-12 h-12 mx-auto text-green-400 mb-4" />
                  <p className="text-green-400 font-mono text-sm mb-4">Console available</p>
                  <Link href={`/vms/${vm.id}/console`} target="_blank">
                    <Button variant="default" className="gap-2">
                      <ExternalLink className="w-4 h-4" />
                      Connect to Console
                    </Button>
                  </Link>
                </div>
              </div>
            ) : (
              <div className="bg-gray-100 border-4 border-black p-8 h-64 flex items-center justify-center">
                <div className="text-center">
                  <Terminal className="w-12 h-12 mx-auto text-gray-300 mb-4" />
                  <p className="text-gray-500 font-bold uppercase text-sm">VM is not running</p>
                  <p className="text-gray-400 text-xs mt-1">Start the VM to access the console</p>
                </div>
              </div>
            )}
          </div>
        </TabsContent>

        {/* Network Tab */}
        <TabsContent value="network">
          <div className="space-y-6">
            <div className="bg-white border-4 border-black p-6 shadow-neo">
              <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6 flex items-center gap-2">
                <Network className="w-5 h-5" />Network Configuration
              </h2>
              <div className="space-y-4">
                <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
                  <div className="p-4 border-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Node</span>
                    <span className="text-sm font-mono font-bold">{vm.node_id}</span>
                  </div>
                  <div className="p-4 border-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500 block">VNC Port</span>
                    <span className="text-sm font-mono font-bold">{vm.vnc_port || "N/A"}</span>
                  </div>
                  <div className="p-4 border-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Status</span>
                    <span className="text-sm font-bold uppercase">{vm.status}</span>
                  </div>
                  <div className="p-4 border-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500 block">Resources</span>
                    <span className="text-sm font-bold">{vm.resources.cpu}C / {ramGB}GB</span>
                  </div>
                </div>
              </div>
            </div>

            {/* Bandwidth Usage */}
            <BandwidthUsageCard vmId={vmId} />
          </div>
        </TabsContent>

        {/* Snapshots Tab */}
        <TabsContent value="snapshots">
          <div className="bg-white border-4 border-black p-6 shadow-neo">
            <div className="flex items-center justify-between mb-6">
              <h2 className="text-lg font-black uppercase tracking-tight text-black flex items-center gap-2">
                <Database className="w-5 h-5" />Snapshots
              </h2>
              <Dialog open={createSnapshotOpen} onOpenChange={setCreateSnapshotOpen}>
                <DialogTrigger asChild>
                  <Button variant="default" size="sm"><Plus className="w-4 h-4 mr-2" />Create Snapshot</Button>
                </DialogTrigger>
                <DialogContent className="max-w-sm border-4 border-black shadow-neo-xl">
                  <DialogHeader>
                    <DialogTitle className="text-lg font-black uppercase">Create Snapshot</DialogTitle>
                  </DialogHeader>
                  <div className="space-y-3 py-2">
                    <label htmlFor="snapshot-name" className="text-xs font-bold uppercase text-gray-600 block">Snapshot Name</label>
                    <Input id="snapshot-name" value={snapshotName} onChange={(e) => setSnapshotName(e.target.value)} placeholder="e.g., before-update" className="border-2 border-black" />
                  </div>
                  <DialogFooter>
                    <Button variant="ghost" onClick={() => setCreateSnapshotOpen(false)}>Cancel</Button>
                    <Button onClick={handleCreateSnapshot} disabled={!snapshotName.trim() || createSnapshot.isPending}>
                      {createSnapshot.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Plus className="w-4 h-4" />}
                      Create
                    </Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
            </div>

            {snapshotsLoading ? (
              <SectionSkeleton />
            ) : snapshotsError ? (
              <SectionError message={(snapshotsError as Error).message} onRetry={() => refetchSnapshots()} />
            ) : !snapshots?.length ? (
              <SectionEmpty icon={Database} message="No snapshots yet. Create one to save the current VM state." />
            ) : (
              <div className="space-y-3">
                {snapshots.map((snap: Snapshot) => (
                  <div key={snap.id} className="flex items-center justify-between p-4 border-2 border-black hover:bg-gray-50">
                    <div className="flex items-center gap-4">
                      <Database className="w-5 h-5" />
                      <div>
                        <span className="font-black">{snap.name}</span>
                        <span className="text-xs text-gray-500 ml-2">{formatDate(snap.created_at)}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className={`text-xs font-bold uppercase px-2 py-1 border border-black ${
                        snap.status === "completed" ? "bg-success" : snap.status === "failed" ? "bg-danger text-white" : "bg-gray-200"
                      }`}>
                        {snap.status}
                      </span>
                      <Button variant="ghost" size="sm" onClick={() => handleRestoreSnapshot(snap.id)} disabled={restoreSnapshot.isPending}>
                        Restore
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => handleDeleteSnapshot(snap.id)} disabled={deleteSnapshot.isPending}>
                        Delete
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </TabsContent>

        {/* Backups Tab */}
        <TabsContent value="backups">
          <div className="bg-white border-4 border-black p-6 shadow-neo">
            <div className="flex items-center justify-between mb-6">
              <h2 className="text-lg font-black uppercase tracking-tight text-black flex items-center gap-2">
                <HardDrive className="w-5 h-5" />Backups
              </h2>
              <Button variant="default" size="sm" onClick={handleCreateBackup} disabled={createBackup.isPending}>
                {createBackup.isPending ? <Loader2 className="w-4 h-4 mr-2 animate-spin" /> : <Plus className="w-4 h-4 mr-2" />}
                Create Backup
              </Button>
            </div>

            {backupsLoading ? (
              <SectionSkeleton />
            ) : backupsError ? (
              <SectionError message={(backupsError as Error).message} onRetry={() => refetchBackups()} />
            ) : !backups?.length ? (
              <SectionEmpty icon={HardDrive} message="No backups yet. Create one to backup this VM." />
            ) : (
              <div className="space-y-3">
                {backups.map((backup: Backup) => (
                  <div key={backup.id} className="flex items-center justify-between p-4 border-2 border-black hover:bg-gray-50">
                    <div className="flex items-center gap-4">
                      <HardDrive className="w-5 h-5" />
                      <div>
                        <span className="font-black">{backup.backup_type} backup</span>
                        <span className="text-xs text-gray-500 ml-2">{formatDate(backup.created_at)}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-xs font-bold">{formatBytes(backup.size)}</span>
                      <span className={`text-xs font-bold uppercase px-2 py-1 border border-black ${
                        backup.status === "completed" ? "bg-success" : backup.status === "failed" ? "bg-danger text-white" : backup.status === "in_progress" ? "bg-secondary" : "bg-gray-200"
                      }`}>
                        {backup.status}
                      </span>
                      <Button variant="ghost" size="sm" onClick={() => handleDeleteBackup(backup.id)} disabled={deleteBackupMutation.isPending}>
                        Delete
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </TabsContent>

        {/* Firewall Tab */}
        <TabsContent value="firewall">
          <div className="bg-white border-4 border-black p-6 shadow-neo">
            <div className="flex items-center justify-between mb-6">
              <h2 className="text-lg font-black uppercase tracking-tight text-black flex items-center gap-2">
                <Shield className="w-5 h-5" />Firewall Rules
              </h2>
            </div>

            {firewallLoading ? (
              <SectionSkeleton />
            ) : firewallError ? (
              <SectionError message={(firewallError as Error).message} onRetry={() => refetchFirewall()} />
            ) : !firewallRules?.length ? (
              <SectionEmpty icon={Shield} message="No firewall rules configured." />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full">
                  <thead>
                    <tr className="border-b-4 border-black">
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Protocol</th>
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Port</th>
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Direction</th>
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Source</th>
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Action</th>
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Priority</th>
                      <th className="text-left py-3 px-4 text-xs font-black uppercase">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {firewallRules.map((rule: FirewallRule) => (
                      <tr key={rule.id} className="border-b-2 border-black">
                        <td className="py-3 px-4 font-mono text-sm uppercase">{rule.protocol}</td>
                        <td className="py-3 px-4 font-mono text-sm">{rule.port_range || "*"}</td>
                        <td className="py-3 px-4 text-xs font-bold uppercase">{rule.direction}</td>
                        <td className="py-3 px-4 font-mono text-sm">{rule.source_ip || "Any"}</td>
                        <td className="py-3 px-4">
                          <span className={`text-xs font-bold uppercase px-2 py-1 border border-black ${
                            rule.action === "allow" ? "bg-success" : "bg-danger text-white"
                          }`}>
                            {rule.action}
                          </span>
                        </td>
                        <td className="py-3 px-4 font-mono text-sm">{rule.priority}</td>
                        <td className="py-3 px-4">
                          <Button variant="ghost" size="sm" onClick={() => handleDeleteFirewallRule(rule.id)} disabled={deleteFirewallRule.isPending}>
                            Delete
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </TabsContent>

        {/* Logs Tab */}
        <TabsContent value="logs">
          <div className="bg-white border-4 border-black p-6 shadow-neo">
            <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6 flex items-center gap-2">
              <FileText className="w-5 h-5" />Activity Logs
            </h2>
            <div className="bg-gray-100 border-4 border-black p-8 h-64 flex items-center justify-center">
              <div className="text-center">
                <FileText className="w-12 h-12 mx-auto text-gray-300 mb-4" />
                <p className="text-gray-500 font-bold uppercase text-sm">No activity logs yet for this VM</p>
                <p className="text-gray-400 text-xs mt-1">VM activity logging is under development</p>
              </div>
            </div>
          </div>
        </TabsContent>
      </Tabs>

      {/* Toast */}
      {toast && (
        <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />
      )}
    </div>
  )
}
