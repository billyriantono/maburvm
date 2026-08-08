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
import { useConfirm } from "@/components/confirm-provider"

const emptyForm: CreatePlanRequest = {
  name: "",
  cpu: 1,
  ram: 1024,
  disk: 20,
  bandwidth_mbps: 0,
  data_quota_gb: 0,
  over_quota_policy: "throttle",
  throttle_speed_mbps: 0,
  description: "",
  is_active: true,
}

export default function PlansPage() {
  const confirm = useConfirm()
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
      data_quota_gb: plan.data_quota_gb,
      over_quota_policy: plan.over_quota_policy,
      throttle_speed_mbps: plan.throttle_speed_mbps,
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
    const ok = await confirm({
      title: `Delete plan "${plan.name}"?`,
      description:
        "New orders can no longer choose it. VMs already created on this plan keep the resources they have.",
      confirmLabel: "Delete plan",
      destructive: true,
      action: () => deletePlan.mutateAsync(plan.id),
    })
    if (!ok) return
    toast.success(`Plan "${plan.name}" deleted`)
  }

  const saving = createPlan.isPending || updatePlan.isPending

  return (
    <div className="max-w-6xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
            <Boxes className="w-7 h-7" />
            Plans
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            VPS flavors — reusable bundles of CPU, RAM, disk &amp; bandwidth
          </p>
        </div>
        <Button className="gap-2" onClick={openCreate}>
          <Plus className="w-4 h-4" />
          Create Plan
        </Button>
      </div>

      {showForm && (
        <form onSubmit={handleSubmit} className="bg-card text-card-foreground border rounded-lg p-5 shadow-sm mb-6">
          <h2 className="text-lg font-semibold mb-4">{editingId ? "Edit Plan" : "Create Plan"}</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">Name</label>
              <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">vCPU</label>
              <Input type="number" min={1} max={128} value={form.cpu} onChange={(e) => setForm({ ...form, cpu: Number(e.target.value) })} required />
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">RAM (MB)</label>
              <Input type="number" min={128} step={128} value={form.ram} onChange={(e) => setForm({ ...form, ram: Number(e.target.value) })} required />
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">Disk (GB)</label>
              <Input type="number" min={1} value={form.disk} onChange={(e) => setForm({ ...form, disk: Number(e.target.value) })} required />
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">Network Speed (Mbps · 0 = unlimited)</label>
              <Input type="number" min={0} max={10000} value={form.bandwidth_mbps ?? 0} onChange={(e) => setForm({ ...form, bandwidth_mbps: Number(e.target.value) })} />
              <p className="text-[10px] text-muted-foreground mt-1">Interface rate cap applied to VMs on this plan (e.g. 100, 1000, 10000). Not the monthly data quota.</p>
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">Data Quota (GB / month · 0 = unlimited)</label>
              <Input type="number" min={0} value={form.data_quota_gb ?? 0} onChange={(e) => setForm({ ...form, data_quota_gb: Number(e.target.value) })} />
              <p className="text-[10px] text-muted-foreground mt-1">Monthly transfer allowance (e.g. 100 = 100 GB). Enforced per calendar month.</p>
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">When Quota Exceeded</label>
              <select
                value={form.over_quota_policy ?? "throttle"}
                onChange={(e) => setForm({ ...form, over_quota_policy: e.target.value as CreatePlanRequest["over_quota_policy"] })}
                className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                disabled={!form.data_quota_gb}
              >
                <option value="throttle">Throttle speed</option>
                <option value="overage">Overage (charge extra)</option>
                <option value="suspend">Suspend VM</option>
              </select>
              <p className="text-[10px] text-muted-foreground mt-1">{!form.data_quota_gb ? "Set a data quota to enable a policy." : "Action once the monthly quota is used up."}</p>
            </div>
            {form.over_quota_policy === "throttle" && !!form.data_quota_gb && (
              <div>
                <label className="block text-xs font-medium text-muted-foreground mb-1">Throttled Speed (Mbps)</label>
                <Input type="number" min={0} max={10000} value={form.throttle_speed_mbps ?? 0} onChange={(e) => setForm({ ...form, throttle_speed_mbps: Number(e.target.value) })} />
                <p className="text-[10px] text-muted-foreground mt-1">Speed after quota is hit (e.g. 10). 0 = a low default.</p>
              </div>
            )}
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">Description</label>
              <Input value={form.description ?? ""} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            </div>
          </div>
          <label className="flex items-center gap-2 mt-4 cursor-pointer w-fit">
            <input type="checkbox" checked={form.is_active ?? true} onChange={(e) => setForm({ ...form, is_active: e.target.checked })} className="w-4 h-4" />
            <span className="font-medium text-sm">Active (selectable when creating VMs)</span>
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
        <div className="rounded-lg border border-destructive bg-destructive/10 text-destructive p-6 mb-6">
          <p className="font-semibold">Failed to load plans</p>
          <p className="text-sm">{(error as Error).message}</p>
        </div>
      )}

      <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-muted text-muted-foreground font-medium text-xs">
          <div className="col-span-3">Name</div>
          <div className="col-span-2">vCPU</div>
          <div className="col-span-2">RAM</div>
          <div className="col-span-2">Disk</div>
          <div className="col-span-1">Speed / Quota</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {isLoading ? (
          <div className="flex items-center justify-center py-16">
            <Loader2 className="w-8 h-8 animate-spin" />
            <span className="ml-3 font-medium text-muted-foreground">Loading plans...</span>
          </div>
        ) : !plans || plans.length === 0 ? (
          <div className="p-12 text-center">
            <Boxes className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
            <p className="text-muted-foreground font-medium mb-4">No plans yet</p>
            <Button onClick={openCreate} className="gap-2">
              <Plus className="w-4 h-4" />
              Create your first plan
            </Button>
          </div>
        ) : (
          plans.map((plan) => (
            <div key={plan.id} className="grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0 hover:bg-muted/50 transition-colors">
              <div className="col-span-3">
                <div className="flex items-center gap-2">
                  <span className="font-medium truncate">{plan.name}</span>
                  {!plan.is_active && <Badge variant="secondary" className="text-[10px]">Inactive</Badge>}
                </div>
                {plan.description && <p className="text-xs text-muted-foreground truncate">{plan.description}</p>}
              </div>
              <div className="col-span-2 flex items-center gap-1 font-medium"><Cpu className="w-4 h-4 text-muted-foreground" />{plan.cpu}</div>
              <div className="col-span-2 flex items-center gap-1 font-medium"><MemoryStick className="w-4 h-4 text-muted-foreground" />{plan.ram} MB</div>
              <div className="col-span-2 flex items-center gap-1 font-medium"><HardDrive className="w-4 h-4 text-muted-foreground" />{plan.disk} GB</div>
              <div className="col-span-1 font-medium text-sm leading-tight">
                <div className="flex items-center gap-1"><Zap className="w-3 h-3 text-muted-foreground" />{plan.bandwidth_mbps ? `${plan.bandwidth_mbps}` : "∞"}</div>
                <div className="text-[11px] text-muted-foreground">{plan.data_quota_gb ? `${plan.data_quota_gb}GB·${plan.over_quota_policy[0].toUpperCase()}` : "∞ data"}</div>
              </div>
              <div className="col-span-2 flex justify-end gap-2">
                <Button variant="outline" size="sm" className="h-8 w-8 p-0" title="Edit plan" onClick={() => openEdit(plan)}>
                  <Pencil className="w-4 h-4" />
                </Button>
                <Button variant="ghost" size="sm" className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive" title="Delete plan" onClick={() => handleDelete(plan)}>
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
