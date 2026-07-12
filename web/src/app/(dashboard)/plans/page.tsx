"use client"

import { FormEvent, useState } from "react"
import {
  Boxes,
  Cpu,
  HardDrive,
  Loader2,
  MemoryStick,
  Pencil,
  Plus,
  Trash2,
  Zap,
} from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { usePlans, useCreatePlan, useUpdatePlan, useDeletePlan } from "@/lib/hooks/use-plans"
import type { CreatePlanRequest, Plan } from "@/types/plan"

const emptyForm: CreatePlanRequest = {
  name: "",
  cpu: 1,
  ram: 1024,
  disk: 20,
  bandwidth_mbps: 0,
  description: "",
  is_active: true,
}

export default function PlansPage() {
  const { data: plans, isLoading, error } = usePlans()
  const createPlan = useCreatePlan()
  const updatePlan = useUpdatePlan()
  const deletePlan = useDeletePlan()

  const [showForm, setShowForm] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [form, setForm] = useState<CreatePlanRequest>(emptyForm)

  const openCreate = () => {
    setEditingId(null)
    setForm(emptyForm)
    setShowForm(true)
  }

  const openEdit = (plan: Plan) => {
    setEditingId(plan.id)
    setForm({
      name: plan.name,
      cpu: plan.cpu,
      ram: plan.ram,
      disk: plan.disk,
      bandwidth_mbps: plan.bandwidth_mbps,
      description: plan.description ?? "",
      is_active: plan.is_active,
    })
    setShowForm(true)
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    try {
      if (editingId) {
        await updatePlan.mutateAsync({ id: editingId, data: form })
        toast.success(`Plan "${form.name}" updated`)
      } else {
        await createPlan.mutateAsync(form)
        toast.success(`Plan "${form.name}" created`)
      }
      setShowForm(false)
      setEditingId(null)
      setForm(emptyForm)
    } catch (err) {
      toast.error(`Failed to save plan: ${(err as Error).message}`)
    }
  }

  const handleDelete = async (plan: Plan) => {
    if (!window.confirm(`Delete plan "${plan.name}"? VMs already created keep their resources.`)) return
    try {
      await deletePlan.mutateAsync(plan.id)
      toast.success(`Plan "${plan.name}" deleted`)
    } catch (err) {
      toast.error(`Failed to delete plan: ${(err as Error).message}`)
    }
  }

  const saving = createPlan.isPending || updatePlan.isPending

  return (
    <div className="max-w-6xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2">
            <Boxes className="w-8 h-8" />
            Plans
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            VPS flavors — reusable bundles of CPU, RAM, disk &amp; bandwidth
          </p>
        </div>
        <Button className="gap-2" onClick={openCreate}>
          <Plus className="w-4 h-4" />
          Create Plan
        </Button>
      </div>

      {showForm && (
        <form onSubmit={handleSubmit} className="bg-white border-4 border-black p-5 shadow-neo mb-6">
          <h2 className="text-xl font-black uppercase mb-4">{editingId ? "Edit Plan" : "Create Plan"}</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            <div>
              <label className="block text-xs font-black uppercase text-gray-500 mb-1">Name</label>
              <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} className="border-2 border-black" required />
            </div>
            <div>
              <label className="block text-xs font-black uppercase text-gray-500 mb-1">vCPU</label>
              <Input type="number" min={1} max={128} value={form.cpu} onChange={(e) => setForm({ ...form, cpu: Number(e.target.value) })} className="border-2 border-black" required />
            </div>
            <div>
              <label className="block text-xs font-black uppercase text-gray-500 mb-1">RAM (MB)</label>
              <Input type="number" min={128} step={128} value={form.ram} onChange={(e) => setForm({ ...form, ram: Number(e.target.value) })} className="border-2 border-black" required />
            </div>
            <div>
              <label className="block text-xs font-black uppercase text-gray-500 mb-1">Disk (GB)</label>
              <Input type="number" min={1} value={form.disk} onChange={(e) => setForm({ ...form, disk: Number(e.target.value) })} className="border-2 border-black" required />
            </div>
            <div>
              <label className="block text-xs font-black uppercase text-gray-500 mb-1">Network Speed (Mbps · 0 = unlimited)</label>
              <Input type="number" min={0} max={10000} value={form.bandwidth_mbps ?? 0} onChange={(e) => setForm({ ...form, bandwidth_mbps: Number(e.target.value) })} className="border-2 border-black" />
              <p className="text-[10px] text-gray-400 mt-1 font-medium">Interface rate cap applied to VMs on this plan (e.g. 100, 1000, 10000). Not the monthly data quota.</p>
            </div>
            <div>
              <label className="block text-xs font-black uppercase text-gray-500 mb-1">Description</label>
              <Input value={form.description ?? ""} onChange={(e) => setForm({ ...form, description: e.target.value })} className="border-2 border-black" />
            </div>
          </div>
          <label className="flex items-center gap-2 mt-4 cursor-pointer w-fit">
            <input type="checkbox" checked={form.is_active ?? true} onChange={(e) => setForm({ ...form, is_active: e.target.checked })} className="w-4 h-4" />
            <span className="font-bold text-sm uppercase">Active (selectable when creating VMs)</span>
          </label>
          <div className="flex justify-end gap-3 mt-4">
            <Button type="button" variant="ghost" onClick={() => { setShowForm(false); setEditingId(null) }}>Cancel</Button>
            <Button type="submit" disabled={saving}>
              {saving && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
              {editingId ? "Save Changes" : "Create Plan"}
            </Button>
          </div>
        </form>
      )}

      {error && (
        <div className="bg-danger/10 border-4 border-danger p-6 shadow-neo mb-6">
          <p className="font-black uppercase">Failed to load plans</p>
          <p className="text-sm font-medium">{(error as Error).message}</p>
        </div>
      )}

      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
          <div className="col-span-3">Name</div>
          <div className="col-span-2">vCPU</div>
          <div className="col-span-2">RAM</div>
          <div className="col-span-2">Disk</div>
          <div className="col-span-1">Bandwidth</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {isLoading ? (
          <div className="flex items-center justify-center py-16">
            <Loader2 className="w-8 h-8 animate-spin" />
            <span className="ml-3 font-bold uppercase">Loading plans...</span>
          </div>
        ) : !plans || plans.length === 0 ? (
          <div className="p-12 text-center">
            <Boxes className="w-16 h-16 text-gray-300 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase mb-4">No plans yet</p>
            <Button onClick={openCreate} className="gap-2">
              <Plus className="w-4 h-4" />
              Create your first plan
            </Button>
          </div>
        ) : (
          plans.map((plan, index) => (
            <div key={plan.id} className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${index % 2 === 0 ? "bg-white" : "bg-gray-50"}`}>
              <div className="col-span-3">
                <div className="flex items-center gap-2">
                  <span className="font-black uppercase text-black truncate">{plan.name}</span>
                  {!plan.is_active && <Badge variant="secondary" className="text-[10px]">Inactive</Badge>}
                </div>
                {plan.description && <p className="text-xs text-gray-500 truncate">{plan.description}</p>}
              </div>
              <div className="col-span-2 flex items-center gap-1 font-bold"><Cpu className="w-4 h-4 text-gray-500" />{plan.cpu}</div>
              <div className="col-span-2 flex items-center gap-1 font-bold"><MemoryStick className="w-4 h-4 text-gray-500" />{plan.ram} MB</div>
              <div className="col-span-2 flex items-center gap-1 font-bold"><HardDrive className="w-4 h-4 text-gray-500" />{plan.disk} GB</div>
              <div className="col-span-1 flex items-center gap-1 font-bold text-sm"><Zap className="w-3 h-3 text-gray-500" />{plan.bandwidth_mbps ? `${plan.bandwidth_mbps}` : "∞"}</div>
              <div className="col-span-2 flex justify-end gap-2">
                <Button variant="secondary" size="sm" className="h-8 w-8 p-0" title="Edit plan" onClick={() => openEdit(plan)}>
                  <Pencil className="w-4 h-4" />
                </Button>
                <Button variant="ghost" size="sm" className="h-8 w-8 p-0 border-2 border-black hover:bg-danger hover:text-white" title="Delete plan" onClick={() => handleDelete(plan)}>
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
