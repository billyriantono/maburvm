"use client"

import Link from "next/link"
import { Monitor, PlusCircle, Play, Square, AlertTriangle } from "lucide-react"
import { useVMs, useVMStatusStream } from "@/lib/hooks/use-vms"
import type { VM } from "@/types"

function StatCard({ label, value, icon: Icon }: { label: string; value: number | string; icon: React.ElementType }) {
  return (
    <div className="bg-white border-4 border-black p-5 shadow-neo">
      <div className="flex items-center justify-between">
        <div>
          <p className="text-[11px] font-black uppercase tracking-widest text-gray-500">{label}</p>
          <p className="text-3xl font-black mt-1">{value}</p>
        </div>
        <div className="w-12 h-12 bg-primary border-2 border-black flex items-center justify-center">
          <Icon className="w-6 h-6 text-black" />
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
          <h1 className="text-3xl font-black uppercase tracking-tighter">Dashboard</h1>
          <p className="text-sm font-medium text-gray-600 mt-1">Overview of your virtual machines</p>
        </div>
        <Link
          href="/client/order"
          className="inline-flex items-center gap-2 h-11 px-5 bg-primary text-black border-2 border-black font-black uppercase text-sm shadow-neo hover:shadow-neo-sm transition-all"
        >
          <PlusCircle className="w-5 h-5" /> Order VM
        </Link>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard label="Total VMs" value={isLoading ? "—" : vms.length} icon={Monitor} />
        <StatCard label="Running" value={isLoading ? "—" : running} icon={Play} />
        <StatCard label="Stopped" value={isLoading ? "—" : stopped} icon={Square} />
        <StatCard label="Problems" value={isLoading ? "—" : problems} icon={AlertTriangle} />
      </div>

      <div className="bg-white border-4 border-black shadow-neo">
        <div className="flex items-center justify-between px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight">My Virtual Machines</h2>
          <Link href="/client/vms" className="text-sm font-bold uppercase underline">
            View all
          </Link>
        </div>
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
            {vms.slice(0, 5).map((vm) => (
              <li key={vm.id}>
                <Link href={`/client/vms/${vm.id}`} className="flex items-center justify-between px-5 py-4 hover:bg-gray-50 transition-colors">
                  <div className="flex items-center gap-3 min-w-0">
                    <Monitor className="w-5 h-5 shrink-0" />
                    <div className="min-w-0">
                      <p className="font-bold truncate">{vm.hostname}</p>
                      <p className="text-xs text-gray-500">
                        {vm.resources.cpu} vCPU · {vm.resources.ram} MB RAM · {vm.resources.disk} GB
                      </p>
                    </div>
                  </div>
                  <span className="text-xs font-black uppercase tracking-wider px-3 py-1 border-2 border-black">
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
