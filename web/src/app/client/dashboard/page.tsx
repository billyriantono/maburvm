"use client"

import Link from "next/link"
import { Monitor, PlusCircle, Play, Square, AlertTriangle } from "lucide-react"
import { useVMs, useVMStatusStream } from "@/lib/hooks/use-vms"
import type { VM } from "@/types"

function StatCard({ label, value, icon: Icon }: { label: string; value: number | string; icon: React.ElementType }) {
  return (
    <div className="rounded-lg border bg-card text-card-foreground p-5 shadow-sm">
      <div className="flex items-center justify-between">
        <div>
          <p className="text-xs font-medium text-muted-foreground">{label}</p>
          <p className="text-3xl font-semibold mt-1">{value}</p>
        </div>
        <div className="w-11 h-11 rounded-md bg-muted flex items-center justify-center">
          <Icon className="w-5 h-5 text-muted-foreground" />
        </div>
      </div>
    </div>
  )
}

export default function ClientDashboardPage() {
  useVMStatusStream()
  const { data, isLoading } = useVMs({ pageSize: 100 })
  const vms: VM[] = data?.data ?? []

  const running = vms.filter((v) => v.status === "running").length
  const stopped = vms.filter((v) => v.status === "stopped").length
  const problems = vms.filter((v) => v.status === "error").length

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
          <p className="text-sm text-muted-foreground mt-1">Overview of your virtual machines</p>
        </div>
        <Link
          href="/client/order"
          className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors"
        >
          <PlusCircle className="w-4 h-4" /> Order VM
        </Link>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard label="Total VMs" value={isLoading ? "—" : vms.length} icon={Monitor} />
        <StatCard label="Running" value={isLoading ? "—" : running} icon={Play} />
        <StatCard label="Stopped" value={isLoading ? "—" : stopped} icon={Square} />
        <StatCard label="Problems" value={isLoading ? "—" : problems} icon={AlertTriangle} />
      </div>

      <div className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="flex items-center justify-between px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">My Virtual Machines</h2>
          <Link href="/client/vms" className="text-sm font-medium text-primary hover:underline">
            View all
          </Link>
        </div>
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
            {vms.slice(0, 5).map((vm) => (
              <li key={vm.id}>
                <Link href={`/client/vms/${vm.id}`} className="flex items-center justify-between px-5 py-4 hover:bg-muted/50 transition-colors">
                  <div className="flex items-center gap-3 min-w-0">
                    <Monitor className="w-5 h-5 shrink-0 text-muted-foreground" />
                    <div className="min-w-0">
                      <p className="font-medium truncate">{vm.hostname}</p>
                      <p className="text-xs text-muted-foreground">
                        {vm.resources.cpu} vCPU · {vm.resources.ram} MB RAM · {vm.resources.disk} GB
                      </p>
                    </div>
                  </div>
                  <span className="text-xs font-medium px-2 py-0.5 rounded-md border bg-muted text-muted-foreground">
                    {vm.status}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
