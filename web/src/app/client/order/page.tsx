"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import { ArrowLeft, Check, Cpu, MemoryStick, HardDrive, Gauge } from "lucide-react"
import { usePlans } from "@/lib/hooks/use-plans"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useCreateVM } from "@/lib/hooks/use-vms"
import type { OSTemplate } from "@/types"
import type { Plan } from "@/types/plan"

// A client orders a VM by picking a plan (resource flavor) + an OS template and a
// hostname. Ownership is forced server-side to the caller, and quota is enforced,
// so this form can't be used to provision on behalf of another tenant or exceed
// the account's limits.
export default function OrderVMPage() {
  const router = useRouter()
  const { data: plans, isLoading: plansLoading } = usePlans(true)
  const { data: templates, isLoading: templatesLoading } = useTemplates()
  const createVM = useCreateVM()

  const [planId, setPlanId] = useState<string>("")
  const [templateId, setTemplateId] = useState<string>("")
  const [hostname, setHostname] = useState<string>("")
  const [error, setError] = useState<string>("")

  const activePlans: Plan[] = (plans ?? []).filter((p) => p.is_active)
  const activeTemplates: OSTemplate[] = (templates ?? []).filter((t) => t.is_active)
  const selectedPlan = activePlans.find((p) => p.id === planId)

  const hostnameValid = /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/.test(hostname)
  const canSubmit = !!planId && !!templateId && hostnameValid && !createVM.isPending

  const handleSubmit = () => {
    setError("")
    if (!selectedPlan) {
      setError("Please select a plan.")
      return
    }
    createVM.mutate(
      {
        hostname,
        os_template_id: templateId,
        plan_id: planId,
        // Resources are re-derived from the plan server-side; we send the plan's
        // values so the request passes validation.
        resources: {
          cpu: selectedPlan.cpu,
          ram: selectedPlan.ram,
          disk: selectedPlan.disk,
        },
        bandwidth_mbps: selectedPlan.bandwidth_mbps,
      },
      {
        onSuccess: () => router.push("/client/vms"),
        onError: (e: Error) => setError(e.message || "Failed to create VM"),
      }
    )
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-3">
        <Link href="/client/vms" className="p-2 border-2 border-black bg-white hover:bg-gray-50">
          <ArrowLeft className="w-5 h-5" />
        </Link>
        <h1 className="text-3xl font-black uppercase tracking-tighter">Order a VM</h1>
      </div>

      {/* Step 1: plan */}
      <section className="bg-white border-4 border-black shadow-neo">
        <div className="px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight">1 · Choose a plan</h2>
        </div>
        <div className="p-5">
          {plansLoading ? (
            <p className="text-gray-500 font-medium">Loading plans…</p>
          ) : activePlans.length === 0 ? (
            <p className="text-gray-600 font-medium">No plans are available. Please contact support.</p>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              {activePlans.map((p) => {
                const active = p.id === planId
                return (
                  <button
                    key={p.id}
                    onClick={() => setPlanId(p.id)}
                    className={`text-left p-4 border-2 transition-all ${active ? "border-black bg-primary shadow-neo-sm" : "border-black bg-white hover:shadow-neo-sm"}`}
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-black uppercase">{p.name}</span>
                      {active && <Check className="w-5 h-5" />}
                    </div>
                    <div className="mt-2 grid grid-cols-2 gap-1 text-xs font-bold text-gray-700">
                      <span className="flex items-center gap-1"><Cpu className="w-3 h-3" />{p.cpu} vCPU</span>
                      <span className="flex items-center gap-1"><MemoryStick className="w-3 h-3" />{p.ram} MB</span>
                      <span className="flex items-center gap-1"><HardDrive className="w-3 h-3" />{p.disk} GB</span>
                      <span className="flex items-center gap-1"><Gauge className="w-3 h-3" />{p.bandwidth_mbps} Mbps</span>
                    </div>
                  </button>
                )
              })}
            </div>
          )}
        </div>
      </section>

      {/* Step 2: OS */}
      <section className="bg-white border-4 border-black shadow-neo">
        <div className="px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight">2 · Choose an operating system</h2>
        </div>
        <div className="p-5">
          {templatesLoading ? (
            <p className="text-gray-500 font-medium">Loading templates…</p>
          ) : activeTemplates.length === 0 ? (
            <p className="text-gray-600 font-medium">No OS templates are available. Please contact support.</p>
          ) : (
            <select
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value)}
              className="w-full h-11 px-3 border-2 border-black bg-white font-bold focus:outline-none focus:shadow-neo-sm"
            >
              <option value="">Select an OS…</option>
              {activeTemplates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name} {t.version}
                </option>
              ))}
            </select>
          )}
        </div>
      </section>

      {/* Step 3: hostname */}
      <section className="bg-white border-4 border-black shadow-neo">
        <div className="px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight">3 · Hostname</h2>
        </div>
        <div className="p-5">
          <input
            type="text"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            placeholder="my-server.example.com"
            className="w-full h-11 px-3 border-2 border-black bg-white font-bold placeholder:text-gray-400 focus:outline-none focus:shadow-neo-sm"
          />
          {hostname !== "" && !hostnameValid && (
            <p className="text-sm text-destructive font-bold mt-2">
              Enter a valid hostname (letters, digits, hyphens, dots).
            </p>
          )}
        </div>
      </section>

      {error && (
        <div className="border-4 border-black bg-[#FF4444] text-white px-5 py-4 font-bold">
          {error}
        </div>
      )}

      <button
        onClick={handleSubmit}
        disabled={!canSubmit}
        className="w-full h-12 bg-primary text-black border-4 border-black font-black uppercase tracking-wide shadow-neo hover:shadow-neo-sm transition-all disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {createVM.isPending ? "Creating…" : "Create VM"}
      </button>
    </div>
  )
}
