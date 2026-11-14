"use client"

import { useState, useCallback, useEffect } from "react"
import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { 
  Square, RotateCcw, Trash2, RefreshCw, ArrowLeft,
  Copy, Check, HardDrive,
  Monitor, MonitorOff, KeyRound, Server, Database, Shield, FileText, Terminal, Activity, ExternalLink,
  Loader2, AlertCircle, Plus, Disc, CircleSlash, LifeBuoy, ArrowRightLeft, User,
  Pencil, Cpu, X
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useVM, useVMMetrics, useVMAction, useDeleteVM, useAttachISO, useDetachISO, useRescueVM, useUnrescueVM, useMigrateVM, useRegenerateVNCPassword, useSetConsoleEnabled, useRepairConsole, useRebuildVM, useResetPassword, useCloneVM, useUpdateVM, useAssignVMIP, useReleaseVMIP, useAssignVMIPv6, useReleaseVMIPv6, useInitialPassword, useRevealInitialPassword, useForgetInitialPassword } from "@/lib/hooks/use-vms"
import { useIPPools } from "@/lib/hooks/use-ipam"
import { DeleteProgressDialog } from "@/components/vm-delete-progress"
import { VMNameEditor } from "@/components/vm-name-editor"
import { OSPicker } from "@/components/os-picker"
import { UserPicker } from "@/components/user-picker"
import { useSSHKeys } from "@/lib/hooks/use-ssh-keys"
import { useNodes } from "@/lib/hooks/use-nodes"
import { useSnapshots, useCreateSnapshot, useRestoreSnapshot, useDeleteSnapshot } from "@/lib/hooks/use-snapshots"
import { useCreateImage } from "@/lib/hooks/use-images"
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup, useBackupSchedules, useCreateBackupSchedule, useDeleteBackupSchedule } from "@/lib/hooks/use-backups"
import { useFirewallRules, useDeleteFirewallRule, useCreateFirewallRule, useVMNetworks, useSetVMBandwidth, useSetVMRDNS, usePortForwards, useCreatePortForward, useDeletePortForward, useSetAntiSpoofing } from "@/lib/hooks/use-networks"
import type { Network as NetworkIface } from "@/types"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useVMBandwidth, useVMBandwidthHistory, useSetBandwidthQuota } from "@/lib/hooks/use-bandwidth"
import { useVMDisks, useAttachDisk, useDetachDisk } from "@/lib/hooks/use-disks"
import { useVMActivity } from "@/lib/hooks/use-audit-logs"
import { VNCConsole } from "@/components/vnc-console"
import { VMTelemetry } from "@/components/vm-telemetry"
import { OSLogoTile } from "@/components/os-icon"
import type { Snapshot, Backup, FirewallRule } from "@/types"
import { useConfirm } from "@/components/confirm-provider"
import { StatusBadge, useVMTransitions } from "@/components/status-badge"
import { OperationProgress, vmProvisionSteps } from "@/components/operation-progress"
import { useCopy, useFlash } from "@/lib/use-feedback"
import { CountryFlag } from "@/components/country-flag"
import { toast } from "sonner"

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
// A real switch, not a label: the state is on the control itself.
function AntiSpoofCell({ vmId, iface }: { vmId: string; iface: NetworkIface }) {
  const set = useSetAntiSpoofing(vmId)
  const on = iface.anti_spoofing !== false
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={`Anti-spoofing for ${iface.ip_address}`}
      title="Drops traffic from this VM that claims an address it was not given"
      disabled={set.isPending}
      onClick={() => set.mutate(
        { networkId: iface.id, enabled: !on },
        { onError: (e) => toast.error(`Could not change anti-spoofing: ${e.message}`) },
      )}
      className={`inline-flex items-center gap-2 text-xs font-medium cursor-pointer disabled:cursor-not-allowed disabled:opacity-60 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring rounded-full`}
    >
      <span className={`relative inline-block h-5 w-9 rounded-full border transition-colors ${on ? "bg-primary border-primary" : "bg-muted border-input"}`}>
        <span className={`absolute top-0.5 h-3.5 w-3.5 rounded-full bg-background border transition-all ${on ? "left-[18px]" : "left-0.5"}`} />
      </span>
      {on ? "On" : "Off"}
    </button>
  )
}

