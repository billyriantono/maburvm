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
import type { AddonKey, CreatePlanRequest, Plan } from "@/types/plan"
import { useConfirm } from "@/components/confirm-provider"
import { useCan } from "@/lib/hooks/use-auth"
import { LimitsFields, NO_LIMITS, limitsProblem, limitsSummary } from "@/components/vm-limits"
import { PlanRolloutDialog } from "@/components/plan-rollout"
import { planAllowances, planPrice } from "@/lib/plans"
import { useFirewallGroups } from "@/lib/hooks/use-firewall-groups"
import { useNodeGroups } from "@/lib/hooks/use-admin-extras"
import { useStorageTiers } from "@/lib/hooks/use-storage"

// An empty number field is "not set" (no price, no limit), never 0.
const numOrNull = (v: string) => (v.trim() === "" ? null : Number(v))
const fieldValue = (v: number | null | undefined) => (v == null ? "" : v)

const emptyForm: CreatePlanRequest = {
  name: "",
  cpu: 1,
  ram: 1024,
  disk: 20,
  bandwidth_mbps: 0,
  data_quota_gb: 0,
  over_quota_policy: "throttle",
  throttle_speed_mbps: 0,
  smtp_policy: "allowed",
  limits: NO_LIMITS,
  price_monthly: null,
  price_hourly: null,
  currency: "",
  max_snapshots: null,
  max_backups: null,
  max_extra_ipv4: null,
  max_extra_ipv6: null,
  max_extra_disks: null,
  max_images: null,
  max_floating_ips: null,
  addon_prices: {},
  default_firewall_group_id: "",
  network_type: "public",
  nat_ports: 20,
  nat_hostnames: 0,
  node_group: "",
  storage_tier: "",
  description: "",
  is_active: true,
}

const ADDONS: { key: AddonKey; label: string }[] = [
  { key: "snapshot", label: "Snapshot" },
  { key: "backup", label: "Backup" },
  { key: "image", label: "Saved image" },
  { key: "extra_disk_gb", label: "Extra disk, per GB" },
  { key: "floating_ip", label: "Floating IP" },
  { key: "extra_ipv4", label: "Extra IPv4 address" },
  { key: "extra_ipv6", label: "IPv6" },
]

