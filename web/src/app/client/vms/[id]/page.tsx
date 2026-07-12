"use client"

import { useParams, useRouter } from "next/navigation"
import Link from "next/link"
import { useState } from "react"
import { ArrowLeft, Play, Square, RotateCw, Monitor, Cpu, MemoryStick, HardDrive, Terminal, Trash2, Gauge } from "lucide-react"
import { useVM, useVMAction, useDeleteVM, useVMStatusStream } from "@/lib/hooks/use-vms"
import { useVMNetworks, useSetVMBandwidth } from "@/lib/hooks/use-networks"

// Preset speed tiers a client can self-upgrade to (Mbps). 0 = unlimited.
const SPEED_TIERS: { label: string; mbps: number }[] = [
  { label: "100 Mbps", mbps: 100 },
  { label: "500 Mbps", mbps: 500 },
  { label: "1 Gbps", mbps: 1000 },
  { label: "2.5 Gbps", mbps: 2500 },
  { label: "5 Gbps", mbps: 5000 },
  { label: "10 Gbps", mbps: 10000 },
]

function speedLabel(mbps: number): string {
  if (mbps <= 0) return "Unlimited"
  if (mbps % 1000 === 0) return `${mbps / 1000} Gbps`
  return `${mbps} Mbps`
}

// NetworkSpeedCard lets a client upgrade/downgrade the speed of their VM's
// network interfaces. Ownership is enforced server-side by the bandwidth
// endpoint, so a client can only change their own VM.
function NetworkSpeedCard({ vmId }: { vmId: string }) {
  const { data: networks } = useVMNetworks(vmId)
  const setBandwidth = useSetVMBandwidth(vmId)
  const [pending, setPending] = useState<string | null>(null)

  if (!networks?.length) return null

  return (
    <div className="bg-white border-4 border-black shadow-neo">
      <div className="px-5 py-4 border-b-4 border-black flex items-center gap-2">
        <Gauge className="w-5 h-5" />
        <h2 className="text-lg font-black uppercase tracking-tight">Network Speed</h2>
      </div>
      <div className="p-5 space-y-5">
        {networks.map((iface) => (
          <div key={iface.id} className="space-y-2">
            <div className="flex items-center justify-between">
              <span className="font-mono font-bold">{iface.ip_address}</span>
              <span className="text-sm font-black uppercase bg-[#CCFF00] border-2 border-black px-2 py-0.5">
                {speedLabel(iface.bandwidth_limit)}
              </span>
            </div>
            <div className="flex flex-wrap gap-2">
              {SPEED_TIERS.map((t) => {
                const active = iface.bandwidth_limit === t.mbps
                const busy = pending === iface.id && setBandwidth.isPending
                return (
                  <button
                    key={t.mbps}
                    type="button"
                    disabled={active || busy}
                    onClick={() => {
                      setPending(iface.id)
                      setBandwidth.mutate(
                        { networkId: iface.id, bandwidthMbps: t.mbps },
                        { onSettled: () => setPending(null) },
                      )
                    }}
                    className={`h-9 px-3 border-2 border-black text-xs font-black uppercase disabled:opacity-50 ${
                      active ? "bg-black text-primary" : "bg-white text-black hover:bg-gray-50"
                    }`}
                  >
                    {busy && pending === iface.id ? "…" : t.label}
                  </button>
                )
              })}
            </div>
          </div>
        ))}
        <p className="text-xs text-gray-500 font-medium">
          Speed changes apply immediately to the running VM. The highest available tier is 10 Gbps.
        </p>
      </div>
    </div>
  )
}

function StatusBadge({ status }: { status?: string }) {
  const colors: Record<string, string> = {
    running: "bg-[#CCFF00] text-black",
    stopped: "bg-[#FF4444] text-white",
    suspended: "bg-[#FFAA00] text-black",
    creating: "bg-[#00CCFF] text-black",
    deleting: "bg-[#FF8800] text-black",
    error: "bg-[#FF0000] text-white",
  }
  return (
    <span className={`inline-flex items-center px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${colors[status || ""] || "bg-gray-200 text-black"}`}>
      {status || "unknown"}
    </span>
  )
}

