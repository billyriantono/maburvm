"use client"

import Link from "next/link"
import { Monitor, PlusCircle, Play, Square, RotateCw } from "lucide-react"
import { useVMs, useVMStatusStream, useVMActions } from "@/lib/hooks/use-vms"
import type { VM } from "@/types"

function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, string> = {
    running: "bg-[#CCFF00] text-black",
    stopped: "bg-[#FF4444] text-white",
    suspended: "bg-[#FFAA00] text-black",
    creating: "bg-[#00CCFF] text-black",
    deleting: "bg-[#FF8800] text-black",
    error: "bg-[#FF0000] text-white",
  }
  return (
    <span className={`inline-flex items-center px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${colors[status] || "bg-gray-200 text-black"}`}>
      {status}
    </span>
  )
}

export default function ClientVMsPage() {
  useVMStatusStream()
  const { data, isLoading } = useVMs({ pageSize: 100 })
  const action = useVMActions()
  const vms: VM[] = data?.data ?? []

  const busy = (status: string) => status === "creating" || status === "deleting"

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tighter">My VMs</h1>
          <p className="text-sm font-medium text-gray-600 mt-1">Manage your virtual machines</p>
        </div>
        <Link
          href="/client/order"
          className="inline-flex items-center gap-2 h-11 px-5 bg-primary text-black border-2 border-black font-black uppercase text-sm shadow-neo hover:shadow-neo-sm transition-all"
        >
          <PlusCircle className="w-5 h-5" /> Order VM
        </Link>
      </div>

      <div className="bg-white border-4 border-black shadow-neo">
        {isLoading ? (
          <div className="p-8 text-center text-gray-500 font-medium">Loading…</div>
        ) : vms.length === 0 ? (
          <div className="p-10 text-center">
            <p className="font-bold text-gray-700">You don&apos;t have any VMs yet.</p>
            <Link href="/client/order" className="inline-flex items-center gap-2 mt-4 h-10 px-4 bg-primary text-black border-2 border-black font-black uppercase text-sm">
              <PlusCircle className="w-4 h-4" /> Order your first VM
            </Link>
          </div>
        ) : (
          <ul className="divide-y-2 divide-black">
            {vms.map((vm) => (
              <li key={vm.id} className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 px-5 py-4 hover:bg-gray-50 transition-colors">
                <Link href={`/client/vms/${vm.id}`} className="flex items-center gap-3 min-w-0 flex-1">
                  <Monitor className="w-5 h-5 shrink-0" />
                  <div className="min-w-0">
                    <p className="font-bold truncate">{vm.hostname}</p>
                    <p className="text-xs text-gray-500">
                      {vm.resources.cpu} vCPU · {vm.resources.ram} MB RAM · {vm.resources.disk} GB disk
                    </p>
                  </div>
                </Link>
                <div className="flex items-center gap-2">
                  <StatusBadge status={vm.status} />
                  {vm.status === "stopped" && (
                    <button
                      onClick={() => action.mutate({ vmId: vm.id, action: "start" })}
                      disabled={action.isPending || busy(vm.status)}
                      className="inline-flex items-center gap-1 h-9 px-3 bg-[#CCFF00] text-black border-2 border-black font-bold uppercase text-xs disabled:opacity-50"
                    >
                      <Play className="w-4 h-4" /> Start
                    </button>
                  )}
                  {vm.status === "running" && (
                    <>
                      <button
                        onClick={() => action.mutate({ vmId: vm.id, action: "restart" })}
                        disabled={action.isPending}
                        className="inline-flex items-center gap-1 h-9 px-3 bg-white text-black border-2 border-black font-bold uppercase text-xs disabled:opacity-50"
                      >
                        <RotateCw className="w-4 h-4" /> Reboot
                      </button>
                      <button
                        onClick={() => action.mutate({ vmId: vm.id, action: "stop" })}
                        disabled={action.isPending}
                        className="inline-flex items-center gap-1 h-9 px-3 bg-[#FF4444] text-white border-2 border-black font-bold uppercase text-xs disabled:opacity-50"
                      >
                        <Square className="w-4 h-4" /> Stop
                      </button>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
