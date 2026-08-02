"use client"

import { useState, useEffect, useCallback } from "react"
import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { 
  Play, Square, RotateCcw, Trash2, RefreshCw, ArrowLeft,
  Copy, Check, Cpu, HardDrive,
  Monitor, MonitorOff, KeyRound, Server, Network, Database, Shield, FileText, Terminal, Activity, ExternalLink,
  Loader2, AlertCircle, Plus, Disc, CircleSlash, LifeBuoy, ArrowRightLeft, User
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useVM, useVMMetrics, useVMMetricsHistory, useVMAction, useDeleteVM, useAttachISO, useDetachISO, useRescueVM, useUnrescueVM, useMigrateVM, useRegenerateVNCPassword, useSetConsoleEnabled, useRebuildVM, useResetPassword, useCloneVM, useUpdateVM } from "@/lib/hooks/use-vms"
import { useUsers } from "@/lib/hooks/use-users"
import { useSSHKeys } from "@/lib/hooks/use-ssh-keys"
import { useNodes } from "@/lib/hooks/use-nodes"
import { Sparkline } from "@/components/ui/sparkline"
import { useSnapshots, useCreateSnapshot, useRestoreSnapshot, useDeleteSnapshot } from "@/lib/hooks/use-snapshots"
import { useBackups, useCreateBackup, useDeleteBackup } from "@/lib/hooks/use-backups"
import { useFirewallRules, useDeleteFirewallRule, useVMNetworks, useSetVMBandwidth } from "@/lib/hooks/use-networks"
import type { Network as NetworkIface } from "@/types"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useVMBandwidth, useSetBandwidthQuota } from "@/lib/hooks/use-bandwidth"
import { useVMDisks, useAttachDisk, useDetachDisk } from "@/lib/hooks/use-disks"
import { VNCConsole } from "@/components/vnc-console"
import type { VMStatus, Snapshot, Backup, FirewallRule } from "@/types"

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
          className="text-[10px] font-black uppercase underline text-gray-500 hover:text-primary"
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
        className="h-8 border-2 border-black text-xs font-bold px-1"
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
        className="h-8 w-20 border-2 border-black text-xs font-mono px-1"
        aria-label="Bandwidth in Mbps"
      />
      <span className="text-[10px] font-bold text-gray-500">Mbps</span>
      <button
        type="button"
        onClick={apply}
        disabled={setBandwidth.isPending}
        className="h-8 px-2 bg-[#CCFF00] text-black border-2 border-black text-xs font-black uppercase disabled:opacity-50"
      >
        {setBandwidth.isPending ? "…" : "Save"}
      </button>
      <button
        type="button"
        onClick={() => setEditing(false)}
        className="h-8 px-2 bg-white text-black border-2 border-black text-xs font-black uppercase"
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
    deleting: { bg: "bg-warning", text: "text-black", label: "Deleting" },
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
      <Icon className="w-10 h-10 mx-auto text-gray-500 mb-3" />
      <p className="text-gray-500 text-sm font-medium">{message}</p>
    </div>
  )
}

