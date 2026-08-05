"use client"

import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { useState } from "react"
import { ArrowLeft, Play, Square, RotateCw, Monitor, Cpu, MemoryStick, HardDrive, Terminal, Trash2, Gauge, RefreshCw, Layers } from "lucide-react"
import { useVM, useVMAction, useDeleteVM, useVMStatusStream, useRebuildVM, useVMMetrics, useVMMetricsHistory } from "@/lib/hooks/use-vms"
import { Sparkline } from "@/components/ui/sparkline"
import type { VMMetricSample } from "@/types"
import { CountryFlag } from "@/components/country-flag"
import { useCreateImage } from "@/lib/hooks/use-images"
import { useVMNetworks } from "@/lib/hooks/use-networks"
import { useTemplates } from "@/lib/hooks/use-templates"

function speedLabel(mbps: number): string {
  if (mbps <= 0) return "Unlimited"
  if (mbps % 1000 === 0) return `${mbps / 1000} Gbps`
  return `${mbps} Mbps`
}

// NetworkSpeedCard shows a client the network speed of their VM's interfaces.
// It is READ-ONLY: speed is determined by the VM's plan and can only be changed
// by an administrator (the bandwidth endpoint is admin-only), so clients see
// their current speed but cannot set it here.
function NetworkSpeedCard({ vmId }: { vmId: string }) {
  const { data: networks } = useVMNetworks(vmId)

  if (!networks?.length) return null

  return (
    <div className="rounded-lg border bg-card text-card-foreground shadow-sm">
      <div className="px-5 py-4 border-b flex items-center gap-2">
        <Gauge className="w-5 h-5 text-muted-foreground" />
        <h2 className="text-lg font-semibold">Network Speed</h2>
      </div>
      <div className="p-5 space-y-3">
        {networks.map((iface) => (
          <div key={iface.id} className="flex items-center justify-between">
            <span className="font-mono text-sm">{iface.ip_address}</span>
            <span className="text-xs font-medium rounded-md border bg-muted text-muted-foreground px-2 py-0.5">
              {speedLabel(iface.bandwidth_limit)}
            </span>
          </div>
        ))}
        <p className="text-xs text-muted-foreground">
          Network speed is set by your plan. Contact your administrator to change it.
        </p>
      </div>
    </div>
  )
}

