"use client"

import Link from "next/link"
import { Monitor, PlusCircle, Play, Square, RotateCw } from "lucide-react"
import { useVMs, useVMStatusStream, useVMActions } from "@/lib/hooks/use-vms"
import type { VM } from "@/types"

function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-400 dark:border-emerald-900",
    stopped: "bg-muted text-muted-foreground border-border",
    suspended: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-400 dark:border-amber-900",
    creating: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-400 dark:border-blue-900",
    deleting: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-400 dark:border-amber-900",
    error: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-400 dark:border-red-900",
  }
  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-xs font-medium capitalize rounded-md border ${colors[status] || "bg-muted text-muted-foreground border-border"}`}>
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
          <h1 className="text-2xl font-semibold tracking-tight">My VMs</h1>
          <p className="text-sm text-muted-foreground mt-1">Manage your virtual machines</p>
        </div>
        <Link
          href="/client/order"
          className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors"
        >
          <PlusCircle className="w-4 h-4" /> Order VM
        </Link>
      </div>

      <div className="rounded-lg border bg-card text-card-foreground shadow-sm">
        {isLoading ? (
          <div className="p-8 text-center text-muted-foreground">Loading…</div>
        ) : vms.length === 0 ? (
          <div className="p-10 text-center">
            <p className="font-medium text-muted-foreground">You don&apos;t have any VMs yet.</p>
            <Link href="/client/order" className="inline-flex items-center gap-2 mt-4 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors">
              <PlusCircle className="w-4 h-4" /> Order your first VM
            </Link>
          </div>
        ) : (
          <ul className="divide-y">
            {vms.map((vm) => (
              <li key={vm.id} className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 px-5 py-4 hover:bg-muted/50 transition-colors">
                <Link href={`/client/vms/${vm.id}`} className="flex items-center gap-3 min-w-0 flex-1">
                  <Monitor className="w-5 h-5 shrink-0 text-muted-foreground" />
                  <div className="min-w-0">
                    <p className="font-medium truncate">{vm.hostname}</p>
                    <p className="text-xs text-muted-foreground">
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
                      className="inline-flex items-center gap-1 h-9 px-3 rounded-md bg-emerald-600 text-white text-xs font-medium hover:bg-emerald-700 transition-colors disabled:opacity-50"
                    >
                      <Play className="w-4 h-4" /> Start
                    </button>
                  )}
                  {vm.status === "running" && (
                    <>
                      <button
                        onClick={() => action.mutate({ vmId: vm.id, action: "restart" })}
                        disabled={action.isPending}
                        className="inline-flex items-center gap-1 h-9 px-3 rounded-md border border-input bg-background text-xs font-medium hover:bg-muted transition-colors disabled:opacity-50"
                      >
                        <RotateCw className="w-4 h-4" /> Reboot
                      </button>
                      <button
                        onClick={() => action.mutate({ vmId: vm.id, action: "stop" })}
                        disabled={action.isPending}
                        className="inline-flex items-center gap-1 h-9 px-3 rounded-md border border-input bg-background text-destructive text-xs font-medium hover:bg-destructive/10 transition-colors disabled:opacity-50"
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