export default function PlansPage() {
  const confirm = useConfirm()
  const can = useCan()
  const mayEdit = can("plan:update")
  // Applying a plan changes customers' VMs, so it also takes the right to change VMs.
  const mayApply = mayEdit && can("vm:update")
  const [rolloutPlan, setRolloutPlan] = useState<Plan | null>(null)
  const { data: plans, isLoading, error } = usePlans()
  const createPlan = useCreatePlan()
  const updatePlan = useUpdatePlan()
  const deletePlan = useDeletePlan()
  const { data: globalGroups } = useFirewallGroups("global", mayEdit && can("firewall_group:read"))
  const { data: nodeGroups } = useNodeGroups(mayEdit && can("node:read"))
  const { data: storageTiers } = useStorageTiers(mayEdit && can("node:read"))

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
      smtp_policy: plan.smtp_policy ?? "allowed",
      limits: { ...NO_LIMITS, ...plan.limits },
      price_monthly: plan.price_monthly ?? null,
      price_hourly: plan.price_hourly ?? null,
      currency: plan.currency ?? "",
      max_snapshots: plan.max_snapshots ?? null,
      max_backups: plan.max_backups ?? null,
      max_extra_ipv4: plan.max_extra_ipv4 ?? null,
      max_extra_ipv6: plan.max_extra_ipv6 ?? null,
      max_extra_disks: plan.max_extra_disks ?? null,
      max_images: plan.max_images ?? null,
      max_floating_ips: plan.max_floating_ips ?? null,
      addon_prices: { ...plan.addon_prices },
      default_firewall_group_id: plan.default_firewall_group_id ?? "",
      network_type: plan.network_type ?? "public",
      nat_ports: plan.nat_ports || 20,
      nat_hostnames: plan.nat_hostnames ?? 0,
      node_group: plan.node_group ?? "",
      storage_tier: plan.storage_tier ?? "",
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
        toast.success(`Plan "${form.name}" updated`, mayApply ? { description: "VMs already on it keep their size, limits and speed until you use Apply to VMs." } : undefined)
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
        "New orders can no longer choose it. VMs already on this plan keep the resources they have, and its snapshot, backup and address allowances still apply to them.",
      confirmLabel: "Delete plan",
      destructive: true,
      action: () => deletePlan.mutateAsync(plan.id),
    })
    if (!ok) return
    toast.success(`Plan "${plan.name}" deleted`)
  }

  const saving = createPlan.isPending || updatePlan.isPending
  const limitsError = limitsProblem(form.limits ?? NO_LIMITS)
  const priced = form.price_monthly != null || form.price_hourly != null || Object.keys(form.addon_prices ?? {}).length > 0
  const priceError = priced && !/^[A-Z]{3}$/.test(form.currency ?? "") ? "A price needs a three-letter currency code." : null

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
        {mayEdit && (
          <Button className="gap-2" onClick={openCreate}>
            <Plus className="w-4 h-4" />
            Create Plan
          </Button>
        )}
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
              <label htmlFor="plan-smtp" className="block text-xs font-medium text-muted-foreground mb-1">Outbound mail (port 25)</label>
              <select
                id="plan-smtp"
                value={form.smtp_policy ?? "allowed"}
                onChange={(e) => setForm({ ...form, smtp_policy: e.target.value as CreatePlanRequest["smtp_policy"] })}
                className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              >
                <option value="allowed">Allowed</option>
                <option value="blocked">Blocked</option>
              </select>
              <p className="text-[10px] text-muted-foreground mt-1">For VMs created on this plan from now on. Apply to VMs blocks it on existing ones, but never unblocks a VM; an administrator can change it per VM.</p>
            </div>
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">Description</label>
              <Input value={form.description ?? ""} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            </div>
          </div>
          <div className="mt-5 border-t pt-4">
            <h3 className="text-sm font-medium">Price shown to customers</h3>
            <p className="mb-3 text-xs text-muted-foreground">
              Shown on the order page and in plan lists. Charging happens in your billing system; the panel only displays it. Leave the prices empty to show none.
            </p>
            <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
              <div>
                <label htmlFor="plan-price-monthly" className="mb-1 block text-xs font-medium text-muted-foreground">Monthly price</label>
                <Input id="plan-price-monthly" type="number" min={0} step="0.01" value={fieldValue(form.price_monthly)} onChange={(e) => setForm({ ...form, price_monthly: numOrNull(e.target.value) })} />
              </div>
              <div>
                <label htmlFor="plan-price-hourly" className="mb-1 block text-xs font-medium text-muted-foreground">Hourly price (optional)</label>
                <Input id="plan-price-hourly" type="number" min={0} step="0.0001" value={fieldValue(form.price_hourly)} onChange={(e) => setForm({ ...form, price_hourly: numOrNull(e.target.value) })} />
              </div>
              <div>
                <label htmlFor="plan-currency" className="mb-1 block text-xs font-medium text-muted-foreground">Currency</label>
                <Input id="plan-currency" maxLength={3} placeholder="USD" value={form.currency ?? ""} onChange={(e) => setForm({ ...form, currency: e.target.value.toUpperCase() })} />
                <p className="mt-1 text-[10px] text-muted-foreground">Three-letter code, such as USD, EUR or IDR.</p>
              </div>
            </div>
            {priceError && <p role="alert" className="mt-3 text-sm text-destructive">{priceError}</p>}
          </div>
          <div className="mt-5 border-t pt-4">
            <h3 className="text-sm font-medium">What each VM may keep</h3>
            <p className="mb-3 text-xs text-muted-foreground">
              Empty means no limit, 0 means none. These apply to every VM on the plan as soon as you save, and refuse one more once a VM has the number. Nothing already taken is removed, with one exception: scheduled backups over the number are removed oldest first at the next hourly pass. Manual backups are never removed.
            </p>
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
              <div>
                <label htmlFor="plan-max-snapshots" className="mb-1 block text-xs font-medium text-muted-foreground">Snapshots (at most 10)</label>
                <Input id="plan-max-snapshots" type="number" min={0} max={10} placeholder="No limit" value={fieldValue(form.max_snapshots)} onChange={(e) => setForm({ ...form, max_snapshots: numOrNull(e.target.value) })} />
              </div>
              <div>
                <label htmlFor="plan-max-backups" className="mb-1 block text-xs font-medium text-muted-foreground">Backups, scheduled and manual</label>
                <Input id="plan-max-backups" type="number" min={0} max={1000} placeholder="No limit" value={fieldValue(form.max_backups)} onChange={(e) => setForm({ ...form, max_backups: numOrNull(e.target.value) })} />
              </div>
              <div>
                <label htmlFor="plan-max-ipv4" className="mb-1 block text-xs font-medium text-muted-foreground">Extra IPv4 addresses</label>
                <Input id="plan-max-ipv4" type="number" min={0} max={256} placeholder="No limit" value={fieldValue(form.max_extra_ipv4)} onChange={(e) => setForm({ ...form, max_extra_ipv4: numOrNull(e.target.value) })} />
                <p className="mt-1 text-[10px] text-muted-foreground">Beyond the VM&apos;s first address. Floating IPs are not counted.</p>
              </div>
              <div>
                <label htmlFor="plan-max-ipv6" className="mb-1 block text-xs font-medium text-muted-foreground">IPv6 added later</label>
                <select
                  id="plan-max-ipv6"
                  value={form.max_extra_ipv6 === 0 ? "none" : "allowed"}
                  onChange={(e) => setForm({ ...form, max_extra_ipv6: e.target.value === "none" ? 0 : null })}
                  className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                >
                  <option value="allowed">Allowed</option>
                  <option value="none">Not included</option>
                </select>
                <p className="mt-1 text-[10px] text-muted-foreground">A VM holds one IPv6 at most.</p>
              </div>
              <div>
                <label htmlFor="plan-max-disks" className="mb-1 block text-xs font-medium text-muted-foreground">Extra disks (at most 16)</label>
                <Input id="plan-max-disks" type="number" min={0} max={16} placeholder="No limit" value={fieldValue(form.max_extra_disks)} onChange={(e) => setForm({ ...form, max_extra_disks: numOrNull(e.target.value) })} />
              </div>
              <div>
                <label htmlFor="plan-max-images" className="mb-1 block text-xs font-medium text-muted-foreground">Saved images, per VM</label>
                <Input id="plan-max-images" type="number" min={0} max={1000} placeholder="No limit" value={fieldValue(form.max_images)} onChange={(e) => setForm({ ...form, max_images: numOrNull(e.target.value) })} />
              </div>
              <div>
                <label htmlFor="plan-max-fips" className="mb-1 block text-xs font-medium text-muted-foreground">Floating IPs, per VM</label>
                <Input id="plan-max-fips" type="number" min={0} max={256} placeholder="No limit" value={fieldValue(form.max_floating_ips)} onChange={(e) => setForm({ ...form, max_floating_ips: numOrNull(e.target.value) })} />
              </div>
            </div>
            <p className="mt-2 text-[10px] text-muted-foreground">
              Images and floating IPs belong to the account, not one VM: each VM on this plan adds its figure to the account&apos;s total. An account with any VM on no plan, or on a plan with no limit, is not limited.
            </p>
          </div>
          <div className="mt-5 border-t pt-4">
            <h3 className="text-sm font-medium">Prices of extras shown to customers</h3>
            <p className="mb-3 text-xs text-muted-foreground">Monthly, in the plan&apos;s currency, shown beside the plan on the order page. Empty shows none.</p>
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
              {ADDONS.map((a) => (
                <div key={a.key}>
                  <label htmlFor={`plan-addon-${a.key}`} className="mb-1 block text-xs font-medium text-muted-foreground">{a.label}</label>
                  <Input id={`plan-addon-${a.key}`} type="number" min={0} step="0.01" value={fieldValue(form.addon_prices?.[a.key] ?? null)}
                    onChange={(e) => {
                      const v = numOrNull(e.target.value)
                      const next = { ...form.addon_prices }
                      if (v == null) delete next[a.key]
                      else next[a.key] = v
                      setForm({ ...form, addon_prices: next })
                    }} />
                </div>
              ))}
            </div>
          </div>
          <div className="mt-5 border-t pt-4">
            <h3 className="text-sm font-medium">Network and firewall</h3>
            <p className="mb-3 text-xs text-muted-foreground">
              For VMs created on this plan from now on; existing VMs keep their address and firewall.
            </p>
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
              <div>
                <label htmlFor="plan-network" className="mb-1 block text-xs font-medium text-muted-foreground">Address</label>
                <select
                  id="plan-network"
                  value={form.network_type ?? "public"}
                  onChange={(e) => setForm({ ...form, network_type: e.target.value as CreatePlanRequest["network_type"] })}
                  className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                >
                  <option value="public">A public address of its own</option>
                  <option value="nat">NAT: a private address and public ports</option>
                </select>
                {form.network_type === "nat" && (
                  <p className="mt-1 text-[10px] text-muted-foreground">Needs a NAT pool on the node (IP Pools) and free ports in the node&apos;s NAT range (on the node&apos;s page).</p>
                )}
              </div>
              {form.network_type === "nat" && (<>
                <div>
                  <label htmlFor="plan-nat-ports" className="mb-1 block text-xs font-medium text-muted-foreground">Public ports per VM</label>
                  <Input id="plan-nat-ports" type="number" min={1} max={1000} value={form.nat_ports ?? 20} onChange={(e) => setForm({ ...form, nat_ports: Number(e.target.value) })} />
                  <p className="mt-1 text-[10px] text-muted-foreground">One block in a row; the first goes to SSH, or Remote Desktop on Windows.</p>
                </div>
                <div>
                  <label htmlFor="plan-nat-hosts" className="mb-1 block text-xs font-medium text-muted-foreground">Hostnames per VM</label>
                  <Input id="plan-nat-hosts" type="number" min={0} max={100} value={form.nat_hostnames ?? 0} onChange={(e) => setForm({ ...form, nat_hostnames: Number(e.target.value) })} />
                  <p className="mt-1 text-[10px] text-muted-foreground">Websites routed to the VM on the node&apos;s ports 80 and 443. 0 = none.</p>
                </div>
              </>)}
              <div>
                <label htmlFor="plan-fw-group" className="mb-1 block text-xs font-medium text-muted-foreground">Firewall for new VMs</label>
                <select
                  id="plan-fw-group"
                  value={form.default_firewall_group_id ?? ""}
                  onChange={(e) => setForm({ ...form, default_firewall_group_id: e.target.value })}
                  className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                >
                  <option value="">Built-in rules (SSH or Remote Desktop, ping, web)</option>
                  {form.default_firewall_group_id && !globalGroups?.some((g) => g.id === form.default_firewall_group_id) && (
                    <option value={form.default_firewall_group_id}>The group set now</option>
                  )}
                  {(globalGroups ?? []).map((g) => <option key={g.id} value={g.id}>Group: {g.name} ({g.rule_count} rules)</option>)}
                </select>
                <p className="mt-1 text-[10px] text-muted-foreground">A group is placed after the VM&apos;s own rules, so customers can still add their own. Groups are under Firewall groups.</p>
              </div>
              <div>
                <label htmlFor="plan-node-group" className="mb-1 block text-xs font-medium text-muted-foreground">Hypervisor group</label>
                <select
                  id="plan-node-group"
                  value={form.node_group ?? ""}
                  onChange={(e) => setForm({ ...form, node_group: e.target.value })}
                  className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                >
                  <option value="">Any node</option>
                  {form.node_group && !nodeGroups?.some((g) => g.name === form.node_group) && <option value={form.node_group}>{form.node_group}</option>}
                  {(nodeGroups ?? []).map((g) => <option key={g.name} value={g.name}>{g.name} ({g.nodes.length} {g.nodes.length === 1 ? "node" : "nodes"})</option>)}
                </select>
                <p className="mt-1 text-[10px] text-muted-foreground">New VMs on the plan go only to nodes in the group. A node&apos;s group is set on its page.</p>
              </div>
              <div>
                <label htmlFor="plan-storage-tier" className="mb-1 block text-xs font-medium text-muted-foreground">Storage tier</label>
                <select
                  id="plan-storage-tier"
                  value={form.storage_tier ?? ""}
                  onChange={(e) => setForm({ ...form, storage_tier: e.target.value })}
                  className="w-full h-10 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                >
                  <option value="">Each node&apos;s primary pool</option>
                  {form.storage_tier && !storageTiers?.includes(form.storage_tier) && <option value={form.storage_tier}>{form.storage_tier}</option>}
                  {(storageTiers ?? []).map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
                <p className="mt-1 text-[10px] text-muted-foreground">New VMs&apos; disks go only into pools of the tier, on nodes that have one with room. A pool&apos;s tier is set under Storage.</p>
              </div>
            </div>
          </div>
          <div className="mt-5 border-t pt-4">
            <h3 className="text-sm font-medium">CPU and disk limits</h3>
            <p className="mb-3 text-xs text-muted-foreground">
              What each VM on this plan may take from its node, so one busy VM cannot slow down the others. New VMs, and VMs moved to this plan, get these. Changing them leaves existing VMs as they are until you use Apply to VMs; an administrator can also change a VM&apos;s limits on its page.
            </p>
            <LimitsFields idPrefix="plan-limits" value={form.limits ?? NO_LIMITS} onChange={(limits) => setForm({ ...form, limits })} />
            {limitsError && <p role="alert" className="mt-3 text-sm text-destructive">{limitsError}</p>}
          </div>
          <label className="flex items-center gap-2 mt-4 cursor-pointer w-fit">
            <input type="checkbox" checked={form.is_active ?? true} onChange={(e) => setForm({ ...form, is_active: e.target.checked })} className="w-4 h-4" />
            <span className="font-medium text-sm">Active (selectable when creating VMs)</span>
          </label>
          <div className="flex justify-end gap-3 mt-4">
            <Button type="button" variant="ghost" onClick={() => { setShowForm(false); setEditingId(null) }}>Cancel</Button>
            <Button type="submit" disabled={saving || !!limitsError || !!priceError}>
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
            {mayEdit && (
              <Button onClick={openCreate} className="gap-2">
                <Plus className="w-4 h-4" />
                Create your first plan
              </Button>
            )}
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
                <p className="text-xs text-muted-foreground">{planPrice(plan) ?? "No price shown"}</p>
              </div>
              <div className="col-span-2 flex items-center gap-1 font-medium"><Cpu className="w-4 h-4 text-muted-foreground" />{plan.cpu}</div>
              <div className="col-span-2 flex items-center gap-1 font-medium"><MemoryStick className="w-4 h-4 text-muted-foreground" />{plan.ram} MB</div>
              <div className="col-span-2 flex items-center gap-1 font-medium"><HardDrive className="w-4 h-4 text-muted-foreground" />{plan.disk} GB</div>
              <div className="col-span-1 font-medium text-sm leading-tight">
                <div className="flex items-center gap-1"><Zap className="w-3 h-3 text-muted-foreground" />{plan.bandwidth_mbps ? `${plan.bandwidth_mbps}` : "∞"}</div>
                <div className="text-[11px] text-muted-foreground">{plan.data_quota_gb ? `${plan.data_quota_gb}GB·${plan.over_quota_policy[0].toUpperCase()}` : "∞ data"}</div>
                {plan.smtp_policy === "blocked" && <div className="text-[11px] text-muted-foreground">Port 25 blocked</div>}
                {plan.default_firewall_group_id && <div className="text-[11px] text-muted-foreground">Firewall: {globalGroups?.find((g) => g.id === plan.default_firewall_group_id)?.name ?? "a group"}</div>}
                {plan.node_group && <div className="text-[11px] text-muted-foreground">Nodes: {plan.node_group}</div>}
                {plan.storage_tier && <div className="text-[11px] text-muted-foreground">Storage: {plan.storage_tier}</div>}
                {limitsSummary(plan.limits).map((line) => <div key={line} className="text-[11px] text-muted-foreground">{line}</div>)}
                {planAllowances(plan).map((line) => <div key={line} className="text-[11px] text-muted-foreground">{line}</div>)}
              </div>
              <div className="col-span-2 flex justify-end gap-2">
                {mayApply && (
                  <Button variant="outline" size="sm" className="h-8" onClick={() => setRolloutPlan(plan)}>
                    Apply to VMs
                  </Button>
                )}
                {mayEdit && (<>
                <Button variant="outline" size="sm" className="h-8 w-8 p-0" title="Edit plan" onClick={() => openEdit(plan)}>
                  <Pencil className="w-4 h-4" />
                </Button>
                <Button variant="ghost" size="sm" className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive" title="Delete plan" onClick={() => handleDelete(plan)}>
                  <Trash2 className="w-4 h-4" />
                </Button>
                </>)}
              </div>
            </div>
          ))
        )}
      </div>
      <PlanRolloutDialog plan={rolloutPlan} onClose={() => setRolloutPlan(null)} />
    </div>
  )
}