function StatusBadge({ status }: { status?: string }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-400 dark:border-emerald-900",
    stopped: "bg-muted text-muted-foreground border-border",
    suspended: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-400 dark:border-amber-900",
    creating: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-400 dark:border-blue-900",
    deleting: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-400 dark:border-amber-900",
    error: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-400 dark:border-red-900",
  }
  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-xs font-medium capitalize rounded-md border ${colors[status || ""] || "bg-muted text-muted-foreground border-border"}`}>
      {status || "unknown"}
    </span>
  )
}

function Spec({ icon: Icon, label, value }: { icon: React.ElementType; label: string; value: string }) {
  return (
    <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
      <div className="flex items-center gap-2 text-muted-foreground">
        <Icon className="w-4 h-4" />
        <span className="text-xs font-medium">{label}</span>
      </div>
      <p className="text-xl font-semibold mt-1">{value}</p>
    </div>
  )
}

// One usage figure with its trend. Kept tiny and local: it exists only to stop
// the four blocks below repeating the same markup four times.
function UsageStat({ label, value, series }: { label: string; value: string; series: number[] }) {
  return (
    <div>
      <div className="flex items-baseline justify-between">
        <span className="text-sm text-muted-foreground">{label}</span>
        <span className="text-sm font-semibold">{value}</span>
      </div>
      {series.length > 1 ? (
        <div className="mt-2">
          <Sparkline data={series} />
        </div>
      ) : (
        <p className="mt-2 text-xs text-muted-foreground">not enough history yet</p>
      )}
    </div>
  )
}

function formatRate(bytesPerSec: number): string {
  if (!bytesPerSec || bytesPerSec < 1024) return `${Math.round(bytesPerSec || 0)} B/s`
  if (bytesPerSec < 1024 * 1024) return `${(bytesPerSec / 1024).toFixed(1)} KB/s`
  return `${(bytesPerSec / (1024 * 1024)).toFixed(1)} MB/s`
}

export default function ClientVMDetailPage() {
  const params = useParams()
  const router = useRouter()
  const vmId = params.id as string
  useVMStatusStream()
  const { data: vm, isLoading, isError } = useVM(vmId)
  const action = useVMAction(vmId)
  const del = useDeleteVM()
  const rebuild = useRebuildVM(vmId)
  const { data: templates } = useTemplates()
  const { data: metrics } = useVMMetrics(vmId)
  const { data: history } = useVMMetricsHistory(vmId, 60)

  // The template name usually already carries the version ("Debian 12"), so it
  // is only appended when it does not — otherwise the page reads "Debian 12 12".
  // Fall back to the newest stored sample when the live reading is unavailable,
  // so a momentary node timeout shows a slightly stale number rather than a dash.
  const last = (h?: VMMetricSample[]) => (h && h.length ? h[h.length - 1] : undefined)
  const latest = (key: "cpu_usage" | "memory_usage", unit: string) => {
    const s = last(history)
    return s ? `${Math.round(s[key])}${unit}` : "—"
  }

  const template = (templates ?? []).find((t) => t.id === vm?.os_template_id)
  const osLabel = template
    ? template.version && !template.name.toLowerCase().endsWith(template.version.toLowerCase())
      ? `${template.name} ${template.version}`
      : template.name
    : "—"
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [reinstallOpen, setReinstallOpen] = useState(false)
  const [templateId, setTemplateId] = useState("")
  const [rootPassword, setRootPassword] = useState("")
  const [regenPassword, setRegenPassword] = useState(false)
  const [reinstallError, setReinstallError] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const createImage = useCreateImage()
  const [imageOpen, setImageOpen] = useState(false)
  const [imageName, setImageName] = useState("")
  const [imageError, setImageError] = useState("")
  const [imageStarted, setImageStarted] = useState(false)

  if (isLoading) {
    return <div className="p-8 text-center text-muted-foreground">Loading…</div>
  }
  if (isError || !vm) {
    return (
      <div className="p-10 text-center">
        <p className="font-medium text-muted-foreground">VM not found.</p>
        <Link href="/client/vms" className="inline-block mt-4 text-primary hover:underline font-medium">Back to My VMs</Link>
      </div>
    )
  }

  const handleDelete = () => {
    del.mutate(vmId, { onSuccess: () => router.push("/client/vms") })
  }

  const handleReinstall = async () => {
    if (!templateId) return
    setReinstallError("")
    try {
      const res = await rebuild.mutateAsync({
        template_id: templateId,
        preserve_ip: true,
        password: rootPassword || undefined,
        regenerate_password: regenPassword,
      })
      setNewPassword(res.root_password || "")
      if (!res.root_password) {
        setReinstallOpen(false)
        setTemplateId("")
        setRootPassword("")
        setRegenPassword(false)
      }
    } catch (err) {
      setReinstallError((err as Error).message || "Reinstall failed")
    }
  }

  const handleCreateImage = async () => {
    if (!imageName.trim()) return
    setImageError("")
    try {
      await createImage.mutateAsync({ vm_id: vmId, name: imageName.trim() })
      setImageStarted(true)
    } catch (err) {
      setImageError((err as Error).message || "Failed to create image")
    }
  }

  return (
    // Widened from max-w-4xl: that suited a page of text, but it now carries
    // usage charts, which need room to be readable. max-w-7xl matches the admin
    // VM page rather than going full-bleed — prose running the width of a 2000px
    // monitor is worse than the empty space it removes.
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Link href="/client/vms" className="p-2 rounded-md border bg-background hover:bg-muted transition-colors">
          <ArrowLeft className="w-5 h-5" />
        </Link>
        <Monitor className="w-6 h-6 text-muted-foreground" />
        <h1 className="text-2xl font-semibold tracking-tight truncate">{vm.hostname}</h1>
        <StatusBadge status={vm.status} />
      </div>

      {/* Actions */}
      <div className="flex flex-wrap items-center gap-2">
        {vm.status === "stopped" && (
          <button
            onClick={() => action.mutate("start")}
            disabled={action.isPending}
            className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-emerald-600 text-white text-sm font-medium hover:bg-emerald-700 transition-colors disabled:opacity-50"
          >
            <Play className="w-4 h-4" /> Start
          </button>
        )}
        {vm.status === "running" && (
          <>
            <button
              onClick={() => action.mutate("restart")}
              disabled={action.isPending}
              className="inline-flex items-center gap-2 h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors disabled:opacity-50"
            >
              <RotateCw className="w-4 h-4" /> Reboot
            </button>
            <button
              onClick={() => action.mutate("stop")}
              disabled={action.isPending}
              className="inline-flex items-center gap-2 h-10 px-4 rounded-md border border-input bg-background text-destructive text-sm font-medium hover:bg-destructive/10 transition-colors disabled:opacity-50"
            >
              <Square className="w-4 h-4" /> Stop
            </button>
          </>
        )}
        <Link
          href={`/client/vms/${vm.id}/console`}
          className="inline-flex items-center gap-2 h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors"
        >
          <Terminal className="w-4 h-4" /> Console
        </Link>
        <button
          onClick={() => { setReinstallOpen(true); setReinstallError(""); setNewPassword("") }}
          className="inline-flex items-center gap-2 h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors"
        >
          <RefreshCw className="w-4 h-4" /> Reinstall
        </button>
        <button
          onClick={() => { setImageOpen(true); setImageError(""); setImageStarted(false); setImageName(`${vm.hostname}-image`) }}
          className="inline-flex items-center gap-2 h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors"
        >
          <Layers className="w-4 h-4" /> Save as Image
        </button>
      </div>

      {/* Save as Image dialog */}
      {imageOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={() => setImageOpen(false)}>
          <div className="w-full max-w-md rounded-lg border bg-card text-card-foreground shadow-lg" onClick={(e) => e.stopPropagation()}>
            <div className="px-5 py-4 border-b">
              <h2 className="text-lg font-semibold">Save as Image</h2>
              <p className="text-sm text-muted-foreground mt-1">
                Captures this VM&apos;s disk into a reusable image. Images survive VM deletion and can be used to deploy new VMs.
              </p>
            </div>
            <div className="p-5 space-y-4">
              {imageStarted ? (
                <div className="rounded-md border border-emerald-200 bg-emerald-50 dark:bg-emerald-950 dark:border-emerald-900 p-3 text-xs">
                  <p className="font-medium text-emerald-800 dark:text-emerald-300">
                    Image capture started. Track its progress on the{" "}
                    <Link href="/client/images" className="underline">Images</Link> page.
                  </p>
                </div>
              ) : (
                <div>
                  <span className="text-xs font-medium text-muted-foreground mb-2 block">Image Name</span>
                  <input
                    type="text"
                    value={imageName}
                    onChange={(e) => setImageName(e.target.value)}
                    placeholder="e.g., web-base-image"
                    className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                  />
                </div>
              )}
              {imageError && (
                <div className="rounded-md border border-red-200 bg-red-50 dark:bg-red-950 dark:border-red-900 p-3 text-xs text-destructive">{imageError}</div>
              )}
            </div>
            <div className="px-5 py-4 border-t flex items-center justify-end gap-2">
              <button onClick={() => setImageOpen(false)} className="h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors">
                {imageStarted ? "Close" : "Cancel"}
              </button>
              {!imageStarted && (
                <button
                  onClick={handleCreateImage}
                  disabled={!imageName.trim() || createImage.isPending}
                  className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium hover:bg-primary/90 transition-colors disabled:opacity-50"
                >
                  <Layers className={`w-4 h-4 ${createImage.isPending ? "animate-pulse" : ""}`} /> {createImage.isPending ? "Capturing…" : "Capture"}
                </button>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Reinstall / Rebuild OS dialog */}
      {reinstallOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={() => setReinstallOpen(false)}>
          <div className="w-full max-w-md rounded-lg border bg-card text-card-foreground shadow-lg" onClick={(e) => e.stopPropagation()}>
            <div className="px-5 py-4 border-b">
              <h2 className="text-lg font-semibold">Reinstall OS</h2>
              <p className="text-sm text-muted-foreground mt-1">
                Wipes this VM and reinstalls a fresh OS from a template. All data will be lost. The VM keeps its IP address.
              </p>
            </div>
            <div className="p-5 space-y-4 max-h-[60vh] overflow-y-auto">
              {vm.status !== "stopped" && (
                <div className="rounded-md border border-amber-200 bg-amber-50 dark:bg-amber-950 dark:border-amber-900 p-3 text-xs text-amber-800 dark:text-amber-300">
                  Stop the VM first — it must be powered off to reinstall.
                </div>
              )}
              <div>
                <span className="text-xs font-medium text-muted-foreground mb-2 block">Operating System</span>
                <div className="grid grid-cols-2 gap-2">
                  {templates?.map((t) => (
                    <button
                      key={t.id}
                      type="button"
                      onClick={() => setTemplateId(t.id)}
                      className={`p-3 rounded-md border text-left transition-colors ${templateId === t.id ? "border-primary ring-1 ring-primary bg-primary/5" : "bg-background hover:bg-muted/50"}`}
                    >
                      <span className="text-xs font-medium block">{t.name}</span>
                      <span className="text-[10px] text-muted-foreground">v{t.version}</span>
                    </button>
                  ))}
                  {!templates?.length && <p className="text-xs text-muted-foreground col-span-2">No OS templates available.</p>}
                </div>
              </div>
              <div>
                <span className="text-xs font-medium text-muted-foreground mb-2 block">Root Password</span>
                <label className="flex items-center gap-2 mb-2 cursor-pointer">
                  <input type="checkbox" checked={regenPassword} onChange={(e) => { setRegenPassword(e.target.checked); if (e.target.checked) setRootPassword("") }} className="w-4 h-4" />
                  <span className="text-xs font-medium">Auto-generate a new password</span>
                </label>
                <input
                  type="text"
                  value={rootPassword}
                  onChange={(e) => setRootPassword(e.target.value)}
                  disabled={regenPassword}
                  placeholder={regenPassword ? "Will be generated & shown once" : "Leave blank to keep template default"}
                  className="w-full h-10 px-3 rounded-md border border-input bg-background font-mono text-sm disabled:opacity-50"
                />
              </div>
              {newPassword && (
                <div className="rounded-md border border-emerald-200 bg-emerald-50 dark:bg-emerald-950 dark:border-emerald-900 p-3 text-xs">
                  <p className="font-medium text-emerald-800 dark:text-emerald-300 mb-1">Reinstall started. New root password (shown once):</p>
                  <code className="font-mono text-sm break-all">{newPassword}</code>
                </div>
              )}
              {reinstallError && (
                <div className="rounded-md border border-red-200 bg-red-50 dark:bg-red-950 dark:border-red-900 p-3 text-xs text-destructive">{reinstallError}</div>
              )}
            </div>
            <div className="px-5 py-4 border-t flex items-center justify-end gap-2">
              <button onClick={() => setReinstallOpen(false)} className="h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors">
                {newPassword ? "Close" : "Cancel"}
              </button>
              {!newPassword && (
                <button
                  onClick={handleReinstall}
                  disabled={!templateId || rebuild.isPending}
                  className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-destructive text-destructive-foreground text-sm font-medium hover:bg-destructive/90 transition-colors disabled:opacity-50"
                >
                  <RefreshCw className={`w-4 h-4 ${rebuild.isPending ? "animate-spin" : ""}`} /> {rebuild.isPending ? "Reinstalling…" : "Reinstall"}
                </button>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Specs */}
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <Spec icon={Cpu} label="vCPU" value={`${vm.resources.cpu}`} />
        <Spec icon={MemoryStick} label="Memory" value={`${vm.resources.ram} MB`} />
        <Spec icon={HardDrive} label="Disk" value={`${vm.resources.disk} GB`} />
      </div>

      {/* Live usage. The samples come from the same collector the admin view
          uses; a customer asking "why is it slow" should not have to open a
          ticket to see whether the machine is out of CPU or memory. */}
      {vm.status === "running" && (
        <section className="rounded-lg border bg-card">
          <div className="p-5 border-b flex items-center gap-2">
            <Gauge className="w-5 h-5" />
            <h2 className="text-lg font-semibold">Usage</h2>
            <span className="text-xs text-muted-foreground ml-auto">last 60 minutes</span>
          </div>
          {/* Charts come from stored history, the current figure from the live
              call. History is deliberately the primary source: it is persisted
              and needs no round-trip to the node, whereas the live reading times
              out under load — driving the whole panel off it meant one slow node
              blanked the charts entirely. */}
          {history?.length || metrics ? (
            <div className="grid gap-6 p-5 sm:grid-cols-2">
              <UsageStat
                label="CPU"
                value={metrics ? `${Math.round(metrics.cpu_percent)}%` : latest("cpu_usage", "%")}
                series={(history ?? []).map((h) => h.cpu_usage)}
              />
              <UsageStat
                label="Memory"
                value={
                  metrics
                    ? `${Math.round(metrics.memory_used / (1024 * 1024))} / ${Math.round(
                        metrics.memory_total / (1024 * 1024),
                      )} MB`
                    : latest("memory_usage", "%")
                }
                series={(history ?? []).map((h) => h.memory_usage)}
              />
              <UsageStat
                label="Network in"
                value={formatRate(
                  metrics?.network_rx_bytes_per_sec ?? last(history)?.network_rx_bytes_per_sec ?? 0,
                )}
                series={(history ?? []).map((h) => h.network_rx_bytes_per_sec)}
              />
              <UsageStat
                label="Network out"
                value={formatRate(
                  metrics?.network_tx_bytes_per_sec ?? last(history)?.network_tx_bytes_per_sec ?? 0,
                )}
                series={(history ?? []).map((h) => h.network_tx_bytes_per_sec)}
              />
            </div>
          ) : (
            <p className="p-5 text-sm text-muted-foreground">
              Collecting — the first samples appear within a minute of the VM starting.
            </p>
          )}
        </section>
      )}

      {/* What this machine actually is: the OS it runs and where it runs. Both
          were missing, so the page could not answer the two questions a customer
          opens it to ask. */}
      <div className="grid gap-4 sm:grid-cols-2">
        <Spec icon={Layers} label="Operating system" value={osLabel} />
        <div className="rounded-lg border bg-card p-4">
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Gauge className="w-4 h-4" />
            Location
          </div>
          <div className="mt-1 flex items-center gap-2 text-lg font-semibold">
            <CountryFlag country={vm.region_country} />
            {vm.region_name ?? "—"}
          </div>
        </div>
      </div>

      {/* Network speed self-service upgrade */}
      <NetworkSpeedCard vmId={vmId} />

      {/* Danger zone */}
      <div className="rounded-lg border border-destructive/50 bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b border-destructive/50">
          <h2 className="text-lg font-semibold text-destructive">Danger Zone</h2>
        </div>
        <div className="p-5 flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div>
            <p className="font-medium">Destroy this VM</p>
            <p className="text-sm text-muted-foreground">This permanently deletes the VM and its disks. This cannot be undone.</p>
          </div>
          {confirmDelete ? (
            <div className="flex items-center gap-2">
              <button
                onClick={handleDelete}
                disabled={del.isPending}
                className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-destructive text-destructive-foreground text-sm font-medium hover:bg-destructive/90 transition-colors disabled:opacity-50"
              >
                <Trash2 className="w-4 h-4" /> {del.isPending ? "Deleting…" : "Confirm"}
              </button>
              <button
                onClick={() => setConfirmDelete(false)}
                className="h-10 px-4 rounded-md border border-input bg-background text-sm font-medium hover:bg-muted transition-colors"
              >
                Cancel
              </button>
            </div>
          ) : (
            <button
              onClick={() => setConfirmDelete(true)}
              className="inline-flex items-center gap-2 h-10 px-4 rounded-md border border-input bg-background text-destructive text-sm font-medium hover:bg-destructive/10 transition-colors"
            >
              <Trash2 className="w-4 h-4" /> Delete
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