function BandwidthHistory({ vmId }: { vmId: string }) {
  const [open, setOpen] = useState(false)
  const { data, isLoading, error } = useVMBandwidthHistory(open ? vmId : "")
  const month = (d: string) => new Date(d).toLocaleDateString("en-US", { year: "numeric", month: "long" })
  return (
    <div className="mt-6 pt-4 border-t">
      <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        {open ? "Hide previous months" : "Show previous months"}
      </Button>
      {open && (isLoading ? (
        <Skeleton className="h-5 w-64 mt-3" />
      ) : error ? (
        <p role="alert" className="text-sm text-destructive mt-3">Could not load history: {(error as Error).message}</p>
      ) : !data?.history?.length ? (
        <p className="text-sm text-muted-foreground mt-3">No earlier months recorded yet.</p>
      ) : (
        <div className="border overflow-x-auto mt-3">
          <table className="w-full text-sm">
            <thead className="bg-muted text-xs text-muted-foreground">
              <tr>
                <th className="text-left p-3">Month</th>
                <th className="text-right p-3">Received</th>
                <th className="text-right p-3">Sent</th>
                <th className="text-right p-3">Total</th>
                <th className="text-right p-3">Quota</th>
              </tr>
            </thead>
            <tbody>
              {data.history.map((h) => (
                <tr key={h.period_start} className="border-t">
                  <td className="p-3">{month(h.period_start)}</td>
                  <td className="p-3 text-right font-mono">{formatBytes(h.rx_bytes)}</td>
                  <td className="p-3 text-right font-mono">{formatBytes(h.tx_bytes)}</td>
                  <td className="p-3 text-right font-mono">{formatBytes(h.total_bytes)}</td>
                  <td className="p-3 text-right font-mono">{h.quota_gb > 0 ? `${h.quota_gb} GB${h.exceeded ? " (exceeded)" : ""}` : "Unlimited"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
    </div>
  )
}

function BandwidthCell({ vmId, iface }: { vmId: string; iface: NetworkIface }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState<number>(iface.bandwidth_limit)
  const setBandwidth = useSetVMBandwidth(vmId)
  const saved = useFlash()

  if (!editing) {
    return (
      <div className="flex items-center gap-2">
        <span className="font-mono">{formatMbps(iface.bandwidth_limit)}</span>
        {saved.active() && <Check className="w-3 h-3 text-success" aria-label="Saved" />}
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
      {
        onSuccess: () => { setEditing(false); saved.trigger() },
        onError: (e) => toast.error(`Could not set bandwidth: ${(e as Error).message}`),
      },
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
        {setBandwidth.isPending ? <Loader2 className="w-3 h-3 animate-spin" aria-label="Saving" /> : "Save"}
      </button>
      <button
        type="button"
        onClick={() => setEditing(false)}
        className="h-8 px-2 rounded-md border bg-background text-xs font-medium hover:bg-muted"
        aria-label="Cancel"
      >
        <X className="w-3 h-3" aria-hidden="true" />
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

// Reverse DNS (PTR) for one address, edited in place. For a customer the server
// only accepts a hostname that already resolves to the address, so the error
// it returns is shown as written: it says exactly which record is missing.
function RDNSCell({ vmId, ipAddress, rdns, verified }: { vmId: string; ipAddress: string; rdns: string; verified: boolean }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(rdns)
  const [error, setError] = useState<string | null>(null)
  const setRDNS = useSetVMRDNS(vmId)
  const saved = useFlash()
  const inputId = `rdns-${ipAddress}`

  if (!editing) {
    return (
      <div className="flex items-center gap-2">
        <span className={`font-mono text-xs break-all ${rdns ? "" : "text-muted-foreground"}`}>{rdns || "Not set"}</span>
        {saved.active() && <Check className="w-3 h-3 shrink-0 text-success" aria-label="Saved" />}
        <button
          type="button"
          onClick={() => { setValue(rdns); setError(null); setEditing(true) }}
          className="shrink-0 rounded-sm p-1 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`Edit reverse DNS for ${ipAddress}`}
        >
          <Pencil className="w-3 h-3" aria-hidden="true" />
        </button>
      </div>
    )
  }

  const save = () => {
    setError(null)
    setRDNS.mutate(
      { ipAddress, rdns: value.trim() },
      {
        onSuccess: () => { setEditing(false); saved.trigger() },
        onError: (err) => setError(err.message),
      },
    )
  }

  return (
    <form className="min-w-[16rem]" onSubmit={(e) => { e.preventDefault(); save() }}>
      <label htmlFor={inputId} className="sr-only">Reverse DNS hostname for {ipAddress}</label>
      <div className="flex items-center gap-1.5">
        <input
          id={inputId}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Escape") setEditing(false) }}
          placeholder="host.example.com"
          autoFocus
          spellCheck={false}
          autoComplete="off"
          aria-invalid={!!error}
          aria-describedby={`${inputId}-help`}
          className="h-8 w-full min-w-0 rounded-md border bg-background px-2 font-mono text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        />
        <button
          type="submit"
          disabled={setRDNS.isPending}
          className="h-8 shrink-0 rounded-md bg-primary px-2 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
        >
          {setRDNS.isPending ? <Loader2 className="w-3 h-3 animate-spin" aria-label="Saving" /> : "Save"}
        </button>
        <button
          type="button"
          onClick={() => setEditing(false)}
          className="h-8 shrink-0 rounded-md border bg-background px-2 text-xs font-medium hover:bg-muted"
          aria-label="Cancel"
        >
          <X className="w-3 h-3" aria-hidden="true" />
        </button>
      </div>
      <p id={`${inputId}-help`} role={error ? "alert" : undefined} className={`mt-1 text-xs ${error ? "text-destructive" : "text-muted-foreground"}`}>
        {error || (verified ? `Must already resolve to ${ipAddress}. Leave empty to remove.` : "Leave empty to remove.")}
      </p>
    </form>
  )
}

// One action per row: what it does on the left, the button on the right. Used
// where an action sits beside the thing it changes instead of in a button wall.
function ActionRow({ title, description, children }: { title: string; description: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 px-6 py-4">
      <div className="min-w-0 flex-1 basis-64">
        <p className="text-sm font-medium text-foreground">{title}</p>
        <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">{children}</div>
    </div>
  )
}

// Show an attached ISO by its file name; the full URL or path is noise here.
function isoLabel(iso: string) {
  const name = iso.split(/[?#]/)[0].split("/").filter(Boolean).pop()
  return name || iso
}

const VM_TABS = ["overview", "console", "network", "snapshots", "backups", "firewall", "logs", "settings"]

// Port forwards: a port on the node's public address sent on to this VM.
function PortForwardsCard({ vmId }: { vmId: string }) {
  const confirm = useConfirm()
  const { data: forwards, isLoading, error, refetch } = usePortForwards(vmId)
  const create = useCreatePortForward(vmId)
  const remove = useDeletePortForward(vmId)
  const [adding, setAdding] = useState(false)
  const [ext, setExt] = useState("")
  const [int, setInt] = useState("")
  const [proto, setProto] = useState<"tcp" | "udp">("tcp")
  const [source, setSource] = useState("")
  const [err, setErr] = useState("")

  const validPort = (v: string) => { const n = Number(v); return Number.isInteger(n) && n >= 1 && n <= 65535 }

  const handleAdd = async () => {
    if (!validPort(ext) || !validPort(int)) { setErr("Enter both ports as numbers from 1 to 65535."); return }
    setErr("")
    try {
      await create.mutateAsync({ external_port: Number(ext), internal_port: Number(int), protocol: proto, source_ip: source.trim() || undefined })
      toast.success("Port forward added")
      setAdding(false); setExt(""); setInt(""); setSource("")
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6">
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
          Port Forwards
        </h2>
        <Button size="sm" variant="outline" onClick={() => setAdding((v) => !v)}><Plus className="w-4 h-4" aria-hidden="true" />Add Forward</Button>
      </div>
      <p className="text-sm text-muted-foreground mb-4">Sends a port on the host&apos;s public address to a port on this VM. Useful when the VM has a private address only.</p>

      {adding && (
        <div className="flex flex-wrap items-end gap-2 mb-4 p-3 border rounded-md bg-muted">
          <div>
            <label htmlFor="pf-ext" className="text-xs font-medium text-muted-foreground block mb-1">Host port</label>
            <Input id="pf-ext" inputMode="numeric" value={ext} onChange={(e) => setExt(e.target.value)} placeholder="8080" className="border w-28 font-mono" />
          </div>
          <div>
            <label htmlFor="pf-int" className="text-xs font-medium text-muted-foreground block mb-1">VM port</label>
            <Input id="pf-int" inputMode="numeric" value={int} onChange={(e) => setInt(e.target.value)} placeholder="80" className="border w-28 font-mono" />
          </div>
          <div>
            <label htmlFor="pf-proto" className="text-xs font-medium text-muted-foreground block mb-1">Protocol</label>
            <select id="pf-proto" value={proto} onChange={(e) => setProto(e.target.value as "tcp" | "udp")} className="h-9 border rounded-md bg-background px-2 text-sm cursor-pointer">
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </select>
          </div>
          <div>
            <label htmlFor="pf-src" className="text-xs font-medium text-muted-foreground block mb-1">Allowed source (optional)</label>
            <Input id="pf-src" value={source} onChange={(e) => setSource(e.target.value)} placeholder="Any address" className="border w-44 font-mono" />
          </div>
          <Button size="sm" onClick={handleAdd} disabled={create.isPending}>
            {create.isPending ? <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" /> : <Check className="w-4 h-4" aria-hidden="true" />}Add
          </Button>
          <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setErr("") }}>Cancel</Button>
        </div>
      )}
      {err && <p role="alert" className="text-xs text-destructive font-medium mb-3">{err}</p>}

      {isLoading ? (
        <SectionSkeleton />
      ) : error ? (
        <SectionError message={(error as Error).message} onRetry={() => refetch()} />
      ) : !forwards?.length ? (
        <SectionEmpty icon={ArrowRightLeft} message="No port forwards." />
      ) : (
        <div className="border overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted text-xs text-muted-foreground">
              <tr>
                <th className="text-left p-3">Host port</th>
                <th className="text-left p-3">VM port</th>
                <th className="text-left p-3">Protocol</th>
                <th className="text-left p-3">Allowed source</th>
                <th className="text-right p-3"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {forwards.map((pf) => (
                <tr key={pf.id} className="border-t">
                  <td className="p-3 font-mono">{pf.external_port}</td>
                  <td className="p-3 font-mono">{pf.internal_port}</td>
                  <td className="p-3 uppercase">{pf.protocol}</td>
                  <td className="p-3 font-mono">{pf.source_ip && pf.source_ip !== "0.0.0.0/0" ? pf.source_ip : "Any"}</td>
                  <td className="p-3 text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => confirm({
                        title: `Remove forward on port ${pf.external_port}?`,
                        description: `Traffic to host port ${pf.external_port} will stop reaching this VM.`,
                        confirmLabel: "Remove",
                        destructive: true,
                        action: () => remove.mutateAsync(pf.id),
                      })}
                    >
                      Remove
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// The schedule is stored as a cron line; the form offers the three shapes
// people ask for and shows anything else as it is.
const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

function describeCron(cron: string): { every: "daily" | "weekly" | "monthly"; hour: number; text: string } | null {
  const m = cron.trim().match(/^0 (\d{1,2}) (\*|1) \* (\*|[0-6])$/)
  if (!m) return null
  const hour = Number(m[1])
  const at = `${String(hour).padStart(2, "0")}:00`
  if (m[2] === "1" && m[3] === "*") return { every: "monthly", hour, text: `On the 1st of each month at ${at}` }
  if (m[2] === "*" && m[3] !== "*") return { every: "weekly", hour, text: `Every ${WEEKDAYS[Number(m[3])]} at ${at}` }
  if (m[2] === "*" && m[3] === "*") return { every: "daily", hour, text: `Every day at ${at}` }
  return null
}

function BackupScheduleCard({ vmId }: { vmId: string }) {
  const confirm = useConfirm()
  const { data: schedules, isLoading, error, refetch } = useBackupSchedules(vmId)
  const save = useCreateBackupSchedule(vmId)
  const remove = useDeleteBackupSchedule(vmId)
  const current = schedules?.[0]
  const parsed = current ? describeCron(current.schedule) : null

  const [editing, setEditing] = useState(false)
  const [every, setEvery] = useState<"daily" | "weekly" | "monthly">("daily")
  const [hour, setHour] = useState(2)
  const [keep, setKeep] = useState("7")
  const [err, setErr] = useState("")

  const startEdit = () => {
    if (parsed) { setEvery(parsed.every); setHour(parsed.hour) }
    if (current?.retention_policy?.keep_last) setKeep(String(current.retention_policy.keep_last))
    setErr("")
    setEditing(true)
  }

  const handleSave = async () => {
    const keepLast = Number(keep)
    if (!Number.isInteger(keepLast) || keepLast < 1 || keepLast > 365) { setErr("Keep between 1 and 365 backups."); return }
    const cron = every === "monthly" ? `0 ${hour} 1 * *` : every === "weekly" ? `0 ${hour} * * 0` : `0 ${hour} * * *`
    try {
      await save.mutateAsync({ schedule: cron, retention_policy: { keep_last: keepLast } })
      toast.success("Backup schedule saved")
      setEditing(false)
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6 mb-6">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-semibold text-foreground">Automatic backups</h2>
        {!editing && !isLoading && !error && (
          <div className="flex items-center gap-2">
            {current && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => confirm({
                  title: "Turn off automatic backups?",
                  description: "No new backups will be made on a schedule. Existing backups are kept.",
                  confirmLabel: "Turn off",
                  action: () => remove.mutateAsync(current.id),
                })}
              >
                Turn off
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={startEdit}>{current ? "Change" : "Set up"}</Button>
          </div>
        )}
      </div>

      {isLoading ? (
        <Skeleton className="h-5 w-64" />
      ) : error ? (
        <SectionError message={(error as Error).message} onRetry={() => refetch()} />
      ) : editing ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <div>
              <label htmlFor="bs-every" className="text-xs font-medium text-muted-foreground block mb-1">How often</label>
              <select id="bs-every" value={every} onChange={(e) => setEvery(e.target.value as typeof every)} className="h-9 border rounded-md bg-background px-2 text-sm cursor-pointer">
                <option value="daily">Every day</option>
                <option value="weekly">Every Sunday</option>
                <option value="monthly">1st of each month</option>
              </select>
            </div>
            <div>
              <label htmlFor="bs-hour" className="text-xs font-medium text-muted-foreground block mb-1">Start time (server time)</label>
              <select id="bs-hour" value={hour} onChange={(e) => setHour(Number(e.target.value))} className="h-9 border rounded-md bg-background px-2 text-sm font-mono cursor-pointer">
                {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
              </select>
            </div>
            <div>
              <label htmlFor="bs-keep" className="text-xs font-medium text-muted-foreground block mb-1">Backups to keep</label>
              <Input id="bs-keep" inputMode="numeric" value={keep} onChange={(e) => setKeep(e.target.value)} className="border w-24 font-mono" />
            </div>
            <Button size="sm" onClick={handleSave} disabled={save.isPending}>
              {save.isPending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}Save
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>
          </div>
          <p className="text-xs text-muted-foreground">Older backups are deleted once there are more than this number.</p>
          {err && <p role="alert" className="text-xs text-destructive font-medium">{err}</p>}
        </div>
      ) : current ? (
        <dl className="grid grid-cols-1 sm:grid-cols-3 gap-4 text-sm">
          <div><dt className="text-xs text-muted-foreground">Runs</dt><dd className="font-medium">{parsed ? parsed.text : <span className="font-mono">{current.schedule}</span>}</dd></div>
          <div><dt className="text-xs text-muted-foreground">Keeps</dt><dd className="font-medium">{current.retention_policy?.keep_last ? `Last ${current.retention_policy.keep_last} backups` : "All backups"}</dd></div>
          <div><dt className="text-xs text-muted-foreground">Next run</dt><dd className="font-medium">{current.next_run_at && !current.next_run_at.startsWith("0001") ? formatDate(current.next_run_at) : "Not scheduled yet"}</dd></div>
        </dl>
      ) : (
        <p className="text-sm text-muted-foreground">Off. Backups are made only when you create one.</p>
      )}
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
  const { data: disks, isLoading, error, refetch } = useVMDisks(vmId)
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
    <div className="bg-card text-card-foreground border rounded-lg p-6">
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
          Data Disks
        </h2>
        <Button size="sm" variant="outline" onClick={() => setAdding((v) => !v)}><Plus className="w-4 h-4" />Add Disk</Button>
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
      ) : error ? (
        <SectionError message={(error as Error).message} onRetry={() => refetch()} />
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
                    <Button size="sm" variant="destructive" onClick={() => handleDetach(d.device)} disabled={detach.isPending && detach.variables?.device === d.device}>
                      {detach.isPending && detach.variables?.device === d.device ? <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" /> : <Trash2 className="w-4 h-4" />}Detach
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

function BandwidthUsageCard({ vmId, canEditQuota }: { vmId: string; canEditQuota: boolean }) {
  const { data: bandwidth, isLoading, error, refetch } = useVMBandwidth(vmId)
  const setQuota = useSetBandwidthQuota(vmId)
  const [editingQuota, setEditingQuota] = useState(false)
  const [quotaInput, setQuotaInput] = useState("")
  const quotaSaved = useFlash()

  if (isLoading) return <SectionSkeleton />
  if (error) {
    return (
      <div className="bg-card text-card-foreground border rounded-lg">
        <SectionError message={(error as Error).message} onRetry={() => refetch()} />
      </div>
    )
  }
  if (!bandwidth) return null

  const handleSaveQuota = async () => {
    const gb = parseInt(quotaInput, 10)
    if (Number.isNaN(gb) || gb < 0) return
    try {
      await setQuota.mutateAsync(gb)
      setEditingQuota(false)
      quotaSaved.trigger()
    } catch (e) {
      toast.error(`Could not save quota: ${(e as Error).message}`)
    }
  }

  const usagePercent = bandwidth.quota_gb > 0 ? Math.min(bandwidth.usage_percent, 100) : 0
  const isWarning = bandwidth.quota_gb > 0 && bandwidth.usage_percent >= 80
  const isDanger = bandwidth.exceeded

  return (
    <div className={`bg-card text-card-foreground border rounded-lg p-6 ${isDanger ? 'border-destructive' : ''}`}>
      <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
        Bandwidth Usage
        {isDanger && <span className="text-xs font-semibold text-destructive">Over the limit</span>}
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
              className={`h-full transition-all duration-300 ${isDanger ? 'bg-destructive' : isWarning ? 'bg-warning' : 'bg-foreground'}`}
              style={{ width: `${usagePercent}%` }}
            />
          </div>
          <div className="flex justify-between mt-1">
            <span className="text-xs text-muted-foreground">{bandwidth.period_start}</span>
            <span className="text-xs text-muted-foreground">{bandwidth.period_end}</span>
          </div>
        </div>
      )}

      {/* Monthly quota control. Admin only: for customers the quota comes from the plan. */}
      {canEditQuota && (
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
            {quotaSaved.active() ? <Check className="w-4 h-4" aria-hidden="true" /> : <Activity className="w-4 h-4" />}{quotaSaved.active() ? "Saved" : "Set monthly quota"}
          </Button>
        )}
      </div>
      )}
      <BandwidthHistory vmId={vmId} />
    </div>
  )
}

// InitialPassword lets the owner read the root password the panel generated,
// while it is still kept. It is never emailed: the "server is ready" email
// points here. Reading it needs a signed-in session and is recorded.
function InitialPassword({ vmId }: { vmId: string }) {
  const { data } = useInitialPassword(vmId)
  const reveal = useRevealInitialPassword(vmId)
  const forget = useForgetInitialPassword(vmId)
  const confirm = useConfirm()
  const { copy, copied } = useCopy()
  const [shown, setShown] = useState<string | null>(null)
  if (!data?.available) return null
  const until = data.expires_at ? new Date(data.expires_at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : ""
  return (
    <div className="mt-3 border-t border-hair pt-3">
      <p className="text-xs font-medium text-muted-foreground">Root password</p>
      {shown ? (
        <p className="mt-1 font-medium">
          <span className="font-mono break-all">{shown}</span>
          <button type="button" onClick={() => copy(shown, "initial-pw")} className="ml-2 border-b border-border pb-px text-xs font-semibold hover:border-foreground active:translate-y-px">
            {copied("initial-pw") ? "Copied" : "Copy"}
          </button>
          <button type="button" onClick={() => setShown(null)} className="ml-3 border-b border-border pb-px text-xs font-semibold hover:border-foreground active:translate-y-px">
            Hide
          </button>
        </p>
      ) : (
        <Button
          size="sm"
          variant="outline"
          className="mt-1.5"
          disabled={reveal.isPending}
          onClick={async () => {
            try {
              setShown((await reveal.mutateAsync()).password)
            } catch (e) {
              toast.error(`Could not show the password: ${(e as Error).message}`)
            }
          }}
        >
          {reveal.isPending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
          Show the password
        </Button>
      )}
      <p className="mt-2 text-xs text-muted-foreground">
        Set when this VM was installed. Kept until {until}, then erased. Each time it is shown is recorded.{" "}
        <button
          type="button"
          className="font-semibold text-foreground underline underline-offset-2"
          onClick={async () => {
            const ok = await confirm({
              title: "Erase the stored password now?",
              description: "Do this once you have saved it somewhere safe. It cannot be shown again; if you lose it, set a new one from the Console tab.",
              confirmLabel: "Erase it",
              destructive: true,
              action: () => forget.mutateAsync(),
            })
            if (ok) {
              setShown(null)
              toast.success("Stored password erased")
            }
          }}
        >
          Erase it now
        </button>
      </p>
    </div>
  )
}

// TransferSummary is the month's data transfer in one line, for the overview.
// The full breakdown and its history live on the Network tab.
function TransferSummary({ vmId, onOpen }: { vmId: string; onOpen: () => void }) {
  const { data: bw, isLoading, error, refetch } = useVMBandwidth(vmId)
  const limited = !!bw && bw.quota_gb > 0
  return (
    <section className="p-4">
      <h2 className="mb-1.5 text-xs font-medium text-muted-foreground">Transfer this month</h2>
      {isLoading ? (
        <Skeleton className="h-10 w-full" aria-label="Loading transfer" />
      ) : error ? (
        <p className="text-muted-foreground">
          Could not load.{" "}
          <button type="button" onClick={() => refetch()} className="font-semibold text-foreground underline underline-offset-2">Try again</button>
        </p>
      ) : !bw ? (
        <p className="text-muted-foreground">Nothing recorded yet.</p>
      ) : (
        <>
          <p>
            <span className="font-semibold">{bw.used_gb.toFixed(bw.used_gb < 10 ? 2 : 1)} GB</span>
            {limited ? ` of ${bw.quota_gb.toLocaleString()} GB` : " used, no limit"}
          </p>
          {limited && (
            <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
              <div className={`h-full ${bw.exceeded ? "bg-destructive" : "bg-foreground"}`} style={{ width: `${Math.min(bw.usage_percent, 100)}%` }} />
            </div>
          )}
          <p className={`mt-1.5 text-xs ${bw.exceeded ? "font-semibold text-destructive" : "text-muted-foreground"}`}>
            {bw.exceeded ? "Over the monthly limit. " : ""}
            {bw.period_end ? `Counter resets after ${bw.period_end}.` : ""}
          </p>
          <button type="button" onClick={onOpen} className="mt-2 text-xs font-semibold underline underline-offset-2">Download, upload and history</button>
        </>
      )}
    </section>
  )
}

// --- Main Component ---

// VMDetail is the VM page for both audiences. Admin mode is the operator view.
// Client mode is a customer's own VM: it leaves out everything the API refuses
// customers (resize, migrate, clone, owner, IP and bandwidth changes, VLAN,
// console repair), the data disks, and anything that names a node.
export function VMDetail({ mode }: { mode: "admin" | "client" }) {
  const isAdmin = mode === "admin"
  const basePath = isAdmin ? "/vms" : "/client/vms"
  const backLabel = isAdmin ? "Back to VMs" : "Back to my VMs"
  const rebuildWord = isAdmin ? "Rebuild" : "Reinstall"
  const confirm = useConfirm()
  const params = useParams()
  const router = useRouter()
  const vmId = params.id as string

  // State
  const { copy, copied } = useCopy()
  // A credential returned once by the server; kept open until dismissed.
  const [secret, setSecret] = useState<{ title: string; value: string } | null>(null)
  const [rebuildDialogOpen, setRebuildDialogOpen] = useState(false)
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [selectedTemplate, setSelectedTemplate] = useState("")
  const [confirmVMName, setConfirmVMName] = useState("")
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [consoleActive, setConsoleActive] = useState(false)
  const [tab, setTabState] = useState("overview")
  useEffect(() => {
    const fromUrl = new URLSearchParams(window.location.search).get("tab")
    if (fromUrl && VM_TABS.includes(fromUrl)) setTabState(fromUrl)
  }, [])
  const setTab = useCallback((next: string) => {
    setTabState(next)
    const url = new URL(window.location.href)
    if (next === "overview") url.searchParams.delete("tab")
    else url.searchParams.set("tab", next)
    window.history.replaceState(null, "", url)
  }, [])
  const [snapshotName, setSnapshotName] = useState("")
  const [createSnapshotOpen, setCreateSnapshotOpen] = useState(false)
  const [imageName, setImageName] = useState("")
  const [createImageOpen, setCreateImageOpen] = useState(false)
  const [isoDialogOpen, setIsoDialogOpen] = useState(false)
  const [selectedISOUrl, setSelectedISOUrl] = useState("")
  const [manualISOUrl, setManualISOUrl] = useState("")
  const [migrateOpen, setMigrateOpen] = useState(false)
  const [migrateNodeId, setMigrateNodeId] = useState("")
  // Resize (admin-only, VM must be stopped). Kept as strings so the inputs can
  // be cleared while typing instead of snapping back to 0.
  const [resizeOpen, setResizeOpen] = useState(false)
  const [resizeCpu, setResizeCpu] = useState("")
  const [resizeRam, setResizeRam] = useState("")
  const [resizeDisk, setResizeDisk] = useState("")
  // Rebuild options
  const [rebuildPassword, setRebuildPassword] = useState("")
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
  const { data: activity, isLoading: activityLoading, error: activityError, refetch: refetchActivity } = useVMActivity(vmId)
  const { data: metrics } = useVMMetrics(vmId)
  const vmAction = useVMAction(vmId)
  const { transitionOf, begin, cancel } = useVMTransitions(vm ? [vm] : [])
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
  const { data: allNodes } = useNodes(isAdmin)
  const updateVM = useUpdateVM(vmId)
  const [reassignOpen, setReassignOpen] = useState(false)
  const [reassignUserId, setReassignUserId] = useState("")
  const handleReassign = useCallback(async () => {
    if (!reassignUserId) return
    try {
      await updateVM.mutateAsync({ user_id: reassignUserId })
      setReassignOpen(false)
      setReassignUserId("")
      toast.success("VM owner reassigned")
      refetchVM()
    } catch (err) {
      toast.error(`Reassign failed: ${(err as Error).message}`)
    }
  }, [updateVM, reassignUserId, refetchVM])
  const { data: snapshots, isLoading: snapshotsLoading, error: snapshotsError, refetch: refetchSnapshots } = useSnapshots(vmId)
  const createSnapshot = useCreateSnapshot(vmId)
  const restoreSnapshot = useRestoreSnapshot(vmId)
  const deleteSnapshot = useDeleteSnapshot(vmId)
  const { data: backups, isLoading: backupsLoading, error: backupsError, refetch: refetchBackups } = useBackups(vmId)
  const createBackup = useCreateBackup(vmId)
  const deleteBackupMutation = useDeleteBackup(vmId)
  const restoreBackup = useRestoreBackup(vmId)
  const { data: firewallRules, isLoading: firewallLoading, error: firewallError, refetch: refetchFirewall } = useFirewallRules(vmId)
  const deleteFirewallRule = useDeleteFirewallRule(vmId)
  const { data: templates } = useTemplates()
  // Same rule as the create wizard: inactive templates and "/imported"
  // placeholders have no base image, so building from them fails on the node.
  const installableTemplates = (templates ?? []).filter((t) => {
    const path = (t.image_path || "").trim()
    return t.is_active && path !== "" && path !== "/imported"
  })
  // Only nodes that can take work; migrating also needs a node other than this one.
  const activeNodes = (allNodes ?? []).filter((n) => n.status === "active")
  const migrateTargets = activeNodes.filter((n) => n.id !== vm?.node_id)
  const cloneTarget = cloneNodeId || vm?.node_id || ""
  const cloneTargetValid = activeNodes.some((n) => n.id === cloneTarget)
  const { data: isoTemplates } = useTemplates({ type: "iso" }, isAdmin)
  const { data: vmNetworks } = useVMNetworks(vmId)
  const { data: ipPools } = useIPPools(isAdmin)
  const assignIP = useAssignVMIP(vmId)
  const releaseIP = useReleaseVMIP(vmId)
  const [assignIPOpen, setAssignIPOpen] = useState(false)
  // IPv6: a routed prefix or one address, from a pool on this VM's node.
  const assignIPv6 = useAssignVMIPv6(vmId)
  const releaseIPv6 = useReleaseVMIPv6(vmId)
  const [assignIPv6Open, setAssignIPv6Open] = useState(false)
  const [assignIPv6PoolID, setAssignIPv6PoolID] = useState("")
  // Set once a delete has been accepted; drives the progress dialog.
  const [deletingVM, setDeletingVM] = useState<{ id: string; hostname: string } | null>(null)
  const [assignPoolID, setAssignPoolID] = useState("")

  // Handlers
  const handleAction = useCallback(async (action: string) => {
    if (!vm) return
    setActionLoading(action)
    try {
      await vmAction.mutateAsync(action)
      // Accepted, not done: the badge shows the transition until the status moves.
      begin(vm.id, action, vm.status)
      const verb = action === "force-stop" ? "Force stop" : action.charAt(0).toUpperCase() + action.slice(1)
      toast.success(`${verb} requested for ${vm.hostname}`)
      refetchVM()
    } catch (err) {
      cancel(vm.id)
      toast.error(`Failed to ${action} VM: ${(err as Error).message}`)
    } finally {
      setActionLoading(null)
    }
  }, [vm, vmAction, refetchVM, begin, cancel])

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
      toast.error(`Failed to delete VM: ${(err as Error).message}`)
    }
  }, [deleteVM, vmId, vm?.hostname])

  const handleAttachISO = useCallback(async () => {
    const url = (manualISOUrl.trim() || selectedISOUrl).trim()
    if (!url) {
      toast.error("Pick an ISO or enter a URL")
      return
    }
    try {
      await attachISO.mutateAsync(url)
      toast.success("ISO attach queued. The VM will boot from it on next start")
      // ponytail: the job has no completion event, so re-read after a pause.
      setTimeout(() => refetchVM(), 4000)
      setIsoDialogOpen(false)
      setSelectedISOUrl("")
      setManualISOUrl("")
    } catch (err) {
      toast.error(`Failed to attach ISO: ${(err as Error).message}`)
    }
  }, [attachISO, manualISOUrl, selectedISOUrl, refetchVM])

  const handleDetachISO = useCallback(async () => {
    try {
      await detachISO.mutateAsync()
      toast.success("ISO detach queued. Boot order restored to disk")
      setTimeout(() => refetchVM(), 4000)
    } catch (err) {
      toast.error(`Failed to detach ISO: ${(err as Error).message}`)
    }
  }, [detachISO, refetchVM])

  const handleRescue = useCallback(async () => {
    try {
      await rescueVM.mutateAsync(undefined)
      toast.success("Rescue ISO attached. Start the VM to boot into rescue")
    } catch (err) {
      toast.error(`Failed to enter rescue mode: ${(err as Error).message}`)
    }
  }, [rescueVM])

  const handleUnrescue = useCallback(async () => {
    try {
      await unrescueVM.mutateAsync()
      toast.success("Rescue ISO detached. Start the VM to boot from disk")
    } catch (err) {
      toast.error(`Failed to exit rescue mode: ${(err as Error).message}`)
    }
  }, [unrescueVM])

  const handleRegenerateVNC = useCallback(async () => {
    try {
      const res = await regenerateVNC.mutateAsync()
      setSecret({ title: "New VNC password", value: res.vnc_password })
    } catch (err) {
      toast.error(`Failed to regenerate VNC password: ${(err as Error).message}`)
    }
  }, [regenerateVNC])

  const handleToggleConsole = useCallback(async (enabled: boolean) => {
    try {
      await setConsole.mutateAsync(enabled)
      toast.success(enabled ? "Console enabled" : "Console disabled, active sessions dropped")
    } catch (err) {
      toast.error(`Failed to update console: ${(err as Error).message}`)
    }
  }, [setConsole])

  const handleMigrate = useCallback(async () => {
    if (!migrateNodeId) {
      toast.error("Select a destination node")
      return
    }
    try {
      await migrateVM.mutateAsync({ dest_node_id: migrateNodeId, live: true, copy_storage: true })
      toast.success("VM migrated to destination node")
      setMigrateOpen(false)
      setMigrateNodeId("")
    } catch (err) {
      toast.error(`Migration failed: ${(err as Error).message}`)
    }
  }, [migrateVM, migrateNodeId])

  // Prefill from the VM's current spec each time the dialog opens, so the
  // fields always start from reality rather than a stale previous edit.
  const openResize = useCallback(() => {
    if (!vm) return
    setResizeCpu(String(vm.resources.cpu))
    setResizeRam(String(vm.resources.ram))
    setResizeDisk(String(vm.resources.disk))
    setResizeOpen(true)
  }, [vm])

  const handleResize = useCallback(async () => {
    const cpu = Number(resizeCpu)
    const ram = Number(resizeRam)
    const disk = Number(resizeDisk)
    if (!Number.isInteger(cpu) || !Number.isInteger(ram) || !Number.isInteger(disk)) {
      toast.error("CPU, RAM and disk must be whole numbers")
      return
    }
    // Mirror the server's grow-only rule here so the user finds out before a
    // round trip; the backend rejects it regardless.
    if (vm && disk < vm.resources.disk) {
      toast.error(`Disk can only be grown (currently ${vm.resources.disk} GB)`)
      return
    }
    try {
      await updateVM.mutateAsync({ resources: { cpu, ram, disk } })
      toast.success("Resize queued, applied on next boot")
      setResizeOpen(false)
      refetchVM()
    } catch (err) {
      toast.error(`Resize failed: ${(err as Error).message}`)
    }
  }, [updateVM, resizeCpu, resizeRam, resizeDisk, vm, refetchVM])

  const handleCreateSnapshot = useCallback(async () => {
    if (!snapshotName.trim()) return
    try {
      await createSnapshot.mutateAsync({ vm_id: vmId, name: snapshotName })
      toast.success("Snapshot created")
      setSnapshotName("")
      setCreateSnapshotOpen(false)
      refetchSnapshots()
    } catch (err) {
      toast.error(`Failed to create snapshot: ${(err as Error).message}`)
    }
  }, [createSnapshot, vmId, snapshotName, refetchSnapshots])

  const createImage = useCreateImage()
  const handleCreateImage = useCallback(async () => {
    if (!imageName.trim()) return
    try {
      await createImage.mutateAsync({ vm_id: vmId, name: imageName.trim() })
      toast.success(
        "Image capture started. Track progress on the Images page",
        isAdmin ? undefined : { action: { label: "Images", onClick: () => router.push("/client/images") } },
      )
      setImageName("")
      setCreateImageOpen(false)
    } catch (err) {
      toast.error(`Failed to create image: ${(err as Error).message}`)
    }
  }, [createImage, vmId, imageName, isAdmin, router])

  const handleRestoreSnapshot = useCallback(async (snapshotId: string) => {
    try {
      await restoreSnapshot.mutateAsync(snapshotId)
      toast.success("Snapshot restored")
      refetchVM()
    } catch (err) {
      toast.error(`Failed to restore snapshot: ${(err as Error).message}`)
    }
  }, [restoreSnapshot, refetchVM])

  const handleDeleteSnapshot = useCallback(async (snapshotId: string) => {
    try {
      await deleteSnapshot.mutateAsync(snapshotId)
      toast.success("Snapshot deleted")
      refetchSnapshots()
    } catch (err) {
      toast.error(`Failed to delete snapshot: ${(err as Error).message}`)
    }
  }, [deleteSnapshot, refetchSnapshots])

  const handleCreateBackup = useCallback(async () => {
    try {
      await createBackup.mutateAsync()
      toast.success("Backup started")
      refetchBackups()
    } catch (err) {
      toast.error(`Failed to create backup: ${(err as Error).message}`)
    }
  }, [createBackup, refetchBackups])

  const handleDeleteBackup = useCallback(async (backupId: string) => {
    try {
      await deleteBackupMutation.mutateAsync(backupId)
      toast.success("Backup deleted")
      refetchBackups()
    } catch (err) {
      toast.error(`Failed to delete backup: ${(err as Error).message}`)
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
      toast.success("Firewall rule added")
      refetchFirewall()
    } catch (err) {
      toast.error(`Failed to add rule: ${(err as Error).message}`)
    }
  }, [createFirewallRule, fwForm, vmId, refetchFirewall])
  const handleDeleteFirewallRule = useCallback(async (ruleId: string) => {
    try {
      await deleteFirewallRule.mutateAsync(ruleId)
      toast.success("Firewall rule deleted")
      refetchFirewall()
    } catch (err) {
      toast.error(`Failed to delete rule: ${(err as Error).message}`)
    }
  }, [deleteFirewallRule, refetchFirewall])

  const handleRebuild = async () => {
    if (confirmVMName !== vm?.hostname || !selectedTemplate) return
    try {
      const res = await rebuildVM.mutateAsync({
        template_id: selectedTemplate,
        preserve_ip: true,
        password: rebuildPassword || undefined,
        ssh_key_ids: rebuildKeyIds.length ? rebuildKeyIds : undefined,
      })
      setRebuildDialogOpen(false)
      setConfirmVMName("")
      setSelectedTemplate("")
      setRebuildPassword("")
      setRebuildKeyIds([])
      toast.success(`${rebuildWord} initiated`)
      if (res.root_password) setSecret({ title: "New root password", value: res.root_password })
    } catch (err) {
      toast.error(`Failed to rebuild VM: ${(err as Error).message}`)
    }
  }

  const handleResetPassword = async () => {
    if (!resetPwValue.trim()) return
    try {
      await resetPassword.mutateAsync(resetPwValue)
      setResetPwOpen(false)
      setResetPwValue("")
      toast.success(isAdmin ? "Root password reset enqueued (applied via guest agent)" : "Root password reset requested")
    } catch (err) {
      toast.error(`Failed to reset password: ${(err as Error).message}`)
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
      toast.success(`Clone "${res.vm.hostname}" created, provisioning…`)
      if (res.vm?.id) router.push(`/vms/${res.vm.id}`)
    } catch (err) {
      toast.error(`Failed to clone VM: ${(err as Error).message}`)
    }
  }

  // Loading state
  if (vmLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <nav className="flex items-center gap-2 mb-6">
          <Link href={basePath} className="flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors w-fit">
            <ArrowLeft className="w-4 h-4" aria-hidden="true" />{backLabel}
          </Link>
        </nav>
        <div className="bg-card text-card-foreground border rounded-lg p-6 mb-6">
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
          <Link href={basePath} className="flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors w-fit">
            <ArrowLeft className="w-4 h-4" aria-hidden="true" />{backLabel}
          </Link>
        </nav>
        <SectionError message={(vmError as Error)?.message || "VM not found"} onRetry={() => refetchVM()} />
      </div>
    )
  }

  const ramGB = Math.round(vm.resources.ram / 1024 * 10) / 10
  // A suspended VM is frozen for customers until support restores it.
  const suspended = vm.suspended || vm.status === "suspended"
  const locked = !isAdmin && suspended
  const powerBusy = locked || actionLoading !== null || !!transitionOf(vm.id)
  // A text button beside a value people paste elsewhere.
  const copyLink = (text: string, key: string, what: string) => (
    <button
      type="button"
      onClick={() => copy(text, key)}
      aria-label={copied(key) ? "Copied" : `Copy ${what}`}
      className="ml-2 border-b border-border pb-px text-xs font-semibold hover:border-foreground active:translate-y-px"
    >
      {copied(key) ? "Copied" : "Copy"}
    </button>
  )
  const primaryIP = vmNetworks?.[0]?.ip_address
  const lastBackup = [...(backups ?? [])].sort((x, y) => y.created_at.localeCompare(x.created_at))[0]

  return (
    <div className="max-w-7xl mx-auto">
      {/* Breadcrumb */}
      <nav className="flex items-center gap-2 mb-6">
        <Link href={basePath} className="flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors w-fit">
          <ArrowLeft className="w-4 h-4" aria-hidden="true" />{backLabel}
        </Link>
      </nav>

      {locked && (
        <div role="status" className="mb-6 flex items-center gap-3 rounded-lg border border-warning/50 bg-warning/10 p-4 text-sm font-medium text-foreground">
          <AlertCircle className="w-5 h-5 shrink-0" aria-hidden="true" />
          This VM is suspended. Contact support to restore service.
        </div>
      )}

      {/* Faceplate: what this machine is, its state, and its power controls.
          Dark in both themes; its colours come from .faceplate in globals.css. */}
      <div className="faceplate mb-6 rounded-lg border bg-card px-5 pb-4 pt-5">
        <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-4">
          <div className="flex min-w-0 flex-wrap items-center gap-x-5 gap-y-2">
            <StatusBadge variant="lamp" status={suspended ? "suspended" : vm.status} transition={transitionOf(vm.id)} />
            <VMNameEditor
              hostname={vm.hostname}
              headingClassName="text-2xl font-semibold text-foreground [font-stretch:118%] break-all"
              onRename={async (name) => {
                await updateVM.mutateAsync({ hostname: name })
                toast.success(`Renamed to "${name}"`)
              }}
            />
            {vm.rescue_mode && (
              <span className="rounded-sm border border-warning px-1.5 py-0.5 text-xs font-semibold text-warning">Rescue mode</span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {(["creating", "deleting"] as string[]).includes(vm.status) ? (
              // Mid-operation: power actions would conflict, so show what is happening instead.
              <Button variant="outline" size="sm" disabled>
                <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
                {vm.status === "creating" ? "Provisioning" : "Deleting"}
              </Button>
            ) : vm.status === "running" ? (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={powerBusy}
                  onClick={async () => {
                    const ok = await confirm({
                      title: `Restart ${vm.hostname}?`,
                      description: "It shuts down cleanly, then boots again. Expect about a minute offline.",
                      confirmLabel: "Restart",
                    })
                    if (ok) handleAction("restart")
                  }}
                >
                  {actionLoading === "restart" && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                  Restart
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={powerBusy}
                  onClick={async () => {
                    const ok = await confirm({
                      title: `Stop ${vm.hostname}?`,
                      description: "It shuts down cleanly and stays off until you start it. Disks and addresses are kept.",
                      confirmLabel: "Stop",
                    })
                    if (ok) handleAction("stop")
                  }}
                >
                  {actionLoading === "stop" && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                  Stop
                </Button>
              </>
            ) : (
              <Button variant="default" size="sm" onClick={() => handleAction("start")} disabled={powerBusy}>
                {actionLoading === "start" && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                Start
              </Button>
            )}
            <Button
              variant="outline"
              size="sm"
              onClick={() => { setTab("console"); if (vm.status === "running" && vm.console_enabled !== false) setConsoleActive(true) }}
            >
              Console
            </Button>
            {isAdmin && (
              <Button
                variant="outline"
                size="sm"
                disabled={actionLoading !== null || !!transitionOf(vm.id)}
                onClick={async () => {
                  if (suspended) { handleAction("unsuspend"); return }
                  const ok = await confirm({
                    title: `Suspend ${vm.hostname}?`,
                    description: "The VM is powered off and its owner can no longer start or change it until you unsuspend it. Unsuspending starts it again.",
                    confirmLabel: "Suspend",
                    destructive: true,
                  })
                  if (ok) handleAction("suspend")
                }}
              >
                {(actionLoading === "suspend" || actionLoading === "unsuspend") && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                {suspended ? "Unsuspend" : "Suspend"}
              </Button>
            )}
            {/* Deliberately quieter than Stop: the last resort for a VM that ignores a normal shutdown. */}
            {vm.status === "running" && (
              <Button
                variant="ghost"
                size="sm"
                className="text-muted-foreground"
                disabled={powerBusy}
                onClick={async () => {
                  const ok = await confirm({
                    title: `Force stop ${vm.hostname}?`,
                    description:
                      "This cuts the power at once, like pulling the plug. Anything not yet saved to disk may be lost. Try Stop first unless the VM is not responding.",
                    confirmLabel: "Force stop",
                    destructive: true,
                  })
                  if (ok) handleAction("force-stop")
                }}
              >
                {actionLoading === "force-stop" && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                Force stop
              </Button>
            )}
          </div>
        </div>
        <dl className="mt-4 flex flex-wrap gap-x-8 gap-y-3 border-t border-hair pt-4 text-sm">
          {(vmNetworks ?? []).length > 0 && (
            <div>
              <dt className="text-xs text-muted-foreground">IPv4</dt>
              {(vmNetworks ?? []).map((net) => (
                <dd key={net.id} className="font-medium">
                  <span className="font-mono">{net.ip_address}</span>
                  {copyLink(net.ip_address, `ip-${net.id}`, `address ${net.ip_address}`)}
                </dd>
              ))}
            </div>
          )}
          {vm.ipv6_address && (
            <div>
              <dt className="text-xs text-muted-foreground">IPv6</dt>
              <dd className="font-medium">
                <span className="font-mono break-all">{vm.ipv6_address}</span>
                {copyLink(vm.ipv6_address, "ipv6", `address ${vm.ipv6_address}`)}
              </dd>
            </div>
          )}
          <div>
            <dt className="text-xs text-muted-foreground">System</dt>
            <dd className="flex items-center gap-2 font-medium">
              <OSLogoTile name={templates?.find((t) => t.id === vm.os_template_id)?.name} className="h-5 w-5 rounded" />
              {templates?.find((t) => t.id === vm.os_template_id)?.name || "Unknown"}
            </dd>
          </div>
          {isAdmin ? (
            <div>
              <dt className="text-xs text-muted-foreground">Node</dt>
              <dd className="font-medium">{allNodes?.find((n) => n.id === vm.node_id)?.name || vm.node_name || vm.node_id.slice(0, 8)}</dd>
            </div>
          ) : (
            <div>
              <dt className="text-xs text-muted-foreground">Location</dt>
              <dd className="flex items-center gap-2 font-medium">
                {vm.region_country && <CountryFlag country={vm.region_country} />}
                {vm.region_name || "Unknown"}
              </dd>
            </div>
          )}
          <div>
            <dt className="text-xs text-muted-foreground">Created</dt>
            <dd className="font-medium">{formatDate(vm.created_at)}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">ID</dt>
            <dd className="font-medium">
              <span className="font-mono">{vm.id.slice(0, 8)}</span>
              {copyLink(vm.id, "vm-id", "the full VM ID")}
            </dd>
          </div>
        </dl>
      </div>

      {/* Dialogs. Opened from the tabs below; they render nothing until open. */}
      <>
            {/* Rebuild Dialog */}
            <Dialog open={rebuildDialogOpen} onOpenChange={setRebuildDialogOpen}>
              <DialogContent className="max-w-xl border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">{rebuildWord} VM</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    {isAdmin
                      ? "This will destroy the current VM and create a new one from a template. All data will be lost."
                      : "Erases this VM and installs a fresh operating system. All data on it will be lost. The VM keeps its IP address."}
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4 max-h-[60vh] overflow-y-auto">
                  <div>
                    <span id="rebuild-os-title" className="text-xs font-medium text-muted-foreground mb-2 block">Operating system</span>
                    {/* p-px keeps the selected card's outline from being clipped
                        by the dialog's scroll container. */}
                    <div className="p-px [&>div]:md:grid-cols-2 [&>div]:2xl:grid-cols-2">
                      <OSPicker templates={installableTemplates} value={selectedTemplate} onChange={setSelectedTemplate} labelledBy="rebuild-os-title" />
                    </div>
                  </div>

                  {/* Root password */}
                  <div>
                    <label htmlFor="rebuild-password" className="text-xs font-medium text-muted-foreground mb-2 block">Root password</label>
                    <Input
                      id="rebuild-password"
                      type="text"
                      value={rebuildPassword}
                      onChange={(e) => setRebuildPassword(e.target.value)}
                      placeholder="Leave empty to generate one"
                      autoComplete="off"
                      aria-describedby="rebuild-password-help"
                      className="border font-mono text-sm"
                    />
                    <p id="rebuild-password-help" className="mt-1 text-xs text-muted-foreground">
                      A generated password is shown once after you confirm, even when SSH keys are selected, so the VM can still be reached without the key.
                    </p>
                  </div>

                  {/* SSH keys */}
                  <div>
                    <span className="text-xs font-medium text-muted-foreground mb-2 block">SSH Keys</span>
                    {!sshKeys?.length ? (
                      <p className="text-xs text-muted-foreground">
                        No saved keys. Add some under{" "}
                        <Link href={isAdmin ? "/settings/ssh-keys" : "/client/settings/ssh-keys"} className="underline font-medium">Settings → SSH Keys</Link>.
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

                  <div className="rounded-md border border-destructive/40 bg-destructive/10 p-4">
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
                    {rebuildVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}{rebuildWord} VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Attach ISO Dialog */}
            <Dialog open={isoDialogOpen} onOpenChange={setIsoDialogOpen}>
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
                    <label htmlFor="iso-url" className="text-xs font-medium text-muted-foreground mb-2 block">{isAdmin ? "Or enter an ISO URL" : "ISO URL"}</label>
                    <Input id="iso-url" value={manualISOUrl} onChange={(e) => setManualISOUrl(e.target.value)} placeholder="https://example.com/installer.iso" className="border" />
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

            {isAdmin && (<>
            {/* Resize */}
            <Dialog open={resizeOpen} onOpenChange={setResizeOpen}>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Resize VM</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Change vCPU, RAM and disk. The VM must be stopped; the new spec is applied on next boot.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div className="grid grid-cols-3 gap-3">
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-2 block">vCPU</span>
                      <input
                        type="number" min={1} max={128} value={resizeCpu}
                        onChange={(e) => setResizeCpu(e.target.value)}
                        className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                      />
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-2 block">RAM (MB)</span>
                      <input
                        type="number" min={128} max={131072} step={128} value={resizeRam}
                        onChange={(e) => setResizeRam(e.target.value)}
                        className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                      />
                    </div>
                    <div>
                      <span className="text-xs font-medium text-muted-foreground mb-2 block">Disk (GB)</span>
                      <input
                        type="number" min={vm.resources.disk} value={resizeDisk}
                        onChange={(e) => setResizeDisk(e.target.value)}
                        className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                      />
                    </div>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Current: {vm.resources.cpu} vCPU · {vm.resources.ram} MB · {vm.resources.disk} GB
                  </p>
                  {vm.status !== "stopped" && (
                    <div className="flex items-center gap-2 p-3 bg-muted border">
                      <AlertCircle className="w-4 h-4 shrink-0" />
                      <p className="text-xs font-medium">VM must be stopped before it can be resized.</p>
                    </div>
                  )}
                  <div className="flex items-center gap-2 p-3 bg-muted border">
                    <HardDrive className="w-4 h-4 shrink-0" />
                    <p className="text-xs font-medium">
                      Disk can only be grown, never shrunk — and the guest still has to extend its own
                      partition and filesystem afterwards.
                    </p>
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setResizeOpen(false)}>Cancel</Button>
                  <Button onClick={handleResize} disabled={updateVM.isPending || vm.status !== "stopped"}>
                    {updateVM.isPending ? <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Resizing…</> : <><Cpu className="w-4 h-4 mr-2" />Resize</>}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            <Dialog open={migrateOpen} onOpenChange={setMigrateOpen}>
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
                    {migrateTargets.length === 0 ? (
                      <p className="text-xs text-muted-foreground">No other active node to migrate to.</p>
                    ) : (
                      <select
                        value={migrateNodeId}
                        onChange={(e) => setMigrateNodeId(e.target.value)}
                        className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                      >
                        <option value="">Select a node…</option>
                        {migrateTargets.map((n) => (
                          <option key={n.id} value={n.id}>{n.name} ({n.ip_address})</option>
                        ))}
                      </select>
                    )}
                  </div>
                  <div className="flex items-center gap-2 p-3 bg-muted border">
                    <Server className="w-4 h-4 shrink-0" />
                    <p className="text-xs font-medium">Requires node-to-node libvirt connectivity (SSH). The VM stays running during transfer.</p>
                  </div>
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setMigrateOpen(false)}>Cancel</Button>
                  <Button onClick={handleMigrate} disabled={migrateVM.isPending || !migrateTargets.some((n) => n.id === migrateNodeId)}>
                    {migrateVM.isPending ? <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Migrating…</> : <><ArrowRightLeft className="w-4 h-4 mr-2" />Migrate</>}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* Reassign owner (admin) */}
            <Dialog open={reassignOpen} onOpenChange={setReassignOpen}>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Reassign Owner</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    Transfer this VM to another user account.
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-4 py-4">
                  <div>
                    <label htmlFor="reassign-owner" className="text-xs font-medium text-muted-foreground mb-2 block">New Owner</label>
                    <UserPicker id="reassign-owner" value={reassignUserId} onChange={(id) => setReassignUserId(id)} />
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

            {/* Clone VM */}
            <Dialog open={cloneOpen} onOpenChange={setCloneOpen}>
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
                  {activeNodes.length === 0 ? (
                    <p className="text-xs text-muted-foreground">No active node to clone to.</p>
                  ) : (
                    <select
                      id="clone-node"
                      value={cloneTargetValid ? cloneTarget : ""}
                      onChange={(e) => setCloneNodeId(e.target.value)}
                      className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                    >
                      {!cloneTargetValid && <option value="">Select a node…</option>}
                      {activeNodes.map((n) => (
                        <option key={n.id} value={n.id}>
                          {n.name}{n.id === vm.node_id ? " (same node)" : ""}
                        </option>
                      ))}
                    </select>
                  )}
                  {cloneNodeId && cloneNodeId !== vm.node_id && (
                    <p className="text-xs text-muted-foreground">Cross-node clone pulls the disk over SSH from the source node — may take a while for large disks.</p>
                  )}
                  {vm.status === "running" && (
                    <p className="text-xs text-destructive font-medium">VM is running — stop it first to get a consistent copy.</p>
                  )}
                </div>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setCloneOpen(false)}>Cancel</Button>
                  <Button onClick={handleClone} disabled={cloneVM.isPending || vm.status === "running" || !cloneTargetValid}>
                    {cloneVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Copy className="w-4 h-4" />}
                    Clone VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            </>)}

            {/* Reset root password (guest agent) */}
            <Dialog open={resetPwOpen} onOpenChange={setResetPwOpen}>
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Reset Root Password</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    {isAdmin
                      ? "Sets a new root password on the running guest via the QEMU guest agent. The VM must be running and have qemu-guest-agent installed."
                      : "Sets a new root password inside the VM. The VM must be running."}
                  </DialogDescription>
                </DialogHeader>
                <div className="space-y-3 py-2">
                  <label htmlFor="reset-pw" className="text-xs font-medium text-muted-foreground block">New Password</label>
                  <Input id="reset-pw" type="text" value={resetPwValue} onChange={(e) => setResetPwValue(e.target.value)} placeholder="Enter new root password" className="border font-mono" />
                  {vm.status !== "running" && (
                    <p className="text-xs text-destructive font-medium">VM is not running. Start it first.</p>
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
              <DialogContent className="max-w-md border shadow-lg">
                <DialogHeader>
                  <DialogTitle className="text-lg font-semibold">Delete &quot;{vm.hostname}&quot;?</DialogTitle>
                  <DialogDescription className="text-sm text-muted-foreground">
                    The machine and its disks are destroyed and its addresses go back to the pool. This cannot be undone.
                  </DialogDescription>
                </DialogHeader>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setDeleteDialogOpen(false)}>Cancel</Button>
                  <Button variant="destructive" onClick={handleDelete} disabled={deleteVM.isPending}>
                    {deleteVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Trash2 className="w-4 h-4" />}
                    Delete VM
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
      </>

      {vm.status === "creating" && (
        <OperationProgress
          title={`Creating ${vm.hostname}`}
          steps={vmProvisionSteps(vm.status, templates?.find((t) => t.id === vm.os_template_id)?.name)}
          startedAt={vm.created_at}
          className="mb-6"
        />
      )}

      {/* Tabs */}
      <Tabs value={tab} onValueChange={setTab} className="space-y-6">
        <TabsList className="mb-2">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="console">Console</TabsTrigger>
          <TabsTrigger value="network">Network</TabsTrigger>
          <TabsTrigger value="snapshots">Snapshots</TabsTrigger>
          <TabsTrigger value="backups">Backups</TabsTrigger>
          <TabsTrigger value="firewall">Firewall</TabsTrigger>
          <TabsTrigger value="logs">{isAdmin ? "Logs" : "Activity"}</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        {/* Overview: the readings, and beside them what you need to reach and judge the machine. */}
        <TabsContent value="overview">
          <div className="grid grid-cols-1 items-start gap-5 lg:grid-cols-[minmax(0,1fr)_19rem]">
            <VMTelemetry vmId={vmId} running={vm.status === "running"} live={metrics} ramMB={vm.resources.ram} />

            <aside aria-label="Machine details" className="divide-y divide-hair rounded-lg border bg-card text-sm text-card-foreground">
              <section className="p-4">
                <h2 className="mb-1.5 text-xs font-medium text-muted-foreground">Sign in</h2>
                {primaryIP ? (
                  <p className="font-medium">
                    <span className="font-mono break-all">ssh root@{primaryIP}</span>
                    {copyLink(`ssh root@${primaryIP}`, "ssh", "the SSH command")}
                  </p>
                ) : (
                  <p className="text-muted-foreground">
                    No address yet.{" "}
                    {isAdmin ? (
                      <button type="button" onClick={() => setTab("network")} className="font-semibold text-foreground underline underline-offset-2">Assign one on the Network tab</button>
                    ) : "Contact support to have one assigned."}
                  </p>
                )}
                <InitialPassword vmId={vmId} />
                {vm.status === "running" && (
                  vm.vnc_port ? (
                    <div className="mt-3 flex flex-wrap gap-2">
                      <Button asChild size="sm" variant="outline">
                        <Link href={`${basePath}/${vm.id}/console`} target="_blank">Console in a new window</Link>
                      </Button>
                      {/* The SSH console page exists for staff only. */}
                      {isAdmin && (
                        <Button asChild size="sm" variant="outline">
                          <Link href={`${basePath}/${vm.id}/ssh`} target="_blank">SSH console</Link>
                        </Button>
                      )}
                    </div>
                  ) : (
                    // A machine imported from elsewhere may have no display device at all.
                    <p className="mt-3 text-xs text-muted-foreground">
                      {isAdmin
                        ? "This VM has no console device. Repair console on the Console tab adds one; it restarts the VM."
                        : "This VM has no console device. Contact support to add one."}
                    </p>
                  )
                )}
              </section>

              <section className="p-4">
                <div className="mb-1.5 flex items-center justify-between gap-2">
                  <h2 className="text-xs font-medium text-muted-foreground">Size</h2>
                  {isAdmin && <Button variant="outline" size="sm" onClick={openResize}>Resize</Button>}
                </div>
                <dl className="space-y-1">
                  <div className="flex justify-between gap-3"><dt>Processor</dt><dd className="font-semibold">{vm.resources.cpu} vCPU</dd></div>
                  <div className="flex justify-between gap-3"><dt>Memory</dt><dd className="font-semibold">{ramGB} GB</dd></div>
                  <div className="flex justify-between gap-3"><dt>Disk</dt><dd className="font-semibold">{vm.resources.disk} GB</dd></div>
                </dl>
              </section>

              <TransferSummary vmId={vmId} onOpen={() => setTab("network")} />

              <section className="p-4">
                <h2 className="mb-1.5 text-xs font-medium text-muted-foreground">Protection</h2>
                <dl className="space-y-1">
                  <div>
                    <dt>Last backup</dt>
                    <dd className="font-semibold">
                      {backupsLoading ? "Loading" : backupsError ? "Could not load" : lastBackup
                        ? `${formatDate(lastBackup.created_at)}${lastBackup.status === "completed" ? (lastBackup.size > 0 ? `, ${formatBytes(lastBackup.size)}` : "") : `, ${lastBackup.status}`}`
                        : "None yet"}
                    </dd>
                  </div>
                  <div className="flex justify-between gap-3 pt-1">
                    <dt>Snapshots</dt>
                    <dd className="font-semibold">{snapshotsLoading ? "Loading" : snapshotsError ? "Could not load" : `${snapshots?.length ?? 0} of 10`}</dd>
                  </div>
                </dl>
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button size="sm" variant="outline" onClick={() => setTab("backups")}>Backups</Button>
                  <Button size="sm" variant="outline" onClick={() => setTab("snapshots")}>Snapshots</Button>
                </div>
              </section>
            </aside>
          </div>
        </TabsContent>

        {/* Console Tab */}
        <TabsContent value="console" className="space-y-6">
          <div className="bg-card text-card-foreground border rounded-lg">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                Console
              </h2>
              <div className="flex items-center gap-2">
                {isAdmin && (
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
                )}
                {vm.status === "running" && vm.console_enabled !== false && (
                  <Button
                    variant="outline"
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
                  <Link href={`${basePath}/${vm.id}/console`} target="_blank">
                    <Button variant="secondary" size="sm">
                      <ExternalLink className="w-4 h-4 mr-2" />Fullscreen
                    </Button>
                  </Link>
                )}
              </div>
            </div>
            {vm.console_enabled === false ? (
              <div className="bg-muted p-10 flex items-center justify-center min-h-[14rem]">
                <div className="text-center">
                  <p className="text-muted-foreground font-medium mb-2">Console disabled</p>
                  <p className="text-muted-foreground text-sm mb-4">Nobody can open this VM&apos;s screen until it is enabled again.</p>
                  <Button variant="default" size="sm" onClick={() => handleToggleConsole(true)} disabled={setConsole.isPending}>
                    {setConsole.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Monitor className="w-4 h-4" />}
                    Enable console
                  </Button>
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
                <div className="bg-muted p-10 flex items-center justify-center min-h-[14rem]">
                  <div className="text-center">
                    <p className="text-muted-foreground text-sm mb-4">The screen is not connected.</p>
                    <Button variant="default" onClick={() => setConsoleActive(true)} className="gap-2">
                      <Terminal className="w-4 h-4" />
                      Connect to Console
                    </Button>
                  </div>
                </div>
              )
            ) : (
              <div className="bg-muted p-10 flex items-center justify-center min-h-[14rem]">
                <div className="text-center">
                  <p className="text-muted-foreground font-medium text-sm">VM is not running</p>
                  <p className="text-muted-foreground text-xs mt-1">Start the VM to access the console</p>
                </div>
              </div>
            )}
          </div>

          <section className="bg-card text-card-foreground border rounded-lg divide-y">
            <h2 className="px-6 py-4 text-base font-semibold flex items-center gap-2">
              Access
            </h2>
            <ActionRow title="Root password" description={isAdmin ? "Set a new root password inside the VM through the guest agent." : "Set a new root password inside the running VM."}>
              <Button variant="outline" size="sm" onClick={() => setResetPwOpen(true)} disabled={locked}>
                <KeyRound className="w-4 h-4" />Reset password
              </Button>
            </ActionRow>
            <ActionRow title="Console password" description="Generate a new VNC password. It is shown once.">
              <Button variant="outline" size="sm" onClick={handleRegenerateVNC} disabled={regenerateVNC.isPending}>
                {regenerateVNC.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
                Regenerate
              </Button>
            </ActionRow>
            <ActionRow
              title="Console access"
              description={vm.console_enabled === false ? "Disabled. Nobody can open this VM's screen." : "Enabled. Disabling it drops any open session."}
            >
              {vm.console_enabled === false ? (
                <Button variant="outline" size="sm" onClick={() => handleToggleConsole(true)} disabled={setConsole.isPending}>
                  {setConsole.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Monitor className="w-4 h-4" />}
                  Enable console
                </Button>
              ) : (
                <Button variant="outline" size="sm" onClick={() => handleToggleConsole(false)} disabled={setConsole.isPending}>
                  {setConsole.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <MonitorOff className="w-4 h-4" />}
                  Disable console
                </Button>
              )}
            </ActionRow>
          </section>

          <section className="bg-card text-card-foreground border rounded-lg divide-y">
            <h2 className="px-6 py-4 text-base font-semibold flex items-center gap-2">
              Boot media
            </h2>
            <ActionRow
              title="ISO image"
              description={
                <>
                  {/* attached_iso: null = the panel does not know (VMs from before
                      it was tracked), "" = drive empty, otherwise the image. */}
                  {vm.attached_iso ? (
                    <>Attached: <span className="font-medium text-foreground break-all">{isoLabel(vm.attached_iso)}</span>. The VM boots from it on the next start.</>
                  ) : vm.attached_iso === "" ? (
                    "No ISO attached. Mount an install or recovery image to boot from it on the next start."
                  ) : (
                    "Attach an install or recovery ISO. The VM boots from it on the next start."
                  )}
                  {vm.status === "running" && " Stop the VM to mount an ISO."}
                </>
              }
            >
              <Button variant="outline" size="sm" onClick={() => setIsoDialogOpen(true)} disabled={locked || vm.status === "running"}>
                <Disc className="w-4 h-4" />Mount ISO
              </Button>
              <Button variant="outline" size="sm" onClick={handleDetachISO} disabled={locked || detachISO.isPending || vm.attached_iso === ""}>
                {detachISO.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <CircleSlash className="w-4 h-4" />}
                Unmount
              </Button>
            </ActionRow>
            <ActionRow
              title="Rescue mode"
              description={vm.rescue_mode
                ? "Rescue ISO attached. Start the VM to boot into it, or exit to boot from disk again."
                : vm.status === "running"
                  ? "Boot a rescue system to repair a VM that will not start. Stop the VM to enable this."
                  : "Boot a rescue system to repair a VM that will not start."}
            >
              {vm.rescue_mode ? (
                <Button variant="outline" size="sm" onClick={handleUnrescue} disabled={locked || unrescueVM.isPending}>
                  {unrescueVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <LifeBuoy className="w-4 h-4" />}
                  Exit rescue
                </Button>
              ) : (
                <Button variant="outline" size="sm" onClick={handleRescue} disabled={locked || rescueVM.isPending || vm.status === "running"}>
                  {rescueVM.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <LifeBuoy className="w-4 h-4" />}
                  Enter rescue
                </Button>
              )}
            </ActionRow>
          </section>
        </TabsContent>

        {/* Network Tab */}
        <TabsContent value="network">
          <div className="space-y-6">
            <div className="bg-card text-card-foreground border rounded-lg p-6">
              <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
                IPv4 addresses
              </h2>

              {/* Interfaces (per-IP detail) */}
              <div className="flex items-center justify-between mb-3">
                <h3 className="sr-only">Addresses on this VM</h3>
                {isAdmin && (
                  <Button type="button" size="sm" variant="outline" onClick={() => setAssignIPOpen(true)}>
                    <Plus className="w-4 h-4 mr-1" />
                    Assign IP address
                  </Button>
                )}
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
                        {isAdmin && <th className="text-left p-3">VLAN</th>}
                        {isAdmin && <th className="text-left p-3">Anti-spoofing</th>}
                        <th className="text-left p-3">{isAdmin ? "Bandwidth" : "Speed limit"}</th>
                        <th className="text-left p-3">rDNS</th>
                        {isAdmin && <th className="text-right p-3"></th>}
                      </tr>
                    </thead>
                    <tbody>
                      {vmNetworks.map((iface, idx) => (
                        <tr key={iface.id} className={`border-t ${idx % 2 ? "bg-muted/50" : "bg-card"}`}>
                          <td className="p-3 font-mono font-medium">
                            {isAdmin && iface.pool_id ? (
                              <Link href={`/ip-pools/${iface.pool_id}`} className="underline hover:text-primary">{iface.ip_address}</Link>
                            ) : iface.ip_address}
                          </td>
                          <td className="p-3 font-mono">{iface.gateway || "—"}</td>
                          <td className="p-3 font-mono">{iface.netmask || "—"}</td>
                          {isAdmin && <td className="p-3 font-mono">{iface.vlan_id ?? "—"}</td>}
                          {isAdmin && <td className="p-3"><AntiSpoofCell vmId={vmId} iface={iface} /></td>}
                          <td className="p-3">
                            {isAdmin ? <BandwidthCell vmId={vmId} iface={iface} /> : <span className="font-mono">{formatMbps(iface.bandwidth_limit)}</span>}
                          </td>
                          <td className="p-3"><RDNSCell vmId={vmId} ipAddress={iface.ip_address} rdns={iface.rdns || ""} verified={!isAdmin} /></td>
                          {isAdmin && (
                          <td className="p-3 text-right">
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                              title="Release this address back to its pool"
                              aria-label={`Release ${iface.ip_address}`}
                              disabled={releaseIP.isPending && releaseIP.variables === iface.id}
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
                                toast.success(`${iface.ip_address} released`)
                              }}
                            >
                              {releaseIP.isPending && releaseIP.variables === iface.id ? <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" /> : <Trash2 className="w-4 h-4" aria-hidden="true" />}
                            </Button>
                          </td>
                          )}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {!isAdmin && !!vmNetworks?.length && (
                <p className="mt-3 text-xs text-muted-foreground">
                  Speed is set by your plan. Contact support to change it. To set reverse DNS, first point a DNS record for
                  the hostname at the address; it is checked before saving.
                </p>
              )}
            </div>

            {/* Routed IPv6. Customers see it only when they have a prefix. */}
            {(isAdmin || vm.ipv6_address) && (
            <div className="bg-card text-card-foreground border rounded-lg p-6">
              <div className="flex items-center justify-between mb-4">
                <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                  IPv6
                </h2>
                {isAdmin && (vm.ipv6_address ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="text-destructive"
                    disabled={releaseIPv6.isPending}
                    onClick={async () => {
                      const ok = await confirm({
                        title: `Release ${vm.ipv6_prefix || vm.ipv6_address}?`,
                        description:
                          "It returns to its pool and the node stops carrying it. The guest's IPv6 address is removed from its network configuration; a guest without a responsive guest agent is rebooted to apply that.",
                        confirmLabel: "Release IPv6",
                        destructive: true,
                        action: () => releaseIPv6.mutateAsync(),
                      })
                      if (!ok) return
                      toast.success("IPv6 released")
                    }}
                  >
                    <Trash2 className="w-4 h-4 mr-1" />Release
                  </Button>
                ) : (
                  <Button type="button" size="sm" variant="outline" onClick={() => setAssignIPv6Open(true)}>
                    <Plus className="w-4 h-4 mr-1" />Assign IPv6
                  </Button>
                ))}
              </div>
              {vm.ipv6_address ? (
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                  <div className="p-4 border">
                    <span className="text-xs font-medium text-muted-foreground block">{vm.ipv6_prefix ? "Routed prefix" : "Kind"}</span>
                    <span className={`text-sm font-medium break-all ${vm.ipv6_prefix ? "font-mono" : ""}`}>{vm.ipv6_prefix || "One address on a shared subnet"}</span>
                  </div>
                  <div className="p-4 border">
                    <span className="text-xs font-medium text-muted-foreground block">Address</span>
                    <span className="text-sm font-mono font-medium break-all">{vm.ipv6_address}</span>
                  </div>
                  <div className="p-4 border">
                    <span className="text-xs font-medium text-muted-foreground block">Gateway</span>
                    <span className="text-sm font-mono font-medium">{vm.ipv6_gateway}</span>
                  </div>
                </div>
              ) : (
                <div className="border border-dashed border-gray-300 p-6 text-center text-sm font-medium text-muted-foreground">
                  No IPv6 assigned
                </div>
              )}
            </div>
            )}

            {/* Data disks: admin only until billing for extra disks is decided. */}
            {isAdmin && <DisksCard vmId={vmId} />}

            <PortForwardsCard vmId={vmId} />

            {/* Bandwidth Usage */}
            <BandwidthUsageCard vmId={vmId} canEditQuota={isAdmin} />
          </div>
        </TabsContent>

        {/* Snapshots Tab */}
        <TabsContent value="snapshots">
          <div className="bg-card text-card-foreground border rounded-lg p-6">
            <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                Snapshots
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
                  <Button variant="outline" size="sm"><Plus className="w-4 h-4 mr-2" />Create Snapshot</Button>
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
              <div>
                {snapshots.map((snap: Snapshot) => (
                  <div key={snap.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-hair py-3 first:border-t-0 first:pt-0">
                    <div className="min-w-0">
                      <div>
                        <span className="font-semibold">{snap.name}</span>
                        <span className="text-xs text-muted-foreground ml-2">{formatDate(snap.created_at)}</span>
                      </div>
                    </div>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <StatusBadge variant="lamp" className="text-xs" status={snap.status} />
                      <Button variant="ghost" size="sm" onClick={() => handleRestoreSnapshot(snap.id)} disabled={restoreSnapshot.isPending && restoreSnapshot.variables === snap.id}>
                        {restoreSnapshot.isPending && restoreSnapshot.variables === snap.id && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                        Restore
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => handleDeleteSnapshot(snap.id)} disabled={deleteSnapshot.isPending && deleteSnapshot.variables === snap.id}>
                        {deleteSnapshot.isPending && deleteSnapshot.variables === snap.id && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
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
          <BackupScheduleCard vmId={vmId} />
          <div className="bg-card text-card-foreground border rounded-lg p-6">
            <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                Backups
              </h2>
              <Button variant="outline" size="sm" onClick={handleCreateBackup} disabled={createBackup.isPending}>
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
              <div>
                {backups.map((backup: Backup) => (
                  <div key={backup.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-hair py-3 first:border-t-0 first:pt-0">
                    <div className="min-w-0">
                      <div>
                        <span className="font-semibold">{backup.backup_type === "scheduled" ? "Scheduled backup" : "Manual backup"}</span>
                        <span className="text-xs text-muted-foreground ml-2">{formatDate(backup.created_at)}</span>
                      </div>
                      {backup.status === "failed" && backup.error_message && (
                        <p className="mt-0.5 text-xs text-destructive break-words">{backup.error_message}</p>
                      )}
                    </div>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="text-xs font-medium">{formatBytes(backup.size)}</span>
                      <StatusBadge variant="lamp" className="text-xs" status={backup.status} />
                      {backup.status === "completed" && (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={locked}
                          onClick={() => confirm({
                            title: "Restore this backup?",
                            description: `The VM's disk is replaced with the backup from ${formatDate(backup.created_at)}. Everything written since then is lost. The VM must be stopped first.`,
                            confirmLabel: "Restore backup",
                            destructive: true,
                            action: async () => {
                              await restoreBackup.mutateAsync(backup.id)
                              toast.success("Restore started")
                            },
                          })}
                        >
                          Restore
                        </Button>
                      )}
                      <Button variant="ghost" size="sm" onClick={async () => {
                        const ok = await confirm({
                          title: "Delete this backup?",
                          description: `The backup from ${formatDate(backup.created_at)} is removed for good.`,
                          confirmLabel: "Delete backup",
                          destructive: true,
                        })
                        if (ok) handleDeleteBackup(backup.id)
                      }} disabled={deleteBackupMutation.isPending && deleteBackupMutation.variables === backup.id}>
                        {deleteBackupMutation.isPending && deleteBackupMutation.variables === backup.id && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
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
          <div className="bg-card text-card-foreground border rounded-lg p-6">
            <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
              <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
                Firewall Rules
              </h2>
              <Dialog open={fwOpen} onOpenChange={setFwOpen}>
                <DialogTrigger asChild>
                  <Button size="sm" variant="outline"><Plus className="w-4 h-4 mr-1" />Add Rule</Button>
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
                          <StatusBadge variant="lamp" className="text-xs" status={rule.action} />
                        </td>
                        <td className="py-3 px-4 font-mono text-sm">{rule.priority}</td>
                        <td className="py-3 px-4">
                          <Button variant="ghost" size="sm" onClick={() => handleDeleteFirewallRule(rule.id)} disabled={deleteFirewallRule.isPending && deleteFirewallRule.variables === rule.id}>
                            {deleteFirewallRule.isPending && deleteFirewallRule.variables === rule.id && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
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
          <div className="bg-card text-card-foreground border rounded-lg p-6">
            <h2 className="text-lg font-semibold text-foreground mb-6 flex items-center gap-2">
              {isAdmin ? "Activity Logs" : "Activity"}
            </h2>
            {activityLoading ? (
              <SectionSkeleton />
            ) : activityError ? (
              <SectionError message={(activityError as Error).message} onRetry={() => refetchActivity()} />
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

        {/* Settings Tab: where the VM lives, who owns it, and the two actions
            that destroy data, kept apart from everything routine. */}
        <TabsContent value="settings" className="space-y-6">
          {isAdmin && (
          <section className="bg-card text-card-foreground border rounded-lg divide-y">
            <h2 className="px-6 py-4 text-base font-semibold flex items-center gap-2">
              Ownership and placement
            </h2>
            <ActionRow title="Owner" description="Hand this VM to a different user account.">
              <Button variant="outline" size="sm" onClick={() => setReassignOpen(true)}>
                <User className="w-4 h-4" />Reassign
              </Button>
            </ActionRow>
            <ActionRow title="Node" description={<>Runs on <span className="font-medium text-foreground">{allNodes?.find((n) => n.id === vm.node_id)?.name || vm.node_name || "an unnamed node"}</span>. Move it to another node while it keeps running.</>}>
              <Button variant="outline" size="sm" onClick={() => setMigrateOpen(true)}>
                <ArrowRightLeft className="w-4 h-4" />Migrate
              </Button>
            </ActionRow>
            <ActionRow title="Clone" description="Create a new VM that starts as a copy of this one.">
              <Button variant="outline" size="sm" onClick={() => setCloneOpen(true)}>
                <Copy className="w-4 h-4" />Clone
              </Button>
            </ActionRow>
          </section>
          )}

          <section className="bg-card text-card-foreground border border-destructive/40 rounded-lg divide-y divide-destructive/20">
            <h2 className="px-6 py-4 text-base font-semibold text-destructive flex items-center gap-2">
              Danger zone
            </h2>
            <ActionRow title={rebuildWord} description="Reinstall the operating system. Everything on the disk is erased; the VM keeps its addresses.">
              <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRebuildDialogOpen(true)} disabled={locked}>
                <RefreshCw className="w-4 h-4" />{rebuildWord}
              </Button>
            </ActionRow>
            <ActionRow title="Delete" description="Remove this VM and its disks for good and release its addresses.">
              <Button variant="destructive" size="sm" onClick={() => setDeleteDialogOpen(true)}>
                <Trash2 className="w-4 h-4" />Delete
              </Button>
            </ActionRow>
          </section>
        </TabsContent>
      </Tabs>

      {isAdmin && (<>
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
              {(ipPools ?? [])
                .filter((p) => {
                  const bound = p.node_ids && p.node_ids.length > 0 ? p.node_ids : p.node_id ? [p.node_id] : []
                  return bound.length === 0 || bound.includes(vm.node_id)
                })
                .map((p) => (
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
          <div className="rounded-md border border-warning/50 bg-warning/10 p-3 text-xs text-foreground">
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
                  toast.success("IP address assigned")
                } catch (err) {
                  toast.error((err as Error).message)
                }
              }}
            >
              {assignIP.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Assign"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delegate a routed /64 to the VM. */}
      <Dialog open={assignIPv6Open} onOpenChange={setAssignIPv6Open}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Assign IPv6 to {vm.hostname}</DialogTitle>
            <DialogDescription>
              Depending on the pool, the VM gets a routed prefix of its own (the guest takes its ::1, with fe80::1 as
              gateway) or one address from a shared subnet (with that subnet&apos;s router as gateway). The firewall rules
              on its IPv4 address apply to its IPv6 as well.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-2">
            <label htmlFor="assign-v6-pool" className="text-sm font-medium">Pool</label>
            <select
              id="assign-v6-pool"
              className="w-full border rounded-md px-3 py-2 text-sm bg-background"
              value={assignIPv6PoolID}
              onChange={(e) => setAssignIPv6PoolID(e.target.value)}
            >
              <option value="">Choose an IPv6 pool</option>
              {(ipPools ?? [])
                .filter((p) => p.family === "ipv6")
                .filter((p) => {
                  const bound = p.node_ids && p.node_ids.length > 0 ? p.node_ids : p.node_id ? [p.node_id] : []
                  return bound.length === 0 || bound.includes(vm.node_id)
                })
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} ({p.cidr}: {p.delegated_prefix_len ? `a routed /${p.delegated_prefix_len}` : "one address"})
                  </option>
                ))}
            </select>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setAssignIPv6Open(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              disabled={assignIPv6.isPending || !assignIPv6PoolID}
              onClick={async () => {
                try {
                  await assignIPv6.mutateAsync({ pool_id: assignIPv6PoolID })
                  setAssignIPv6Open(false)
                  setAssignIPv6PoolID("")
                  toast.success("IPv6 prefix assigned")
                } catch (err) {
                  toast.error((err as Error).message)
                }
              }}
            >
              {assignIPv6.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Assign"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      </>)}

      {/* Delete progress. Leaving for the list only once it has finished means
          the outcome is always seen — including a failure, which previously
          scrolled past as a toast on a page the operator had already left. */}
      <DeleteProgressDialog
        vm={deletingVM}
        onClose={() => {
          setDeletingVM(null)
          router.push(basePath)
        }}
      />

      {/* One-time credential. A toast would vanish before it could be copied.
          For customers only the explicit button closes it, so a stray click or
          Escape cannot lose the password. */}
      <Dialog open={!!secret} onOpenChange={(open) => { if (!open && isAdmin) setSecret(null) }}>
        <DialogContent
          className={isAdmin ? "max-w-md" : "max-w-md [&>button:last-child]:hidden"}
          onInteractOutside={(e) => { if (!isAdmin) e.preventDefault() }}
          onEscapeKeyDown={(e) => { if (!isAdmin) e.preventDefault() }}
        >
          <DialogHeader>
            <DialogTitle>{secret?.title}</DialogTitle>
            <DialogDescription>This is shown once. Copy it now and store it somewhere safe.</DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2">
            <code className="flex-1 break-all rounded-md border bg-muted px-3 py-2 font-mono text-sm">{secret?.value}</code>
            <Button variant="secondary" size="sm" onClick={() => secret && copy(secret.value, "secret")}>
              {copied("secret") ? <Check className="w-4 h-4" aria-hidden="true" /> : <Copy className="w-4 h-4" aria-hidden="true" />}
              {copied("secret") ? "Copied" : "Copy"}
            </Button>
          </div>
          <DialogFooter>
            <Button onClick={() => setSecret(null)}>{isAdmin ? "Done" : "I have saved it"}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