function Spec({ icon: Icon, label, value }: { icon: React.ElementType; label: string; value: string }) {
  return (
    <div className="bg-white border-2 border-black p-4">
      <div className="flex items-center gap-2 text-gray-500">
        <Icon className="w-4 h-4" />
        <span className="text-[11px] font-black uppercase tracking-widest">{label}</span>
      </div>
      <p className="text-xl font-black mt-1">{value}</p>
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
    return <div className="p-8 text-center text-gray-500 font-medium">Loading…</div>
  }
  if (isError || !vm) {
    return (
      <div className="p-10 text-center">
        <p className="font-bold text-gray-700">VM not found.</p>
        <Link href="/client/vms" className="inline-block mt-4 underline font-bold">Back to My VMs</Link>
      </div>
    )
  }

  const handleDelete = () => {
    del.mutate(vmId, { onSuccess: () => router.push("/client/vms") })
  }

  return (
    <div className="space-y-6 max-w-4xl">
      <div className="flex items-center gap-3">
        <Link href="/client/vms" className="p-2 border-2 border-black bg-white hover:bg-gray-50">
          <ArrowLeft className="w-5 h-5" />
        </Link>
        <Monitor className="w-6 h-6" />
        <h1 className="text-2xl font-black uppercase tracking-tighter truncate">{vm.hostname}</h1>
        <StatusBadge status={vm.status} />
      </div>

      {/* Actions */}
      <div className="flex flex-wrap items-center gap-2">
        {vm.status === "stopped" && (
          <button
            onClick={() => action.mutate("start")}
            disabled={action.isPending}
            className="inline-flex items-center gap-2 h-10 px-4 bg-[#CCFF00] text-black border-2 border-black font-black uppercase text-sm disabled:opacity-50"
          >
            <Play className="w-4 h-4" /> Start
          </button>
        )}
        {vm.status === "running" && (
          <>
            <button
              onClick={() => action.mutate("restart")}
              disabled={action.isPending}
              className="inline-flex items-center gap-2 h-10 px-4 bg-white text-black border-2 border-black font-black uppercase text-sm disabled:opacity-50"
            >
              <RotateCw className="w-4 h-4" /> Reboot
            </button>
            <button
              onClick={() => action.mutate("stop")}
              disabled={action.isPending}
              className="inline-flex items-center gap-2 h-10 px-4 bg-[#FF4444] text-white border-2 border-black font-black uppercase text-sm disabled:opacity-50"
            >
              <Square className="w-4 h-4" /> Stop
            </button>
          </>
        )}
        <Link
          href={`/client/vms/${vm.id}/console`}
          className="inline-flex items-center gap-2 h-10 px-4 bg-black text-primary border-2 border-black font-black uppercase text-sm"
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
      <div className="bg-white border-4 border-black shadow-neo">
        <div className="px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight text-destructive">Danger Zone</h2>
        </div>
        <div className="p-5 flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div>
            <p className="font-bold">Destroy this VM</p>
            <p className="text-sm text-gray-600">This permanently deletes the VM and its disks. This cannot be undone.</p>
          </div>
          {confirmDelete ? (
            <div className="flex items-center gap-2">
              <button
                onClick={handleDelete}
                disabled={del.isPending}
                className="inline-flex items-center gap-2 h-10 px-4 bg-[#FF0000] text-white border-2 border-black font-black uppercase text-sm disabled:opacity-50"
              >
                <Trash2 className="w-4 h-4" /> {del.isPending ? "Deleting…" : "Confirm"}
              </button>
              <button
                onClick={() => setConfirmDelete(false)}
                className="h-10 px-4 bg-white text-black border-2 border-black font-black uppercase text-sm"
              >
                Cancel
              </button>
            </div>
          ) : (
            <button
              onClick={() => setConfirmDelete(true)}
              className="inline-flex items-center gap-2 h-10 px-4 bg-white text-destructive border-2 border-black font-black uppercase text-sm"
            >
              <Trash2 className="w-4 h-4" /> Delete
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
