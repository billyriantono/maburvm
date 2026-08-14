"use client"

import { useState, useEffect, useCallback } from "react"
import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { 
  Play, Square, RotateCcw, Trash2, RefreshCw, ArrowLeft,
  Copy, Check, HardDrive,
  Monitor, MonitorOff, KeyRound, Server, Network, Database, Shield, FileText, Terminal, Activity, ExternalLink,
  Loader2, AlertCircle, Plus, Disc, CircleSlash, LifeBuoy, ArrowRightLeft, User
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useVM, useVMMetrics, useVMMetricsHistory, useVMAction, useDeleteVM, useAttachISO, useDetachISO, useRescueVM, useUnrescueVM, useMigrateVM, useRegenerateVNCPassword, useSetConsoleEnabled, useRepairConsole, useRebuildVM, useResetPassword, useCloneVM, useUpdateVM, useAssignVMIP, useReleaseVMIP } from "@/lib/hooks/use-vms"
import { useIPPools } from "@/lib/hooks/use-ipam"
import { DeleteProgressDialog } from "@/components/vm-delete-progress"
import { useUsers } from "@/lib/hooks/use-users"
import { useSSHKeys } from "@/lib/hooks/use-ssh-keys"
import { useNodes } from "@/lib/hooks/use-nodes"
import { Sparkline } from "@/components/ui/sparkline"
import { useSnapshots, useCreateSnapshot, useRestoreSnapshot, useDeleteSnapshot } from "@/lib/hooks/use-snapshots"
import { useCreateImage } from "@/lib/hooks/use-images"
import { useBackups, useCreateBackup, useDeleteBackup } from "@/lib/hooks/use-backups"
import { useFirewallRules, useDeleteFirewallRule, useCreateFirewallRule, useVMNetworks, useSetVMBandwidth } from "@/lib/hooks/use-networks"
import type { Network as NetworkIface } from "@/types"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useVMBandwidth, useSetBandwidthQuota } from "@/lib/hooks/use-bandwidth"
import { useVMDisks, useAttachDisk, useDetachDisk } from "@/lib/hooks/use-disks"
import { useVMActivity } from "@/lib/hooks/use-audit-logs"
import { VNCConsole } from "@/components/vnc-console"
import type { VMStatus, Snapshot, Backup, FirewallRule } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

// --- Utility Components ---

// Preset speed tiers (Mbps). 0 = unlimited. Shared shape with the client portal.
const BANDWIDTH_TIERS: { label: string; mbps: number }[] = [
  { label: "100 Mbps", mbps: 100 },
  { label: "500 Mbps", mbps: 500 },
  { label: "1 Gbps", mbps: 1000 },
  { label: "2.5 Gbps", mbps: 2500 },
  { label: "5 Gbps", mbps: 5000 },
  { label: "10 Gbps", mbps: 10000 },
  { label: "Unlimited", mbps: 0 },
]

function formatMbps(mbps: number): string {
  if (mbps <= 0) return "Unlimited"
  if (mbps % 1000 === 0) return `${mbps / 1000} Gbps`
  return `${mbps} Mbps`
}

