"use client"

import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { useState } from "react"
import { ArrowLeft, Play, Square, RotateCw, Monitor, Cpu, MemoryStick, HardDrive, Terminal, Trash2, Gauge } from "lucide-react"
import { useVM, useVMAction, useDeleteVM, useVMStatusStream } from "@/lib/hooks/use-vms"
import { useVMNetworks } from "@/lib/hooks/use-networks"

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

export default function ClientVMDetailPage() {
  const params = useParams()
  const router = useRouter()
  const vmId = params.id as string
  useVMStatusStream()
  const { data: vm, isLoading, isError } = useVM(vmId)
  const action = useVMAction(vmId)
  const del = useDeleteVM()
  const [confirmDelete, setConfirmDelete] = useState(false)

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

  return (
    <div className="space-y-6 max-w-4xl">
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
      </div>

      {/* Specs */}
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <Spec icon={Cpu} label="vCPU" value={`${vm.resources.cpu}`} />
        <Spec icon={MemoryStick} label="Memory" value={`${vm.resources.ram} MB`} />
        <Spec icon={HardDrive} label="Disk" value={`${vm.resources.disk} GB`} />
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