function DisksCard({ vmId }: { vmId: string }) {
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

  const handleDetach = async (device: string) => {
    const deleteVolume = window.confirm(`Detach ${device}?\n\nClick OK to also DELETE its data permanently, or Cancel to keep the volume on disk.`)
    try {
      await detach.mutateAsync({ device, deleteVolume })
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  return (
    <div className="bg-white border-4 border-black p-6 shadow-neo">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-lg font-black uppercase tracking-tight text-black flex items-center gap-2">
          <HardDrive className="w-5 h-5" />Data Disks
        </h2>
        <Button size="sm" onClick={() => setAdding((v) => !v)}><Plus className="w-4 h-4" />Add Disk</Button>
      </div>

      {adding && (
        <div className="flex flex-wrap items-end gap-2 mb-4 p-3 border-2 border-black bg-gray-50">
          <div>
            <label htmlFor="disk-size" className="text-xs font-bold uppercase text-gray-600 block mb-1">Size (GB)</label>
            <Input id="disk-size" type="number" min={1} value={sizeGB} onChange={(e) => setSizeGB(e.target.value)} className="border-2 border-black w-32 font-mono" />
          </div>
          <Button size="sm" onClick={handleAttach} disabled={attach.isPending}>
            {attach.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Check className="w-4 h-4" />}Attach
          </Button>
          <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setErr("") }}>Cancel</Button>
        </div>
      )}
      {err && <p className="text-xs text-danger font-bold mb-3">{err}</p>}

      {isLoading ? (
        <SectionSkeleton />
      ) : !disks?.length ? (
        <div className="border-2 border-dashed border-gray-300 p-6 text-center text-sm font-bold uppercase text-gray-400">
          No extra data disks. The boot disk isn&apos;t listed here.
        </div>
      ) : (
        <div className="border-2 border-black overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-black text-white font-black uppercase text-xs tracking-wider">
                <th className="text-left p-3">Device</th>
                <th className="text-left p-3">Size</th>
                <th className="text-left p-3">Path</th>
                <th className="text-right p-3">Action</th>
              </tr>
            </thead>
            <tbody>
              {disks.map((d, i) => (
                <tr key={d.id} className={`border-t-2 border-black ${i % 2 ? "bg-gray-50" : "bg-white"}`}>
                  <td className="p-3 font-mono font-bold">{d.device}</td>
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
      <p className="text-xs text-gray-500 mt-3">Disks hot-plug on running VMs; format &amp; mount inside the guest after attaching.</p>
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
            <span className="text-xs text-gray-600">{bandwidth.period_start}</span>
            <span className="text-xs text-gray-600">{bandwidth.period_end}</span>
          </div>
        </div>
      )}

      {/* Monthly quota control */}
      <div className="mt-6 pt-4 border-t-2 border-gray-200">
        {editingQuota ? (
          <div className="flex flex-wrap items-end gap-2">
            <div>
              <label htmlFor="bw-quota" className="text-xs font-bold uppercase text-gray-500 block mb-1">Monthly quota (GB, 0 = unlimited)</label>
              <Input id="bw-quota" type="number" min={0} value={quotaInput} onChange={(e) => setQuotaInput(e.target.value)} className="border-2 border-black w-40 font-mono" />
            </div>
            <Button size="sm" onClick={handleSaveQuota} disabled={setQuota.isPending}>
              {setQuota.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Check className="w-4 h-4" />}Save
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setEditingQuota(false)}>Cancel</Button>
          </div>
        ) : (
          <Button size="sm" variant="secondary" className="border-2 border-black" onClick={() => { setQuotaInput(String(bandwidth.quota_gb || 0)); setEditingQuota(true) }}>
            <Activity className="w-4 h-4" />Set monthly quota
          </Button>
        )}
      </div>
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
  const [consoleActive, setConsoleActive] = useState(false)
  const [snapshotName, setSnapshotName] = useState("")
  const [createSnapshotOpen, setCreateSnapshotOpen] = useState(false)
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

      {/* Header (identity) */}
      <div className="bg-white border-4 border-black p-6 shadow-neo mb-4">
        <div className="flex items-center gap-4">
            <div className="w-16 h-16 bg-primary flex items-center justify-center border-4 border-black shadow-neo">
              <Monitor className="w-8 h-8" />
            </div>
            <div>
              <h1 className="text-2xl lg:text-3xl font-black uppercase tracking-tight text-black">{vm.hostname}</h1>
              <div className="flex items-center gap-3 mt-2">
                <StatusBadge status={vm.status} />
                {vm.rescue_mode && (
                  <span className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase border-2 border-black bg-warning">
                    <LifeBuoy className="w-3 h-3" />
                    Rescue
                  </span>
                )}
                <span className="text-xs font-medium text-gray-500">ID: {vm.id.slice(0, 12)}</span>
              </div>
            </div>
        </div>
      </div>

      {/* Actions */}
      <div className="bg-white border-4 border-black shadow-neo mb-6">
        <div className="p-3 bg-black text-white font-black uppercase text-xs tracking-wider">Actions</div>
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
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Rebuild VM</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    This will destroy the current VM and create a new one from a template. All data will be lost.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4 max-h-[60vh] overflow-y-auto">
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

                  {/* Root password */}
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">Root Password</span>
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
                      className="border-2 border-black font-mono text-sm disabled:opacity-50"
                    />
                  </div>

                  {/* SSH keys */}
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">SSH Keys</span>
                    {!sshKeys?.length ? (
                      <p className="text-xs text-gray-500">
                        No saved keys. Add some under{" "}
                        <Link href="/settings/ssh-keys" className="underline font-bold">Settings → SSH Keys</Link>.
                      </p>
                    ) : (
                      <div className="border-2 border-black max-h-32 overflow-y-auto divide-y divide-gray-200">
                        {sshKeys.map((k) => (
                          <label key={k.id} className="flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-gray-50">
                            <input
                              type="checkbox"
                              checked={rebuildKeyIds.includes(k.id)}
                              onChange={(e) => setRebuildKeyIds((prev) => e.target.checked ? [...prev, k.id] : prev.filter((id) => id !== k.id))}
                              className="w-4 h-4"
                            />
                            <span className="text-xs font-bold truncate" title={k.fingerprint}>{k.name}</span>
                          </label>
                        ))}
                      </div>
                    )}
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
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Mount ISO</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    Attach a bootable ISO for OS install or rescue. The VM boots from it on next start; detach to return to disk boot.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  {isoTemplates && isoTemplates.length > 0 && (
                    <div>
                      <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">Select an ISO</span>
                      <div className="grid grid-cols-2 gap-2 max-h-48 overflow-y-auto">
                        {isoTemplates.map((iso) => (
                          <button key={iso.id} type="button"
                            onClick={() => { setSelectedISOUrl(iso.image_path); setManualISOUrl("") }}
                            className={`p-3 border-2 border-black text-left transition-all ${selectedISOUrl === iso.image_path && !manualISOUrl ? "bg-primary shadow-neo" : "bg-white hover:bg-gray-50"}`}>
                            <span className="text-xs font-bold uppercase block truncate">{iso.name}</span>
                            <span className="text-[10px] text-gray-500 truncate block">{iso.image_path}</span>
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">Or enter an ISO URL</span>
                    <Input value={manualISOUrl} onChange={(e) => setManualISOUrl(e.target.value)} placeholder="https://example.com/installer.iso" className="border-2 border-black" />
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
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Live Migrate VM</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    Move this VM to another node with no downtime (block migration copies the disk).
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">Destination Node</span>
                    <select
                      value={migrateNodeId}
                      onChange={(e) => setMigrateNodeId(e.target.value)}
                      className="w-full h-12 px-3 border-2 border-black font-medium bg-white"
                    >
                      <option value="">Select a node…</option>
                      {allNodes?.filter((n) => n.id !== vm.node_id).map((n) => (
                        <option key={n.id} value={n.id}>{n.name} ({n.ip_address})</option>
                      ))}
                    </select>
                  </div>
                  <div className="flex items-center gap-2 p-3 bg-gray-100 border-2 border-black">
                    <Server className="w-4 h-4 shrink-0" />
                    <p className="text-xs font-bold">Requires node-to-node libvirt connectivity (SSH). The VM stays running during transfer.</p>
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
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Reassign Owner</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    Transfer this VM to another user account.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div>
                    <span className="text-xs font-bold uppercase text-gray-600 mb-2 block">New Owner</span>
                    <select
                      value={reassignUserId}
                      onChange={(e) => setReassignUserId(e.target.value)}
                      className="w-full h-12 px-3 border-2 border-black font-medium bg-white"
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
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Clone VM</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    Creates a new VM on the same node as an independent copy of this VM&apos;s disk, with a fresh IP, MAC and VNC. The VM must be stopped.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-3 py-2">
                  <label htmlFor="clone-hostname" className="text-xs font-bold uppercase text-gray-600 block">New hostname (optional)</label>
                  <Input id="clone-hostname" value={cloneHostname} onChange={(e) => setCloneHostname(e.target.value)} placeholder={`${vm.hostname}-clone`} className="border-2 border-black" />

                  <label htmlFor="clone-node" className="text-xs font-bold uppercase text-gray-600 block">Destination node</label>
                  <select
                    id="clone-node"
                    value={cloneNodeId || vm.node_id}
                    onChange={(e) => setCloneNodeId(e.target.value)}
                    className="w-full h-11 px-3 border-2 border-black font-medium bg-white"
                  >
                    {allNodes?.map((n) => (
                      <option key={n.id} value={n.id}>
                        {n.name}{n.id === vm.node_id ? " (same node)" : ""}
                      </option>
                    ))}
                  </select>
                  {cloneNodeId && cloneNodeId !== vm.node_id && (
                    <p className="text-xs text-gray-600">Cross-node clone pulls the disk over SSH from the source node — may take a while for large disks.</p>
                  )}
                  {vm.status === "running" && (
                    <p className="text-xs text-danger font-bold">VM is running — stop it first to get a consistent copy.</p>
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
              <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
                <DialogHeader>
                  <DialogTitle className="text-lg font-black uppercase">Reset Root Password</DialogTitle>
                  <DialogDescription className="text-sm text-gray-600">
                    Sets a new root password on the running guest via the QEMU guest agent. The VM must be running and have qemu-guest-agent installed.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-3 py-2">
                  <label htmlFor="reset-pw" className="text-xs font-bold uppercase text-gray-600 block">New Password</label>
                  <Input id="reset-pw" type="text" value={resetPwValue} onChange={(e) => setResetPwValue(e.target.value)} placeholder="Enter new root password" className="border-2 border-black font-mono" />
                  {vm.status !== "running" && (
                    <p className="text-xs text-danger font-bold">VM is not running — start it first.</p>
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
                  {/* Trend (last 60m) */}
                  <div className="mt-4 pt-4 border-t-2 border-black">
                    <p className="text-[10px] font-black uppercase tracking-wider text-gray-400 mb-2">Trend · last 60m</p>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="border-2 border-black p-2">
                        <span className="text-[10px] font-bold uppercase text-gray-500">CPU %</span>
                        <Sparkline data={cpuTrend} colorClass="text-primary" height={36} />
                      </div>
                      <div className="border-2 border-black p-2">
                        <span className="text-[10px] font-bold uppercase text-gray-500">Memory %</span>
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
                  <p className="text-xs text-gray-600 italic">Live metrics available when VM is running</p>
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
                {vmNetworks && vmNetworks.length > 0 && (
                  <div className="flex items-center justify-between pb-3 border-b-2 border-black">
                    <span className="text-xs font-bold uppercase text-gray-500">IP Address</span>
                    <div className="flex flex-col items-end gap-1">
                      {vmNetworks.map((net) => (
                        <span key={net.id} className="text-sm font-black font-mono">{net.ip_address}</span>
                      ))}
                    </div>
                  </div>
                )}
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
                <div className="flex items-center gap-2">
                  <Link href={`/vms/${vm.id}/ssh`} target="_blank">
                    <Button variant="secondary" className="gap-2">
                      <Terminal className="w-4 h-4" />
                      SSH Console
                    </Button>
                  </Link>
                  <Link href={`/vms/${vm.id}/console`} target="_blank">
                    <Button variant="accent" className="gap-2">
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
          <div className="bg-white border-4 border-black shadow-neo">
            <div className="flex items-center justify-between p-4 border-b-2 border-black">
              <h2 className="text-lg font-black uppercase tracking-tight text-black flex items-center gap-2">
                <Terminal className="w-5 h-5" />Console
              </h2>
              <div className="flex items-center gap-2">
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
              <div className="bg-gray-100 p-12 flex items-center justify-center h-[500px]">
                <div className="text-center">
                  <MonitorOff className="w-12 h-12 mx-auto text-gray-400 mb-4" />
                  <p className="text-gray-600 font-bold uppercase mb-2">Console disabled</p>
                  <p className="text-gray-500 text-sm">Re-enable the console from the Actions section to connect.</p>
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
                <div className="bg-black p-12 flex items-center justify-center h-[500px]">
                  <div className="text-center">
                    <Terminal className="w-12 h-12 mx-auto text-green-400 mb-4" />
                    <p className="text-green-400 font-mono text-sm mb-4">Console available</p>
                    <Button variant="default" onClick={() => setConsoleActive(true)} className="gap-2">
                      <Terminal className="w-4 h-4" />
                      Connect to Console
                    </Button>
                  </div>
                </div>
              )
            ) : (
              <div className="bg-gray-100 p-12 flex items-center justify-center h-[500px]">
                <div className="text-center">
                  <Terminal className="w-12 h-12 mx-auto text-gray-500 mb-4" />
                  <p className="text-gray-500 font-bold uppercase text-sm">VM is not running</p>
                  <p className="text-gray-600 text-xs mt-1">Start the VM to access the console</p>
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

              {/* Placement & console */}
              <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
                <div className="p-4 border-2 border-black">
                  <span className="text-xs font-bold uppercase text-gray-500 block">Node</span>
                  <Link href={`/nodes/${vm.node_id}`} className="text-sm font-bold underline hover:text-primary break-all">
                    {allNodes?.find((n) => n.id === vm.node_id)?.name || vm.node_id}
                  </Link>
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

              {/* Interfaces (Virtualizor-style per-IP detail) */}
              <h3 className="text-sm font-black uppercase tracking-wider text-gray-600 mb-3">Network Interfaces</h3>
              {!vmNetworks?.length ? (
                <div className="border-2 border-dashed border-gray-300 p-6 text-center text-sm font-bold uppercase text-gray-400">
                  No network interfaces attached
                </div>
              ) : (
                <div className="border-2 border-black overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="bg-black text-white font-black uppercase text-xs tracking-wider">
                        <th className="text-left p-3">IP Address</th>
                        <th className="text-left p-3">Gateway</th>
                        <th className="text-left p-3">Netmask</th>
                        <th className="text-left p-3">VLAN</th>
                        <th className="text-left p-3">Bandwidth</th>
                        <th className="text-left p-3">rDNS</th>
                      </tr>
                    </thead>
                    <tbody>
                      {vmNetworks.map((iface, idx) => (
                        <tr key={iface.id} className={`border-t-2 border-black ${idx % 2 ? "bg-gray-50" : "bg-white"}`}>
                          <td className="p-3 font-mono font-bold">
                            {iface.pool_id ? (
                              <Link href={`/ip-pools/${iface.pool_id}`} className="underline hover:text-primary">{iface.ip_address}</Link>
                            ) : iface.ip_address}
                          </td>
                          <td className="p-3 font-mono">{iface.gateway || "—"}</td>
                          <td className="p-3 font-mono">{iface.netmask || "—"}</td>
                          <td className="p-3 font-mono">{iface.vlan_id ?? "—"}</td>
                          <td className="p-3"><BandwidthCell vmId={vmId} iface={iface} /></td>
                          <td className="p-3 font-mono text-xs">{iface.rdns || "—"}</td>
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
                <FileText className="w-12 h-12 mx-auto text-gray-500 mb-4" />
                <p className="text-gray-500 font-bold uppercase text-sm">No activity logs yet for this VM</p>
                <p className="text-gray-600 text-xs mt-1">VM activity logging is under development</p>
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
