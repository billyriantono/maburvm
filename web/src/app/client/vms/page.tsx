"use client"

import Link from "next/link"
import { useState } from "react"
import { toast } from "sonner"
import { Monitor, PlusCircle, Play, Square, RotateCw, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusBadge, useVMTransitions } from "@/components/status-badge"
import { useVMs, useVMStatusStream, useVMActions } from "@/lib/hooks/use-vms"
import { CountryFlag } from "@/components/country-flag"
import type { VM } from "@/types"

export default function ClientVMsPage() {
  useVMStatusStream()
  const { data, isLoading, error, refetch } = useVMs({ pageSize: 100 })
  const action = useVMActions()
  const vms: VM[] = data?.data ?? []
  const { transitionOf, begin } = useVMTransitions(vms)
  // Which VM is waiting on which request, so only that row is disabled.
  const [inFlight, setInFlight] = useState<Record<string, string>>({})

  const busy = (status: string) => status === "creating" || status === "deleting"

  const run = (vm: VM, act: string, label: string) => {
    setInFlight((p) => ({ ...p, [vm.id]: act }))
    const done = () =>
      setInFlight((p) => {
        const n = { ...p }
        delete n[vm.id]
        return n
      })
    action.mutate(
      { vmId: vm.id, action: act },
      {
        onSuccess: () => {
          done()
          begin(vm.id, act, vm.status)
          toast.success(`${label} requested for ${vm.hostname}`)
        },
        onError: (err) => {
          done()
          toast.error(err.message || `Could not complete the request for ${vm.hostname}`)
        },
      }
    )
  }

  const powerButton = ({ vm, act, label, doing, icon: Icon, variant, className }: {
    vm: VM; act: string; label: string; doing: string; icon: React.ElementType
    variant: "outline" | "success"; className?: string
  }) => {
    const mine = inFlight[vm.id] === act
    return (
      <Button
        key={act}
        size="sm"
        variant={variant}
        className={className}
        onClick={() => run(vm, act, label)}
        disabled={!!inFlight[vm.id] || !!transitionOf(vm.id) || busy(vm.status)}
      >
        {mine ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Icon aria-hidden="true" />}
        {mine ? doing : label}
      </Button>
    )
  }

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
        ) : error && !data ? (
          <div className="p-10 text-center">
            <p className="font-medium text-muted-foreground">Could not load your VMs.</p>
            <p className="text-sm text-muted-foreground mt-1">{(error as Error).message}</p>
            <Button className="mt-4" variant="outline" onClick={() => refetch()}>Retry</Button>
          </div>
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
                    <p className="text-xs text-muted-foreground flex items-center gap-1.5">
                      {vm.region_name && (
                        <>
                          <CountryFlag country={vm.region_country} />
                          {vm.region_name} ·{" "}
                        </>
                      )}
                      {vm.resources.cpu} vCPU · {vm.resources.ram} MB RAM · {vm.resources.disk} GB disk
                    </p>
                  </div>
                </Link>
                <div className="flex items-center gap-2">
                  <StatusBadge status={vm.status} transition={transitionOf(vm.id)} />
                  {vm.status === "stopped" &&
                    powerButton({ vm, act: "start", label: "Start", doing: "Starting", icon: Play, variant: "success" })}
                  {vm.status === "running" && (
                    <>
                      {powerButton({ vm, act: "restart", label: "Reboot", doing: "Rebooting", icon: RotateCw, variant: "outline" })}
                      {powerButton({ vm, act: "stop", label: "Stop", doing: "Stopping", icon: Square, variant: "outline", className: "text-destructive hover:bg-destructive/10 hover:text-destructive" })}
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