// BandwidthCell renders a VM interface's speed limit with inline editing.
// Admins pick a preset tier (up to 10 Gbps) or enter a custom value; saving
// re-applies the tc limit on the hypervisor via the bandwidth endpoint.
function BandwidthCell({ vmId, iface }: { vmId: string; iface: NetworkIface }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState<number>(iface.bandwidth_limit)
  const setBandwidth = useSetVMBandwidth(vmId)

  if (!editing) {
    return (
      <div className="flex items-center gap-2">
        <span className="font-mono">{formatMbps(iface.bandwidth_limit)}</span>
        <button
          type="button"
          onClick={() => { setValue(iface.bandwidth_limit); setEditing(true) }}
          className="text-[10px] font-semibold underline text-muted-foreground hover:text-primary"
        >
          Edit
        </button>
      </div>
    )
  }

  const apply = () => {
    const mbps = Math.max(0, Math.min(10000, Math.floor(value) || 0))
    setBandwidth.mutate(
      { networkId: iface.id, bandwidthMbps: mbps },
      { onSuccess: () => setEditing(false) },
    )
  }

  return (
    <div className="flex flex-wrap items-center gap-1">
      <select
        value={BANDWIDTH_TIERS.some((t) => t.mbps === value) ? value : "custom"}
        onChange={(e) => { if (e.target.value !== "custom") setValue(Number(e.target.value)) }}
        className="h-8 rounded-md border border-input bg-background text-xs font-medium px-1"
      >
        {BANDWIDTH_TIERS.map((t) => (
          <option key={t.mbps} value={t.mbps}>{t.label}</option>
        ))}
        <option value="custom">Custom…</option>
      </select>
      <input
        type="number"
        min={0}
        max={10000}
        value={value}
        onChange={(e) => setValue(Number(e.target.value))}
        className="h-8 w-20 rounded-md border border-input bg-background text-xs font-mono px-1"
        aria-label="Bandwidth in Mbps"
      />
      <span className="text-[10px] font-medium text-muted-foreground">Mbps</span>
      <button
        type="button"
        onClick={apply}
        disabled={setBandwidth.isPending}
        className="h-8 px-2 rounded-md bg-primary text-primary-foreground text-xs font-medium hover:bg-primary/90 disabled:opacity-50"
      >
        {setBandwidth.isPending ? "…" : "Save"}
      </button>
      <button
        type="button"
        onClick={() => setEditing(false)}
        className="h-8 px-2 rounded-md border bg-background text-xs font-medium hover:bg-muted"
      >
        ✕
      </button>
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

function ProgressBar({ value, max, label, unit, color = "primary" }: { value: number; max: number; label: string; unit?: string; color?: "primary" | "secondary" | "accent" | "success" | "danger" }) {
  const percentage = max > 0 ? Math.round((value / max) * 100) : 0
  const colorClasses = { primary: "bg-primary", secondary: "bg-primary", accent: "bg-primary", success: "bg-emerald-500", danger: "bg-destructive" }
  return (
    <div className="mb-4">
      <div className="flex justify-between items-center mb-1">
        <span className="text-xs font-medium text-muted-foreground">{label}</span>
        <span className="text-xs font-semibold">{unit ? `${value} / ${max} ${unit}` : `${percentage}%`}</span>
      </div>
      <div className="h-2 rounded-full bg-muted overflow-hidden relative">
        <div className={`h-full ${colorClasses[color]} transition-all duration-300`} style={{ width: `${Math.min(percentage, 100)}%` }} />
      </div>
    </div>
  )
}

function StatusBadge({ status }: { status: VMStatus }) {
  const statusConfig: Record<string, { cls: string; dot: string; label: string }> = {
    running: { cls: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900", dot: "bg-emerald-500 animate-pulse", label: "Running" },
    stopped: { cls: "bg-muted text-muted-foreground border", dot: "bg-muted-foreground", label: "Stopped" },
    suspended: { cls: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900", dot: "bg-amber-500", label: "Suspended" },
    creating: { cls: "bg-sky-50 text-sky-700 border-sky-200 dark:bg-sky-950 dark:text-sky-300 dark:border-sky-900", dot: "bg-sky-500", label: "Creating" },
    deleting: { cls: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900", dot: "bg-amber-500", label: "Deleting" },
    error: { cls: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900", dot: "bg-red-500", label: "Error" },
  }
  const config = statusConfig[status] || statusConfig.stopped
  return (
    <span className={`inline-flex items-center rounded-md px-2 py-0.5 border text-xs font-medium ${config.cls}`}>
      <span className={`w-1.5 h-1.5 mr-1.5 rounded-full ${config.dot}`} />
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
    <div className={`fixed bottom-4 right-4 z-50 px-4 py-3 border rounded-lg shadow-md bg-background ${
      type === "success" ? "text-emerald-700 dark:text-emerald-300" : "text-destructive"
    }`}>
      <p className="font-medium text-sm">{message}</p>
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
      <AlertCircle className="w-10 h-10 mx-auto text-destructive mb-3" />
      <p className="font-medium text-sm mb-1">Failed to load</p>
      <p className="text-muted-foreground text-xs mb-3">{message}</p>
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
      <Icon className="w-10 h-10 mx-auto text-muted-foreground mb-3" />
      <p className="text-muted-foreground text-sm font-medium">{message}</p>
    </div>
  )
}

function DisksCard({ vmId }: { vmId: string }) {
  const confirm = useConfirm()
  const { data: disks, isLoading } = useVMDisks(vmId)
  const attach = useAttachDisk(vmId)
  const detach = useDetachDisk(vmId)
  const [adding, setAdding] = useState(false)
  const [sizeGB, setSizeGB] = useState("10")
  const [err, setErr] = useState("")

  const handleAttach = async () => {
    const gb = parseInt(sizeGB, 10)
    if (Number.isNaN(gb) || gb <= 0) { setErr("Enter a size in GB"); return }
    setErr("")
    try {
      await attach.mutateAsync(gb)
      setAdding(false)
      setSizeGB("10")
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  // Genuinely three-way, which is why window.confirm could not express it: the
  // old dialog put "keep the volume" on the Cancel button, so backing out of the
  // decision detached the disk anyway. Cancel now means cancel.
  const handleDetach = async (device: string) => {
    const answer = await confirm.choose({
      title: `Detach ${device}?`,
      description:
        "Detaching removes the disk from this VM. You can either keep the volume on the node to reattach later, or delete its data for good.",
      confirmLabel: "Detach and delete data",
      alternateLabel: "Detach, keep volume",
      destructive: true,
      action: (choice) => detach.mutateAsync({ device, deleteVolume: choice === "confirm" }),
    })
    if (answer === "cancel") return
  }

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
          <HardDrive className="w-5 h-5" />Data Disks
        </h2>
        <Button size="sm" onClick={() => setAdding((v) => !v)}><Plus className="w-4 h-4" />Add Disk</Button>
      </div>

      {adding && (
        <div className="flex flex-wrap items-end gap-2 mb-4 p-3 border rounded-md bg-muted">
          <div>
            <label htmlFor="disk-size" className="text-xs font-medium text-muted-foreground block mb-1">Size (GB)</label>
            <Input id="disk-size" type="number" min={1} value={sizeGB} onChange={(e) => setSizeGB(e.target.value)} className="border w-32 font-mono" />
          </div>
          <Button size="sm" onClick={handleAttach} disabled={attach.isPending}>
            {attach.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Check className="w-4 h-4" />}Attach
          </Button>
          <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setErr("") }}>Cancel</Button>
        </div>
      )}
      {err && <p className="text-xs text-destructive font-medium mb-3">{err}</p>}

      {isLoading ? (
        <SectionSkeleton />
      ) : !disks?.length ? (
        <div className="border border-dashed border-gray-300 p-6 text-center text-sm font-medium text-muted-foreground">
          No extra data disks. The boot disk isn&apos;t listed here.
        </div>
      ) : (
        <div className="border overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted text-muted-foreground border-b font-medium text-xs">
                <th className="text-left p-3">Device</th>
                <th className="text-left p-3">Size</th>
                <th className="text-left p-3">Path</th>
                <th className="text-right p-3">Action</th>
              </tr>
            </thead>
            <tbody>
              {disks.map((d, i) => (
                <tr key={d.id} className={`border-t ${i % 2 ? "bg-muted/50" : "bg-card"}`}>
                  <td className="p-3 font-mono font-medium">{d.device}</td>
                  <td className="p-3 font-mono">{d.size_gb} GB</td>
                  <td className="p-3 font-mono text-xs truncate max-w-[280px]" title={d.path}>{d.path}</td>
                  <td className="p-3 text-right">
                    <Button size="sm" variant="destructive" onClick={() => handleDetach(d.device)} disabled={detach.isPending}>
                      <Trash2 className="w-4 h-4" />Detach
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="text-xs text-muted-foreground mt-3">Disks hot-plug on running VMs; format &amp; mount inside the guest after attaching.</p>
    </div>
  )
}

function BandwidthUsageCard({ vmId }: { vmId: string }) {
  const { data: bandwidth, isLoading, error } = useVMBandwidth(vmId)
  const setQuota = useSetBandwidthQuota(vmId)
  const [editingQuota, setEditingQuota] = useState(false)
  const [quotaInput, setQuotaInput] = useState("")

  if (isLoading) return <SectionSkeleton />
  if (error || !bandwidth) return null

  const handleSaveQuota = async () => {
    const gb = parseInt(quotaInput, 10)
    if (Number.isNaN(gb) || gb < 0) return
    try {
      await setQuota.mutateAsync(gb)
      setEditingQuota(false)
    } catch {
      // surfaced by react-query; keep the editor open
    }
  }

  const usagePercent = bandwidth.quota_gb > 0 ? Math.min(bandwidth.usage_percent, 100) : 0
  const isWarning = bandwidth.quota_gb > 0 && bandwidth.usage_percent >= 80
  const isDanger = bandwidth.exceeded

  return (
    <div className={`bg-card text-card-foreground border rounded-lg p-6 shadow-sm ${isDanger ? 'border-red-300 dark:border-red-900' : ''}`}>
      <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
        <Activity className="w-5 h-5" />Bandwidth Usage
        {isDanger && <span className="text-xs rounded-md bg-red-50 text-red-700 border border-red-200 px-2 py-0.5 dark:bg-red-950 dark:text-red-300 dark:border-red-900">Exceeded</span>}
      </h2>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
        <div className="p-4 border rounded-md">
          <span className="text-xs font-medium text-muted-foreground block">Download</span>
          <span className="text-sm font-mono font-medium">{formatBytes(bandwidth.rx_bytes)}</span>
        </div>
        <div className="p-4 border rounded-md">
          <span className="text-xs font-medium text-muted-foreground block">Upload</span>
          <span className="text-sm font-mono font-medium">{formatBytes(bandwidth.tx_bytes)}</span>
        </div>
        <div className="p-4 border rounded-md">
          <span className="text-xs font-medium text-muted-foreground block">Total Used</span>
          <span className="text-sm font-mono font-medium">{bandwidth.used_gb.toFixed(2)} GB</span>
        </div>
        <div className="p-4 border rounded-md">
          <span className="text-xs font-medium text-muted-foreground block">Quota</span>
          <span className="text-sm font-mono font-medium">{bandwidth.quota_gb > 0 ? `${bandwidth.quota_gb} GB` : 'Unlimited'}</span>
        </div>
      </div>

      {bandwidth.quota_gb > 0 && (
        <div>
          <div className="flex justify-between items-center mb-1">
            <span className="text-xs font-medium text-muted-foreground">Usage</span>
            <span className="text-xs font-semibold">{bandwidth.used_gb.toFixed(2)} / {bandwidth.quota_gb} GB ({bandwidth.usage_percent.toFixed(1)}%)</span>
          </div>
          <div className="h-2 rounded-full bg-muted overflow-hidden relative">
            <div
              className={`h-full transition-all duration-300 ${isDanger ? 'bg-destructive' : isWarning ? 'bg-amber-500' : 'bg-emerald-500'}`}
              style={{ width: `${usagePercent}%` }}
            />
          </div>
          <div className="flex justify-between mt-1">
            <span className="text-xs text-muted-foreground">{bandwidth.period_start}</span>
            <span className="text-xs text-muted-foreground">{bandwidth.period_end}</span>
          </div>
        </div>
      )}

      {/* Monthly quota control */}
      <div className="mt-6 pt-4 border-t border-gray-200">
        {editingQuota ? (
          <div className="flex flex-wrap items-end gap-2">
            <div>
              <label htmlFor="bw-quota" className="text-xs font-medium text-muted-foreground block mb-1">Monthly quota (GB, 0 = unlimited)</label>
              <Input id="bw-quota" type="number" min={0} value={quotaInput} onChange={(e) => setQuotaInput(e.target.value)} className="border w-40 font-mono" />
            </div>
            <Button size="sm" onClick={handleSaveQuota} disabled={setQuota.isPending}>
              {setQuota.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Check className="w-4 h-4" />}Save
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setEditingQuota(false)}>Cancel</Button>
          </div>
        ) : (
          <Button size="sm" variant="secondary" className="border" onClick={() => { setQuotaInput(String(bandwidth.quota_gb || 0)); setEditingQuota(true) }}>
            <Activity className="w-4 h-4" />Set monthly quota
          </Button>
        )}
      </div>
    </div>
  )
}

// --- Main Component ---

export default function VMDetailPage() {
  const confirm = useConfirm()
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
  const [consoleActive, setConsoleActive] = useState(false)
  const [snapshotName, setSnapshotName] = useState("")
  const [createSnapshotOpen, setCreateSnapshotOpen] = useState(false)
  const [imageName, setImageName] = useState("")
  const [createImageOpen, setCreateImageOpen] = useState(false)
  const [isoDialogOpen, setIsoDialogOpen] = useState(false)
  const [selectedISOUrl, setSelectedISOUrl] = useState("")
  const [manualISOUrl, setManualISOUrl] = useState("")
  const [migrateOpen, setMigrateOpen] = useState(false)
  const [migrateNodeId, setMigrateNodeId] = useState("")
  // Rebuild options
  const [rebuildPassword, setRebuildPassword] = useState("")
  const [rebuildRegenPassword, setRebuildRegenPassword] = useState(false)
  const [rebuildKeyIds, setRebuildKeyIds] = useState<string[]>([])
  // Reset root password
  const [resetPwOpen, setResetPwOpen] = useState(false)
  const [resetPwValue, setResetPwValue] = useState("")
  // Clone
  const [cloneOpen, setCloneOpen] = useState(false)
  const [cloneHostname, setCloneHostname] = useState("")
  const [cloneNodeId, setCloneNodeId] = useState("")

  // Data hooks
  const { data: vm, isLoading: vmLoading, error: vmError, refetch: refetchVM } = useVM(vmId, {
    // Auto-poll while the VM is in a transitional state (e.g. provisioning) so the
    // page flips to running on its own instead of showing a stale "stopped".
    refetchInterval: (query) => {
      const s = (query.state.data as { status?: string } | undefined)?.status
      return s && ["creating", "deleting"].includes(s) ? 3000 : false
    },
  } as Parameters<typeof useVM>[1])
  const { data: activity, isLoading: activityLoading } = useVMActivity(vmId)
  const { data: metrics } = useVMMetrics(vmId)
  const { data: vmHistory } = useVMMetricsHistory(vmId, 60)
  const cpuTrend = (vmHistory ?? []).map((s) => s.cpu_usage)
  const memTrend = (vmHistory ?? []).map((s) => s.memory_usage)
  const vmAction = useVMAction(vmId)
  const deleteVM = useDeleteVM()
  const attachISO = useAttachISO(vmId)
  const detachISO = useDetachISO(vmId)
  const rescueVM = useRescueVM(vmId)
  const unrescueVM = useUnrescueVM(vmId)
  const migrateVM = useMigrateVM(vmId)
  const regenerateVNC = useRegenerateVNCPassword(vmId)
  const setConsole = useSetConsoleEnabled(vmId)
  const repairConsole = useRepairConsole(vmId)
  const rebuildVM = useRebuildVM(vmId)
  const resetPassword = useResetPassword(vmId)
  const cloneVM = useCloneVM(vmId)
  const { data: sshKeys } = useSSHKeys()
  const { data: allNodes } = useNodes()
  const { data: usersData } = useUsers({ pageSize: 200 })
  const updateVM = useUpdateVM(vmId)
  const [reassignOpen, setReassignOpen] = useState(false)
  const [reassignUserId, setReassignUserId] = useState("")
  const handleReassign = useCallback(async () => {
    if (!reassignUserId) return
    try {
      await updateVM.mutateAsync({ user_id: reassignUserId })
      setReassignOpen(false)
      setReassignUserId("")
      setToast({ message: "VM owner reassigned", type: "success" })
      refetchVM()
    } catch (err) {
      setToast({ message: `Reassign failed: ${(err as Error).message}`, type: "error" })
    }
  }, [updateVM, reassignUserId, refetchVM])
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
  const { data: isoTemplates } = useTemplates({ type: "iso" })
  const { data: vmNetworks } = useVMNetworks(vmId)
  const { data: ipPools } = useIPPools()
  const assignIP = useAssignVMIP(vmId)
  const releaseIP = useReleaseVMIP(vmId)
  const [assignIPOpen, setAssignIPOpen] = useState(false)
  // Set once a delete has been accepted; drives the progress dialog.
  const [deletingVM, setDeletingVM] = useState<{ id: string; hostname: string } | null>(null)
  const [assignPoolID, setAssignPoolID] = useState("")

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
      setDeleteDialogOpen(false)
      // Follow the delete to the end instead of announcing success and leaving.
      // Accepting the job says nothing about whether the machine was removed:
      // the delete that failed on a domain with snapshots reported "VM deleted"
      // here while the VM was still on the node.
      setDeletingVM({ id: vmId, hostname: vm?.hostname ?? vmId })
    } catch (err) {
      setToast({ message: `Failed to delete VM: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteVM, vmId, vm?.hostname])

  const handleAttachISO = useCallback(async () => {
    const url = (manualISOUrl.trim() || selectedISOUrl).trim()
    if (!url) {
      setToast({ message: "Pick an ISO or enter a URL", type: "error" })
      return
    }
    try {
      await attachISO.mutateAsync(url)
      setToast({ message: "ISO attach queued — VM will boot from it on next start", type: "success" })
      setIsoDialogOpen(false)
      setSelectedISOUrl("")
      setManualISOUrl("")
    } catch (err) {
      setToast({ message: `Failed to attach ISO: ${(err as Error).message}`, type: "error" })
    }
  }, [attachISO, manualISOUrl, selectedISOUrl])

  const handleDetachISO = useCallback(async () => {
    try {
      await detachISO.mutateAsync()
      setToast({ message: "ISO detach queued — boot order restored to disk", type: "success" })
    } catch (err) {
      setToast({ message: `Failed to detach ISO: ${(err as Error).message}`, type: "error" })
    }
  }, [detachISO])

  const handleRescue = useCallback(async () => {
    try {
      await rescueVM.mutateAsync(undefined)
      setToast({ message: "Rescue ISO attached — start the VM to boot into rescue", type: "success" })
    } catch (err) {
      setToast({ message: `Failed to enter rescue mode: ${(err as Error).message}`, type: "error" })
    }
  }, [rescueVM])

  const handleUnrescue = useCallback(async () => {
    try {
      await unrescueVM.mutateAsync()
      setToast({ message: "Rescue ISO detached — start the VM to boot from disk", type: "success" })
    } catch (err) {
      setToast({ message: `Failed to exit rescue mode: ${(err as Error).message}`, type: "error" })
    }
  }, [unrescueVM])

  const handleRegenerateVNC = useCallback(async () => {
    try {
      const res = await regenerateVNC.mutateAsync()
      setToast({ message: `New VNC password: ${res.vnc_password}`, type: "success" })
    } catch (err) {
      setToast({ message: `Failed to regenerate VNC password: ${(err as Error).message}`, type: "error" })
    }
  }, [regenerateVNC])

  const handleToggleConsole = useCallback(async (enabled: boolean) => {
    try {
      await setConsole.mutateAsync(enabled)
      setToast({ message: enabled ? "Console enabled" : "Console disabled — active sessions dropped", type: "success" })
    } catch (err) {
      setToast({ message: `Failed to update console: ${(err as Error).message}`, type: "error" })
    }
  }, [setConsole])

  const handleMigrate = useCallback(async () => {
    if (!migrateNodeId) {
      setToast({ message: "Select a destination node", type: "error" })
      return
    }
    try {
      await migrateVM.mutateAsync({ dest_node_id: migrateNodeId, live: true, copy_storage: true })
      setToast({ message: "VM migrated to destination node", type: "success" })
      setMigrateOpen(false)
      setMigrateNodeId("")
    } catch (err) {
      setToast({ message: `Migration failed: ${(err as Error).message}`, type: "error" })
    }
  }, [migrateVM, migrateNodeId])

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

  const createImage = useCreateImage()
  const handleCreateImage = useCallback(async () => {
    if (!imageName.trim()) return
    try {
      await createImage.mutateAsync({ vm_id: vmId, name: imageName.trim() })
      setToast({ message: "Image capture started — track progress on the Images page", type: "success" })
      setImageName("")
      setCreateImageOpen(false)
    } catch (err) {
      setToast({ message: `Failed to create image: ${(err as Error).message}`, type: "error" })
    }
  }, [createImage, vmId, imageName])

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

  const createFirewallRule = useCreateFirewallRule(vmId)
  const [fwOpen, setFwOpen] = useState(false)
  const [fwForm, setFwForm] = useState({ protocol: "tcp", port_range: "", action: "allow", direction: "inbound", source_ip: "", priority: 100 })
  const handleAddFirewallRule = useCallback(async () => {
    try {
      await createFirewallRule.mutateAsync({
        vm_id: vmId,
        protocol: fwForm.protocol as "tcp" | "udp" | "icmp" | "all",
        port_range: fwForm.port_range.trim() || undefined,
        action: fwForm.action as "allow" | "deny",
        direction: fwForm.direction as "inbound" | "outbound",
        source_ip: fwForm.source_ip.trim() || undefined,
        priority: fwForm.priority,
      })
      setFwOpen(false)
      setFwForm({ protocol: "tcp", port_range: "", action: "allow", direction: "inbound", source_ip: "", priority: 100 })
      setToast({ message: "Firewall rule added", type: "success" })
      refetchFirewall()
    } catch (err) {
      setToast({ message: `Failed to add rule: ${(err as Error).message}`, type: "error" })
    }
  }, [createFirewallRule, fwForm, vmId, refetchFirewall])
  const handleDeleteFirewallRule = useCallback(async (ruleId: string) => {
    try {
      await deleteFirewallRule.mutateAsync(ruleId)
      setToast({ message: "Firewall rule deleted", type: "success" })
      refetchFirewall()
    } catch (err) {
      setToast({ message: `Failed to delete rule: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteFirewallRule, refetchFirewall])

  const handleRebuild = async () => {
    if (confirmVMName !== vm?.hostname || !selectedTemplate) return
    try {
      const res = await rebuildVM.mutateAsync({
        template_id: selectedTemplate,
        preserve_ip: true,
        password: rebuildPassword || undefined,
        regenerate_password: rebuildRegenPassword,
        ssh_key_ids: rebuildKeyIds.length ? rebuildKeyIds : undefined,
      })
      setRebuildDialogOpen(false)
      setConfirmVMName("")
      setSelectedTemplate("")
      setRebuildPassword("")
      setRebuildRegenPassword(false)
      setRebuildKeyIds([])
      setToast({
        message: res.root_password
          ? `Rebuild initiated — new root password: ${res.root_password}`
          : "Rebuild initiated",
        type: "success",
      })
    } catch (err) {
      setToast({ message: `Failed to rebuild VM: ${(err as Error).message}`, type: "error" })
    }
  }

  const handleResetPassword = async () => {
    if (!resetPwValue.trim()) return
    try {
      await resetPassword.mutateAsync(resetPwValue)
      setResetPwOpen(false)
      setResetPwValue("")
      setToast({ message: "Root password reset enqueued (applied via guest agent)", type: "success" })
    } catch (err) {
      setToast({ message: `Failed to reset password: ${(err as Error).message}`, type: "error" })
    }
  }

  const handleClone = async () => {
    try {
      const res = await cloneVM.mutateAsync({
        hostname: cloneHostname.trim() || undefined,
        // Only send when targeting a different node; same node = local copy.
        dest_node_id: cloneNodeId && cloneNodeId !== vm?.node_id ? cloneNodeId : undefined,
      })
      setCloneOpen(false)
      setCloneHostname("")
      setCloneNodeId("")
      setToast({ message: `Clone "${res.vm.hostname}" created — provisioning…`, type: "success" })
      if (res.vm?.id) router.push(`/vms/${res.vm.id}`)
    } catch (err) {
      setToast({ message: `Failed to clone VM: ${(err as Error).message}`, type: "error" })
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
          <Link href="/vms" className="flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors w-fit">
            <ArrowLeft className="w-4 h-4" />Back to VMs
          </Link>
        </nav>
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
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
          <Link href="/vms" className="flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors w-fit">
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
        <Link href="/vms" className="flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors w-fit">
          <ArrowLeft className="w-4 h-4" />Back to VMs
        </Link>
      </nav>

      {/* Header (identity) */}
      <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-4">
        <div className="flex items-center gap-4">
            <div className="w-16 h-16 bg-muted text-foreground flex items-center justify-center rounded-lg border">
              <Monitor className="w-8 h-8" />
            </div>
            <div>
              <h1 className="text-2xl lg:text-3xl font-semibold text-foreground">{vm.hostname}</h1>
              <div className="flex items-center gap-3 mt-2">
                <StatusBadge status={vm.status} />
                {vm.rescue_mode && (
                  <span className="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[10px] font-medium border border-amber-200 bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900">
                    <LifeBuoy className="w-3 h-3" />
                    Rescue
                  </span>
                )}
                <span className="text-xs font-medium text-muted-foreground">ID: {vm.id.slice(0, 12)}</span>
              </div>
            </div>
        </div>
      </div>

      {/* Actions */}
      <div className="bg-card text-card-foreground border rounded-lg shadow-sm mb-6">
        <div className="p-3 bg-muted text-muted-foreground border-b font-medium text-xs">Actions</div>
        <div className="p-4 flex flex-wrap gap-3">
            {(["creating", "deleting"] as string[]).includes(vm.status) ? (
              // Transitional: the VM is mid-operation. Don't offer Start/Stop (which
              // would be ambiguous or conflict) — show the in-progress state instead.
              <Button variant="secondary" size="sm" disabled>
                <Loader2 className="w-4 h-4 animate-spin" />
                {vm.status === "creating" ? "Provisioning…" : "Deleting…"}
              </Button>
            ) : vm.status === "running" ? (
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
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Rebuild VM</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    This will destroy the current VM and create a new one from a template. All data will be lost.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4 max-h-[60vh] overflow-y-auto">
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">Select Template</span>
                    <div className="grid grid-cols-2 gap-2 max-h-48 overflow-y-auto">
                      {templates?.map((t) => (
                        <button key={t.id} type="button" onClick={() => setSelectedTemplate(t.id)}
                          className={`p-3 border text-left transition-all ${selectedTemplate === t.id ? "border-primary ring-1 ring-primary bg-primary/5" : "bg-card hover:bg-muted/50"}`}>
                          <span className="text-xs font-medium block">{t.name}</span>
                          <span className="text-[10px] text-muted-foreground">v{t.version}</span>
                        </button>
                      ))}
                    </div>
                  </div>

                  {/* Root password */}
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">Root Password</span>
                    <label className="flex items-center gap-2 mb-2 cursor-pointer">
                      <input type="checkbox" checked={rebuildRegenPassword} onChange={(e) => { setRebuildRegenPassword(e.target.checked); if (e.target.checked) setRebuildPassword("") }} className="w-4 h-4" />
                      <span className="text-xs font-medium">Auto-generate a new password</span>
                    </label>
                    <Input
                      type="text"
                      value={rebuildPassword}
                      onChange={(e) => setRebuildPassword(e.target.value)}
                      disabled={rebuildRegenPassword}
                      placeholder={rebuildRegenPassword ? "Will be generated & shown once" : "Leave blank to keep template default"}
                      className="border font-mono text-sm disabled:opacity-50"
                    />
                  </div>

                  {/* SSH keys */}
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">SSH Keys</span>
                    {!sshKeys?.length ? (
                      <p className="text-xs text-muted-foreground">
                        No saved keys. Add some under{" "}
                        <Link href="/settings/ssh-keys" className="underline font-medium">Settings → SSH Keys</Link>.
                      </p>
                    ) : (
                      <div className="border max-h-32 overflow-y-auto divide-y divide-gray-200">
                        {sshKeys.map((k) => (
                          <label key={k.id} className="flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-muted/50">
                            <input
                              type="checkbox"
                              checked={rebuildKeyIds.includes(k.id)}
                              onChange={(e) => setRebuildKeyIds((prev) => e.target.checked ? [...prev, k.id] : prev.filter((id) => id !== k.id))}
                              className="w-4 h-4"
                            />
                            <span className="text-xs font-medium truncate" title={k.fingerprint}>{k.name}</span>
                          </label>
                        ))}
                      </div>
                    )}
                  </div>

                  <div className="rounded-md border border-red-200 bg-red-50 p-4 dark:bg-red-950 dark:border-red-900">
                    <p className="text-xs font-medium text-destructive mb-2">Warning: Data Loss</p>
                    <p className="text-xs text-foreground">All data on this VM will be permanently deleted.</p>
                  </div>
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">
                      Type <span className="font-semibold">{vm.hostname}</span> to confirm
                    </span>
                    <Input value={confirmVMName} onChange={(e) => setConfirmVMName(e.target.value)} placeholder="Enter VM hostname" className="border" />
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setRebuildDialogOpen(false)}>Cancel</Button>
                  <Button variant="destructive" disabled={confirmVMName !== vm.hostname || !selectedTemplate || rebuildVM.isPending} onClick={handleRebuild}>
                    {rebuildVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}Rebuild VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Attach ISO Dialog */}
            <Dialog open={isoDialogOpen} onOpenChange={setIsoDialogOpen}>
              <DialogTrigger asChild>
                <Button variant="secondary" size="sm"><Disc className="w-4 h-4" />Mount ISO</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Mount ISO</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Attach a bootable ISO for OS install or rescue. The VM boots from it on next start; detach to return to disk boot.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  {isoTemplates && isoTemplates.length > 0 && (
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-2 block">Select an ISO</span>
                      <div className="grid grid-cols-2 gap-2 max-h-48 overflow-y-auto">
                        {isoTemplates.map((iso) => (
                          <button key={iso.id} type="button"
                            onClick={() => { setSelectedISOUrl(iso.image_path); setManualISOUrl("") }}
                            className={`p-3 border text-left transition-all ${selectedISOUrl === iso.image_path && !manualISOUrl ? "border-primary ring-1 ring-primary bg-primary/5" : "bg-card hover:bg-muted/50"}`}>
                            <span className="text-xs font-medium block truncate">{iso.name}</span>
                            <span className="text-[10px] text-muted-foreground truncate block">{iso.image_path}</span>
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">Or enter an ISO URL</span>
                    <Input value={manualISOUrl} onChange={(e) => setManualISOUrl(e.target.value)} placeholder="https://example.com/installer.iso" className="border" />
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setIsoDialogOpen(false)}>Cancel</Button>
                  <Button onClick={handleAttachISO} disabled={attachISO.isPending || (!selectedISOUrl && !manualISOUrl.trim())}>
                    {attachISO.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Disc className="w-4 h-4" />}
                    Mount ISO
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Detach ISO */}
            <Button variant="secondary" size="sm" onClick={handleDetachISO} disabled={detachISO.isPending} title="Detach install/rescue ISO">
              {detachISO.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <CircleSlash className="w-4 h-4" />}
              Unmount ISO
            </Button>

            {/* Rescue mode */}
            {vm.rescue_mode ? (
              <Button variant="warning" size="sm" onClick={handleUnrescue} disabled={unrescueVM.isPending} title="Detach rescue ISO and clear rescue mode">
                {unrescueVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <LifeBuoy className="w-4 h-4" />}
                Exit Rescue
              </Button>
            ) : (
              <Button variant="secondary" size="sm" onClick={handleRescue} disabled={rescueVM.isPending} title="Attach a rescue ISO (VM must be stopped); start the VM to boot into rescue">
                {rescueVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <LifeBuoy className="w-4 h-4" />}
                Rescue
              </Button>
            )}

            {/* Live migrate */}
            <Dialog open={migrateOpen} onOpenChange={setMigrateOpen}>
              <DialogTrigger asChild>
                <Button variant="secondary" size="sm"><ArrowRightLeft className="w-4 h-4" />Migrate</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Live Migrate VM</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Move this VM to another node with no downtime (block migration copies the disk).
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">Destination Node</span>
                    <select
                      value={migrateNodeId}
                      onChange={(e) => setMigrateNodeId(e.target.value)}
                      className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                    >
                      <option value="">Select a node…</option>
                      {allNodes?.filter((n) => n.id !== vm.node_id).map((n) => (
                        <option key={n.id} value={n.id}>{n.name} ({n.ip_address})</option>
                      ))}
                    </select>
                  </div>
                  <div className="flex items-center gap-2 p-3 bg-muted border">
                    <Server className="w-4 h-4 shrink-0" />
                    <p className="text-xs font-medium">Requires node-to-node libvirt connectivity (SSH). The VM stays running during transfer.</p>
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setMigrateOpen(false)}>Cancel</Button>
                  <Button onClick={handleMigrate} disabled={migrateVM.isPending || !migrateNodeId}>
                    {migrateVM.isPending ? <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Migrating…</> : <><ArrowRightLeft className="w-4 h-4 mr-2" />Migrate</>}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Reassign owner (admin) */}
            <Dialog open={reassignOpen} onOpenChange={setReassignOpen}>
              <DialogTrigger asChild>
                <Button variant="secondary" size="sm"><User className="w-4 h-4" />Reassign</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Reassign Owner</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Transfer this VM to another user account.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">New Owner</span>
                    <select
                      value={reassignUserId}
                      onChange={(e) => setReassignUserId(e.target.value)}
                      className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                    >
                      <option value="">Select a user…</option>
                      {usersData?.data?.filter((u) => u.id !== vm.user_id).map((u) => (
                        <option key={u.id} value={u.id}>{u.name ? `${u.name} (${u.email})` : u.email}</option>
                      ))}
                    </select>
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setReassignOpen(false)}>Cancel</Button>
                  <Button onClick={handleReassign} disabled={updateVM.isPending || !reassignUserId}>
                    {updateVM.isPending ? <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Reassigning…</> : <><User className="w-4 h-4 mr-2" />Reassign</>}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Regenerate VNC password */}
            <Button variant="secondary" size="sm" onClick={handleRegenerateVNC} disabled={regenerateVNC.isPending}>
              {regenerateVNC.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <KeyRound className="w-4 h-4" />}
              VNC Password
            </Button>

            {/* Enable/disable VNC console */}
            {vm.console_enabled === false ? (
              <Button variant="secondary" size="sm" onClick={() => handleToggleConsole(true)} disabled={setConsole.isPending}>
                {setConsole.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Monitor className="w-4 h-4" />}
                Enable Console
              </Button>
            ) : (
              <Button variant="secondary" size="sm" onClick={() => handleToggleConsole(false)} disabled={setConsole.isPending}>
                {setConsole.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <MonitorOff className="w-4 h-4" />}
                Disable Console
              </Button>
            )}

            {/* Clone VM */}
            <Dialog open={cloneOpen} onOpenChange={setCloneOpen}>
              <DialogTrigger asChild>
                <Button variant="secondary" size="sm"><Copy className="w-4 h-4" />Clone</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Clone VM</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Creates a new VM on the same node as an independent copy of this VM&apos;s disk, with a fresh IP, MAC and VNC. The VM must be stopped.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-3 py-2">
                  <label htmlFor="clone-hostname" className="text-xs font-medium text-muted-foreground block">New hostname (optional)</label>
                  <Input id="clone-hostname" value={cloneHostname} onChange={(e) => setCloneHostname(e.target.value)} placeholder={`${vm.hostname}-clone`} className="border" />

                  <label htmlFor="clone-node" className="text-xs font-medium text-muted-foreground block">Destination node</label>
                  <select
                    id="clone-node"
                    value={cloneNodeId || vm.node_id}
                    onChange={(e) => setCloneNodeId(e.target.value)}
                    className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                  >
                    {allNodes?.map((n) => (
                      <option key={n.id} value={n.id}>
                        {n.name}{n.id === vm.node_id ? " (same node)" : ""}
                      </option>
                    ))}
                  </select>
                  {cloneNodeId && cloneNodeId !== vm.node_id && (
                    <p className="text-xs text-muted-foreground">Cross-node clone pulls the disk over SSH from the source node — may take a while for large disks.</p>
                  )}
                  {vm.status === "running" && (
                    <p className="text-xs text-destructive font-medium">VM is running — stop it first to get a consistent copy.</p>
                  )}
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setCloneOpen(false)}>Cancel</Button>
                  <Button onClick={handleClone} disabled={cloneVM.isPending || vm.status === "running"}>
                    {cloneVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Copy className="w-4 h-4" />}
                    Clone VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Reset root password (guest agent) */}
            <Dialog open={resetPwOpen} onOpenChange={setResetPwOpen}>
              <DialogTrigger asChild>
                <Button variant="secondary" size="sm"><KeyRound className="w-4 h-4" />Reset Password</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Reset Root Password</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Sets a new root password on the running guest via the QEMU guest agent. The VM must be running and have qemu-guest-agent installed.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-3 py-2">
                  <label htmlFor="reset-pw" className="text-xs font-medium text-muted-foreground block">New Password</label>
                  <Input id="reset-pw" type="text" value={resetPwValue} onChange={(e) => setResetPwValue(e.target.value)} placeholder="Enter new root password" className="border font-mono" />
                  {vm.status !== "running" && (
                    <p className="text-xs text-destructive font-medium">VM is not running — start it first.</p>
                  )}
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setResetPwOpen(false)}>Cancel</Button>
                  <Button onClick={handleResetPassword} disabled={!resetPwValue.trim() || resetPassword.isPending}>
                    {resetPassword.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <KeyRound className="w-4 h-4" />}
                    Reset Password
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Delete Dialog */}
            <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
              <DialogTrigger asChild>
                <Button variant="destructive" size="sm"><Trash2 className="w-4 h-4" />Delete</Button>
              </DialogTrigger>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Delete VM</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">Are you sure you want to delete this VM?</DialogDescription>
                </DialogHeader>
                <div className="rounded-md border border-red-200 bg-red-50 p-4 dark:bg-red-950 dark:border-red-900">
                  <p className="text-xs font-medium text-destructive mb-2">Permanent Data Loss</p>
                  <p className="text-xs text-foreground">The VM &quot;{vm.hostname}&quot; will be permanently deleted.</p>
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
            <div className="lg:col-span-2 bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
              <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
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
                  <div className="grid grid-cols-2 gap-4 mt-4 pt-4 border-t">
                    <div className="p-3 border rounded-md bg-muted">
                      <span className="text-xs font-medium text-muted-foreground block">Disk Read</span>
                      <span className="text-sm font-semibold">{formatBytesPerSec(metrics.disk_read_bytes_per_sec)}</span>
                    </div>
                    <div className="p-3 border rounded-md bg-muted">
                      <span className="text-xs font-medium text-muted-foreground block">Disk Write</span>
                      <span className="text-sm font-semibold">{formatBytesPerSec(metrics.disk_write_bytes_per_sec)}</span>
                    </div>
                    <div className="p-3 border rounded-md bg-muted">
                      <span className="text-xs font-medium text-muted-foreground block">Network RX</span>
                      <span className="text-sm font-semibold">{formatBytesPerSec(metrics.network_rx_bytes_per_sec)}</span>
                    </div>
                    <div className="p-3 border rounded-md bg-muted">
                      <span className="text-xs font-medium text-muted-foreground block">Network TX</span>
                      <span className="text-sm font-semibold">{formatBytesPerSec(metrics.network_tx_bytes_per_sec)}</span>
                    </div>
                  </div>
                  {/* Trend (last 60m) */}
                  <div className="mt-4 pt-4 border-t">
                    <p className="text-[10px] font-semibold text-muted-foreground mb-2">Trend · last 60m</p>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="border p-2">
                        <span className="text-[10px] font-medium text-muted-foreground">CPU %</span>
                        <Sparkline data={cpuTrend} colorClass="text-primary" height={36} />
                      </div>
                      <div className="border p-2">
                        <span className="text-[10px] font-medium text-muted-foreground">Memory %</span>
                        <Sparkline data={memTrend} colorClass="text-secondary" height={36} />
                      </div>
                    </div>
                  </div>
                </div>
              ) : (
                <div className="space-y-6">
                  <ProgressBar value={0} max={100} label="CPU" color="primary" />
                  <ProgressBar value={0} max={vm.resources.ram} label="Memory" unit="MB" color="secondary" />
                  <ProgressBar value={vm.resources.disk} max={vm.resources.disk} label="Disk (Allocated)" unit="GB" color="accent" />
                  <p className="text-xs text-muted-foreground italic">Live metrics available when VM is running</p>
                </div>
              )}
            </div>

            {/* VM Details Sidebar */}
            <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
              <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
                <Server className="w-5 h-5" />VM Details
              </h2>
              <div className="space-y-4">
                <div className="flex items-center justify-between pb-3 border-b">
                  <span className="text-xs font-medium text-muted-foreground">Node</span>
                  <span className="text-sm font-semibold">{allNodes?.find((n) => n.id === vm.node_id)?.name || vm.node_id}</span>
                </div>
                {vmNetworks && vmNetworks.length > 0 && (
                  <div className="flex items-center justify-between pb-3 border-b">
                    <span className="text-xs font-medium text-muted-foreground">IP Address</span>
                    <div className="flex flex-col items-end gap-1">
                      {vmNetworks.map((net) => (
                        <span key={net.id} className="text-sm font-semibold font-mono">{net.ip_address}</span>
                      ))}
                    </div>
                  </div>
                )}
                <div className="flex items-center justify-between pb-3 border-b">
                  <span className="text-xs font-medium text-muted-foreground">OS Template</span>
                  <span className="text-sm font-semibold text-right">{templates?.find((t) => t.id === vm.os_template_id)?.name || vm.os_template_id}</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b">
                  <span className="text-xs font-medium text-muted-foreground">CPU</span>
                  <span className="text-sm font-semibold">{vm.resources.cpu} vCPU</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b">
                  <span className="text-xs font-medium text-muted-foreground">RAM</span>
                  <span className="text-sm font-semibold">{ramGB} GB</span>
                </div>
                <div className="flex items-center justify-between pb-3 border-b">
                  <span className="text-xs font-medium text-muted-foreground">Disk</span>
                  <span className="text-sm font-semibold">{vm.resources.disk} GB</span>
                </div>
                {vm.vnc_port && (
                  <div className="flex items-center justify-between pb-3 border-b">
                    <span className="text-xs font-medium text-muted-foreground">VNC Port</span>
                    <div className="flex items-center gap-1">
                      <span className="text-sm font-semibold font-mono">{vm.vnc_port}</span>
                      <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => copyToClipboard(vm.vnc_port!.toString())}>
                        {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                      </Button>
                    </div>
                  </div>
                )}
                <div className="flex items-center justify-between pb-3 border-b">
                  <span className="text-xs font-medium text-muted-foreground">Created</span>
                  <span className="text-sm font-medium">{formatDate(vm.created_at)}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-xs font-medium text-muted-foreground">Updated</span>
                  <span className="text-sm font-medium">{formatDate(vm.updated_at)}</span>
                </div>
              </div>
            </div>
          </div>

          {/* VNC Access. Shown without a port too: a domain imported from another
              platform often has no <graphics> device at all, and the old card
              simply vanished — leaving no hint that Repair console is what adds
              one. Worse, a remembered 5900 used to be displayed as if it were
              real, sending operators to a console that could never connect. */}
          {vm.status === "running" && (
            <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
              <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
                <Monitor className="w-5 h-5" />VNC Access
              </h2>
              <div className="flex items-center justify-between">
                <div>
                  <span className="text-xs font-medium text-muted-foreground block mb-1">VNC Port</span>
                  {vm.vnc_port ? (
                    <span className="text-xl font-semibold font-mono">{vm.vnc_port}</span>
                  ) : (
                    <>
                      <span className="text-xl font-semibold text-amber-600">No console device</span>
                      <span className="block text-xs text-muted-foreground mt-1 max-w-md">
                        This VM has no VNC device in its definition. Use Repair console on the
                        Console tab to add one — it restarts the VM.
                      </span>
                    </>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <Link href={`/vms/${vm.id}/ssh`} target="_blank">
                    <Button variant="secondary" className="gap-2">
                      <Terminal className="w-4 h-4" />
                      SSH Console
                    </Button>
                  </Link>
                  <Link
                    href={`/vms/${vm.id}/console`}
                    target="_blank"
                    className={vm.vnc_port ? "" : "pointer-events-none"}
                    aria-disabled={!vm.vnc_port}
                  >
                    <Button variant="accent" className="gap-2" disabled={!vm.vnc_port}>
                      <ExternalLink className="w-4 h-4" />
                      Open VNC Console
                    </Button>
                  </Link>
                </div>
              </div>
            </div>
          )}
        </TabsContent>

        {/* Console Tab */}
        <TabsContent value="console">
          <div className="bg-card text-card-foreground border rounded-lg shadow-sm">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                <Terminal className="w-5 h-5" />Console
              </h2>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={repairConsole.isPending}
                  onClick={async () => {
                    const ok = await confirm({
                      title: "Repair this VM's console?",
                      description:
                        "A VNC device is injected into the VM's definition, which fixes a black or unavailable console on imported machines.",
                      confirmLabel: "Repair and restart",
                      destructive: vm.status === "running",
                      details:
                        vm.status === "running"
                          ? [{ label: "Warning", value: "The VM is running and will be restarted" }]
                          : undefined,
                      action: () => repairConsole.mutateAsync(),
                    })
                    if (!ok) return
                  }}
                  title="Fixes a black/unavailable console on imported VMs by injecting a VNC device (restarts the VM)"
                >
                  <MonitorOff className="w-4 h-4 mr-2" />
                  {repairConsole.isPending ? "Repairing..." : "Repair console"}
                </Button>
                {vm.status === "running" && vm.console_enabled !== false && (
                  <Button
                    variant={consoleActive ? "destructive" : "default"}
                    size="sm"
                    onClick={() => setConsoleActive(!consoleActive)}
                  >
                    {consoleActive ? (
                      <><Square className="w-4 h-4 mr-2" />Disconnect</>
                    ) : (
                      <><Terminal className="w-4 h-4 mr-2" />Connect</>
                    )}
                  </Button>
                )}
                {consoleActive && (
                  <Link href={`/vms/${vm.id}/console`} target="_blank">
                    <Button variant="secondary" size="sm">
                      <ExternalLink className="w-4 h-4 mr-2" />Fullscreen
                    </Button>
                  </Link>
                )}
              </div>
            </div>
            {vm.console_enabled === false ? (
              <div className="bg-muted p-12 flex items-center justify-center h-[500px]">
                <div className="text-center">
                  <MonitorOff className="w-12 h-12 mx-auto text-muted-foreground mb-4" />
                  <p className="text-muted-foreground font-medium mb-2">Console disabled</p>
                  <p className="text-muted-foreground text-sm">Re-enable the console from the Actions section to connect.</p>
                </div>
              </div>
            ) : vm.status === "running" ? (
              consoleActive ? (
                <VNCConsole
                  vmId={vmId}
                  className="h-[500px]"
                  onDisconnect={() => setConsoleActive(false)}
                />
              ) : (
                <div className="bg-muted p-12 flex items-center justify-center h-[500px]">
                  <div className="text-center">
                    <Terminal className="w-12 h-12 mx-auto text-muted-foreground mb-4" />
                    <p className="text-muted-foreground font-mono text-sm mb-4">Console available</p>
                    <Button variant="default" onClick={() => setConsoleActive(true)} className="gap-2">
                      <Terminal className="w-4 h-4" />
                      Connect to Console
                    </Button>
                  </div>
                </div>
              )
            ) : (
              <div className="bg-muted p-12 flex items-center justify-center h-[500px]">
                <div className="text-center">
                  <Terminal className="w-12 h-12 mx-auto text-muted-foreground mb-4" />
                  <p className="text-muted-foreground font-medium text-sm">VM is not running</p>
                  <p className="text-muted-foreground text-xs mt-1">Start the VM to access the console</p>
                </div>
              </div>
            )}
          </div>
        </TabsContent>

        {/* Network Tab */}
        <TabsContent value="network">
          <div className="space-y-6">
            <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
              <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
                <Network className="w-5 h-5" />Network Configuration
              </h2>

              {/* Placement & console */}
              <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
                <div className="p-4 border">
                  <span className="text-xs font-medium text-muted-foreground block">Node</span>
                  <Link href={`/nodes/${vm.node_id}`} className="text-sm font-medium underline hover:text-primary break-all">
                    {allNodes?.find((n) => n.id === vm.node_id)?.name || vm.node_id}
                  </Link>
                </div>
                <div className="p-4 border">
                  <span className="text-xs font-medium text-muted-foreground block">VNC Port</span>
                  <span className="text-sm font-mono font-medium">{vm.vnc_port || "N/A"}</span>
                </div>
                <div className="p-4 border">
                  <span className="text-xs font-medium text-muted-foreground block">Status</span>
                  <span className="text-sm font-medium">{vm.status}</span>
                </div>
                <div className="p-4 border">
                  <span className="text-xs font-medium text-muted-foreground block">Resources</span>
                  <span className="text-sm font-medium">{vm.resources.cpu}C / {ramGB}GB</span>
                </div>
              </div>

              {/* Interfaces (per-IP detail) */}
              <div className="flex items-center justify-between mb-3">
                <h3 className="text-sm font-semibold text-muted-foreground">Network Interfaces</h3>
                <Button type="button" size="sm" variant="outline" onClick={() => setAssignIPOpen(true)}>
                  <Plus className="w-4 h-4 mr-1" />
                  Assign IP address
                </Button>
              </div>
              {!vmNetworks?.length ? (
                <div className="border border-dashed border-gray-300 p-6 text-center text-sm font-medium text-muted-foreground">
                  No network interfaces attached
                </div>
              ) : (
                <div className="border overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="bg-muted text-muted-foreground border-b font-medium text-xs">
                        <th className="text-left p-3">IP Address</th>
                        <th className="text-left p-3">Gateway</th>
                        <th className="text-left p-3">Netmask</th>
                        <th className="text-left p-3">VLAN</th>
                        <th className="text-left p-3">Bandwidth</th>
                        <th className="text-left p-3">rDNS</th>
                        <th className="text-right p-3"></th>
                      </tr>
                    </thead>
                    <tbody>
                      {vmNetworks.map((iface, idx) => (
                        <tr key={iface.id} className={`border-t ${idx % 2 ? "bg-muted/50" : "bg-card"}`}>
                          <td className="p-3 font-mono font-medium">
                            {iface.pool_id ? (
                              <Link href={`/ip-pools/${iface.pool_id}`} className="underline hover:text-primary">{iface.ip_address}</Link>
                            ) : iface.ip_address}
                          </td>
                          <td className="p-3 font-mono">{iface.gateway || "—"}</td>
                          <td className="p-3 font-mono">{iface.netmask || "—"}</td>
                          <td className="p-3 font-mono">{iface.vlan_id ?? "—"}</td>
                          <td className="p-3"><BandwidthCell vmId={vmId} iface={iface} /></td>
                          <td className="p-3 font-mono text-xs">{iface.rdns || "—"}</td>
                          <td className="p-3 text-right">
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                              title="Release this address back to its pool"
                              onClick={async () => {
                                const ok = await confirm({
                                  title: `Release ${iface.ip_address}?`,
                                  description:
                                    "The address returns to its pool and can be given to another VM. The guest keeps using it until you change its configuration, so it will stop working rather than fail over.",
                                  confirmLabel: "Release address",
                                  destructive: true,
                                  action: () => releaseIP.mutateAsync(iface.id),
                                })
                                if (!ok) return
                                setToast({ message: `${iface.ip_address} released`, type: "success" })
                              }}
                            >
                              <Trash2 className="w-4 h-4" />
                            </Button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>

            {/* Data disks */}
            <DisksCard vmId={vmId} />

            {/* Bandwidth Usage */}
            <BandwidthUsageCard vmId={vmId} />
          </div>
        </TabsContent>

        {/* Snapshots Tab */}
        <TabsContent value="snapshots">
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
            <div className="flex items-center justify-between mb-6">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                <Database className="w-5 h-5" />Snapshots
              </h2>
              <div className="flex items-center gap-2">
              <Dialog
                open={createImageOpen}
                onOpenChange={(open) => {
                  setCreateImageOpen(open)
                  if (open && !imageName) setImageName(`${vm?.hostname ?? "vm"}-image`)
                }}
              >
                <DialogTrigger asChild>
                  <Button variant="outline" size="sm"><Copy className="w-4 h-4 mr-2" />Create Image</Button>
                </DialogTrigger>
                <DialogContent className="max-w-sm border shadow-lg">
                  <DialogHeader>
                    <DialogTitle className="text-lg font-semibold">Create Image</DialogTitle>
                    <DialogDescription>
                      Captures this VM&apos;s disk into a reusable image. Images survive VM deletion and can seed new VMs.
                    </DialogDescription>
                  </DialogHeader>
                  <div className="space-y-3 py-2">
                    <label htmlFor="image-name" className="text-xs font-medium text-muted-foreground block">Image Name</label>
                    <Input id="image-name" value={imageName} onChange={(e) => setImageName(e.target.value)} placeholder="e.g., web-base-image" className="border" />
                  </div>
                  <DialogFooter>
                    <Button variant="ghost" onClick={() => setCreateImageOpen(false)}>Cancel</Button>
                    <Button onClick={handleCreateImage} disabled={!imageName.trim() || createImage.isPending}>
                      {createImage.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Copy className="w-4 h-4" />}
                      Capture
                    </Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
              <Dialog open={createSnapshotOpen} onOpenChange={setCreateSnapshotOpen}>
                <DialogTrigger asChild>
                  <Button variant="default" size="sm"><Plus className="w-4 h-4 mr-2" />Create Snapshot</Button>
                </DialogTrigger>
                <DialogContent className="max-w-sm border shadow-lg">
                  <DialogHeader>
                    <DialogTitle className="text-lg font-semibold">Create Snapshot</DialogTitle>
                  </DialogHeader>
                  <div className="space-y-3 py-2">
                    <label htmlFor="snapshot-name" className="text-xs font-medium text-muted-foreground block">Snapshot Name</label>
                    <Input id="snapshot-name" value={snapshotName} onChange={(e) => setSnapshotName(e.target.value)} placeholder="e.g., before-update" className="border" />
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
                  <div key={snap.id} className="flex items-center justify-between p-4 border hover:bg-muted/50">
                    <div className="flex items-center gap-4">
                      <Database className="w-5 h-5" />
                      <div>
                        <span className="font-semibold">{snap.name}</span>
                        <span className="text-xs text-muted-foreground ml-2">{formatDate(snap.created_at)}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className={`text-xs font-medium px-2 py-1 border ${
                        snap.status === "completed" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : snap.status === "failed" ? "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900" : "bg-muted text-muted-foreground"
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
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
            <div className="flex items-center justify-between mb-6">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
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
                  <div key={backup.id} className="flex items-center justify-between p-4 border hover:bg-muted/50">
                    <div className="flex items-center gap-4">
                      <HardDrive className="w-5 h-5" />
                      <div>
                        <span className="font-semibold">{backup.backup_type} backup</span>
                        <span className="text-xs text-muted-foreground ml-2">{formatDate(backup.created_at)}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-xs font-medium">{formatBytes(backup.size)}</span>
                      <span className={`text-xs font-medium px-2 py-1 border ${
                        backup.status === "completed" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : backup.status === "failed" ? "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900" : backup.status === "in_progress" ? "bg-sky-50 text-sky-700 border-sky-200 dark:bg-sky-950 dark:text-sky-300 dark:border-sky-900" : "bg-muted text-muted-foreground"
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
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
            <div className="flex items-center justify-between mb-6">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                <Shield className="w-5 h-5" />Firewall Rules
              </h2>
              <Dialog open={fwOpen} onOpenChange={setFwOpen}>
                <DialogTrigger asChild>
                  <Button size="sm"><Plus className="w-4 h-4 mr-1" />Add Rule</Button>
                </DialogTrigger>
                <DialogContent className="max-w-md">
                  <DialogHeader>
                    <DialogTitle>Add Firewall Rule</DialogTitle>
                    <DialogDescription>Allow or deny traffic to this VM.</DialogDescription>
                  </DialogHeader>
                  <div className="grid grid-cols-2 gap-3 py-2">
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-1 block">Direction</span>
                      <select value={fwForm.direction} onChange={(e) => setFwForm({ ...fwForm, direction: e.target.value })} className="w-full h-10 px-3 border rounded-md bg-background text-sm">
                        <option value="inbound">Inbound</option>
                        <option value="outbound">Outbound</option>
                      </select>
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-1 block">Action</span>
                      <select value={fwForm.action} onChange={(e) => setFwForm({ ...fwForm, action: e.target.value })} className="w-full h-10 px-3 border rounded-md bg-background text-sm">
                        <option value="allow">Allow</option>
                        <option value="deny">Deny</option>
                      </select>
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-1 block">Protocol</span>
                      <select value={fwForm.protocol} onChange={(e) => setFwForm({ ...fwForm, protocol: e.target.value })} className="w-full h-10 px-3 border rounded-md bg-background text-sm">
                        <option value="tcp">TCP</option>
                        <option value="udp">UDP</option>
                        <option value="icmp">ICMP</option>
                        <option value="all">All</option>
                      </select>
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-1 block">Port / Range</span>
                      <Input value={fwForm.port_range} onChange={(e) => setFwForm({ ...fwForm, port_range: e.target.value })} placeholder="80 or 8000-8100" />
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-1 block">Source IP/CIDR</span>
                      <Input value={fwForm.source_ip} onChange={(e) => setFwForm({ ...fwForm, source_ip: e.target.value })} placeholder="Any (0.0.0.0/0)" />
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-1 block">Priority</span>
                      <Input type="number" value={fwForm.priority} onChange={(e) => setFwForm({ ...fwForm, priority: parseInt(e.target.value) || 100 })} />
                    </div>
                  </div>
                  <DialogFooter>
                    <Button variant="ghost" onClick={() => setFwOpen(false)}>Cancel</Button>
                    <Button onClick={handleAddFirewallRule} disabled={createFirewallRule.isPending}>
                      {createFirewallRule.isPending ? <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Adding…</> : "Add Rule"}
                    </Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
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
                    <tr className="border-b">
                      <th className="text-left py-3 px-4 text-xs font-semibold">Protocol</th>
                      <th className="text-left py-3 px-4 text-xs font-semibold">Port</th>
                      <th className="text-left py-3 px-4 text-xs font-semibold">Direction</th>
                      <th className="text-left py-3 px-4 text-xs font-semibold">Source</th>
                      <th className="text-left py-3 px-4 text-xs font-semibold">Action</th>
                      <th className="text-left py-3 px-4 text-xs font-semibold">Priority</th>
                      <th className="text-left py-3 px-4 text-xs font-semibold">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {firewallRules.map((rule: FirewallRule) => (
                      <tr key={rule.id} className="border-b">
                        <td className="py-3 px-4 font-mono text-sm">{rule.protocol}</td>
                        <td className="py-3 px-4 font-mono text-sm">{rule.port_range || "*"}</td>
                        <td className="py-3 px-4 text-xs font-medium">{rule.direction}</td>
                        <td className="py-3 px-4 font-mono text-sm">{rule.source_ip || "Any"}</td>
                        <td className="py-3 px-4">
                          <span className={`text-xs font-medium px-2 py-1 border ${
                            rule.action === "allow" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900"
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
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
            <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
              <FileText className="w-5 h-5" />Activity Logs
            </h2>
            {activityLoading ? (
              <div className="bg-muted border p-8 h-64 flex items-center justify-center">
                <p className="text-muted-foreground text-sm">Loading activity...</p>
              </div>
            ) : !activity || activity.length === 0 ? (
              <div className="bg-muted border p-8 h-64 flex items-center justify-center">
                <div className="text-center">
                  <FileText className="w-12 h-12 mx-auto text-muted-foreground mb-4" />
                  <p className="text-muted-foreground font-medium text-sm">No activity yet</p>
                </div>
              </div>
            ) : (
              <div className="divide-y border rounded-lg">
                {activity.map((log) => {
                  const detailStr = log.details && Object.keys(log.details).length > 0
                    ? Object.entries(log.details)
                        .map(([k, v]) => `${k}: ${String(v)}`)
                        .join(', ')
                    : null
                  return (
                    <div key={log.id} className="flex items-start gap-3 px-4 py-3">
                      <FileText className="w-4 h-4 mt-0.5 text-muted-foreground shrink-0" />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="text-sm font-medium text-foreground">{log.action}</span>
                          {log.ip_address && (
                            <span className="text-xs text-muted-foreground">from {log.ip_address}</span>
                          )}
                        </div>
                        {detailStr && (
                          <p className="text-xs text-muted-foreground mt-0.5 truncate">{detailStr}</p>
                        )}
                      </div>
                      <span className="text-xs text-muted-foreground whitespace-nowrap">{formatDate(log.created_at)}</span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </TabsContent>
      </Tabs>

      {/* Assign an address to a VM that already exists. */}
      <Dialog open={assignIPOpen} onOpenChange={setAssignIPOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Assign an IP address to {vm.hostname}</DialogTitle>
            <DialogDescription>
              An address is taken from a pool on this VM&apos;s node and reserved for it. The host is
              configured to route it — anti-spoofing and firewall rules follow the address.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-2">
            <label htmlFor="assign-pool" className="text-sm font-medium">Pool</label>
            <select
              id="assign-pool"
              className="w-full border rounded-md px-3 py-2 text-sm bg-background"
              value={assignPoolID}
              onChange={(e) => setAssignPoolID(e.target.value)}
            >
              <option value="">Any pool available on this node</option>
              {(ipPools ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} ({p.cidr})
                </option>
              ))}
            </select>
          </div>

          {/* Said plainly, because the alternative is the operator assigning an
              address, seeing it listed, and wondering why the VM is unreachable.
              Only a rebuild regenerates cloud-init, so an imported machine has
              to be configured from inside. */}
          <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-700 dark:text-amber-400">
            The address is not written into the guest. Unless this VM is rebuilt, you must configure
            it inside the operating system — the panel reserves it and prepares the host, nothing
            more.
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setAssignIPOpen(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              disabled={assignIP.isPending}
              onClick={async () => {
                try {
                  await assignIP.mutateAsync({ pool_id: assignPoolID || undefined })
                  setAssignIPOpen(false)
                  setAssignPoolID("")
                  setToast({ message: "IP address assigned", type: "success" })
                } catch (err) {
                  setToast({ message: (err as Error).message, type: "error" })
                }
              }}
            >
              {assignIP.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Assign"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete progress. Leaving for the list only once it has finished means
          the outcome is always seen — including a failure, which previously
          scrolled past as a toast on a page the operator had already left. */}
      <DeleteProgressDialog
        vm={deletingVM}
        onClose={() => {
          setDeletingVM(null)
          router.push("/vms")
        }}
      />

      {/* Toast */}
      {toast && (
        <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />
      )}
    </div>
  )
}
