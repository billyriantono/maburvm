"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { StatusBadge } from "@/components/status-badge"
import { useConfirm } from "@/components/confirm-provider"
import { useCan } from "@/lib/hooks/use-auth"
import {
  useAddFirewallGroupRule,
  useDeleteFirewallGroup,
  useDeleteFirewallGroupRule,
  useFirewallGroup,
  useFirewallGroups,
  useSaveFirewallGroup,
} from "@/lib/hooks/use-firewall-groups"
import { describeRule } from "@/lib/firewall"
import type { CreateFirewallRuleRequest, FirewallAction, FirewallFamily, FirewallGroup } from "@/types"

type RuleDraft = Omit<CreateFirewallRuleRequest, "vm_id">

const EMPTY_RULE: RuleDraft = { protocol: "tcp", port_range: "", action: "allow", direction: "inbound", source_ip: "", priority: 100, icmp_type: "", log: false, family: "" }

function RuleForm({ allowLog, pending, onAdd }: { allowLog: boolean; pending: boolean; onAdd: (r: RuleDraft) => Promise<boolean> }) {
  const [form, setForm] = useState<RuleDraft>(EMPTY_RULE)
  const set = (patch: Partial<RuleDraft>) => setForm((f) => ({ ...f, ...patch }))
  const field = "h-9 border rounded-md bg-background px-2 text-sm"
  return (
    <div className="flex flex-wrap items-end gap-2 p-3 border rounded-md bg-muted">
      <div>
        <Label htmlFor="gr-action" className="text-xs">Action</Label>
        <select id="gr-action" value={form.action} onChange={(e) => set({ action: e.target.value as FirewallAction })} className={field}>
          <option value="allow">Allow</option>
          <option value="deny">Drop</option>
          <option value="reject">Reject</option>
        </select>
      </div>
      <div>
        <Label htmlFor="gr-dir" className="text-xs">Direction</Label>
        <select id="gr-dir" value={form.direction} onChange={(e) => set({ direction: e.target.value as RuleDraft["direction"] })} className={field}>
          <option value="inbound">Inbound</option>
          <option value="outbound">Outbound</option>
        </select>
      </div>
      <div>
        <Label htmlFor="gr-proto" className="text-xs">Protocol</Label>
        <select id="gr-proto" value={form.protocol} onChange={(e) => set({ protocol: e.target.value as RuleDraft["protocol"] })} className={field}>
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
          <option value="icmp">ICMP</option>
          <option value="all">All</option>
        </select>
      </div>
      {(form.protocol === "tcp" || form.protocol === "udp") && (
        <div>
          <Label htmlFor="gr-ports" className="text-xs">Ports</Label>
          <Input id="gr-ports" value={form.port_range} onChange={(e) => set({ port_range: e.target.value })} placeholder="80,443 or 1000-2000" className="w-40 font-mono" />
        </div>
      )}
      {form.protocol === "icmp" && (
        <div>
          <Label htmlFor="gr-icmp" className="text-xs">ICMP type</Label>
          <Input id="gr-icmp" value={form.icmp_type} onChange={(e) => set({ icmp_type: e.target.value })} placeholder="All types" className="w-36 font-mono" />
        </div>
      )}
      <div>
        <Label htmlFor="gr-src" className="text-xs">Remote address</Label>
        <Input id="gr-src" value={form.source_ip} onChange={(e) => set({ source_ip: e.target.value })} placeholder="Anywhere" className="w-40 font-mono" />
      </div>
      <div>
        <Label htmlFor="gr-family" className="text-xs">Addresses</Label>
        <select id="gr-family" value={form.family} onChange={(e) => set({ family: e.target.value as FirewallFamily })} className={field}>
          <option value="">IPv4 and IPv6</option>
          <option value="v4">IPv4 only</option>
          <option value="v6">IPv6 only</option>
        </select>
      </div>
      <div>
        <Label htmlFor="gr-prio" className="text-xs">Order in group</Label>
        <Input id="gr-prio" type="number" min={1} max={1000} value={form.priority} onChange={(e) => set({ priority: Number(e.target.value) })} className="w-24 font-mono" />
      </div>
      {allowLog && (
        <label className="flex items-center gap-2 text-sm h-9">
          <input type="checkbox" checked={!!form.log} onChange={(e) => set({ log: e.target.checked })} />
          Log matches
        </label>
      )}
      <Button size="sm" disabled={pending} onClick={async () => { if (await onAdd(form)) setForm(EMPTY_RULE) }}>
        {pending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}Add rule
      </Button>
    </div>
  )
}

function GroupDetail({ groupId, mode, canManage, onClose }: { groupId: string; mode: "admin" | "client"; canManage: boolean; onClose: () => void }) {
  const confirm = useConfirm()
  const { data: group, isLoading, error, refetch } = useFirewallGroup(groupId)
  const addRule = useAddFirewallGroupRule(groupId)
  const removeRule = useDeleteFirewallGroupRule(groupId)
  const removeGroup = useDeleteFirewallGroup()
  const save = useSaveFirewallGroup()
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")

  if (isLoading) return <div className="border rounded-lg p-6 text-sm text-muted-foreground">Loading the group…</div>
  if (error || !group) {
    return (
      <div className="border rounded-lg p-6">
        <p role="alert" className="text-sm text-destructive">{(error as Error)?.message ?? "The group could not be loaded."}</p>
        <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
      </div>
    )
  }
  const vmHref = (id: string) => (mode === "admin" ? `/vms/${id}` : `/client/vms/${id}`)
  const changes = group.vm_count > 0 ? ` The change reaches its ${group.vm_count} VM${group.vm_count === 1 ? "" : "s"} within a minute.` : ""

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6 space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        {editing ? (
          <div className="flex flex-wrap items-end gap-2">
            <div>
              <Label htmlFor="g-name" className="text-xs">Name</Label>
              <Input id="g-name" value={name} onChange={(e) => setName(e.target.value)} className="w-56" />
            </div>
            <div>
              <Label htmlFor="g-desc" className="text-xs">Description</Label>
              <Input id="g-desc" value={description} onChange={(e) => setDescription(e.target.value)} className="w-72" />
            </div>
            <Button size="sm" disabled={save.isPending} onClick={async () => {
              try { await save.mutateAsync({ id: group.id, name, description }); setEditing(false); toast.success("Group saved") } catch (e) { toast.error((e as Error).message) }
            }}>Save</Button>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>
          </div>
        ) : (
          <div>
            <h2 className="text-lg font-semibold">{group.name}</h2>
            <p className="text-sm text-muted-foreground">
              {group.user_id ? (mode === "admin" ? `Owned by ${group.owner_email ?? "a customer"}` : "Your group") : "For everyone, managed by staff"}
              {group.description ? ` · ${group.description}` : ""}
            </p>
          </div>
        )}
        <div className="flex gap-2">
          {canManage && !editing && <Button size="sm" variant="outline" onClick={() => { setName(group.name); setDescription(group.description); setEditing(true) }}>Rename</Button>}
          {canManage && (
            <Button size="sm" variant="outline" onClick={() => confirm({
              title: `Delete "${group.name}"?`,
              description: "Only possible once no VM has it and no plan gives it to new VMs.",
              confirmLabel: "Delete group",
              destructive: true,
              action: async () => { await removeGroup.mutateAsync(group.id); toast.success("Group deleted"); onClose() },
            })}>Delete group</Button>
          )}
          <Button size="sm" variant="ghost" onClick={onClose}>Close</Button>
        </div>
      </div>

      <section className="space-y-3">
        <h3 className="text-sm font-semibold">Rules</h3>
        <p className="text-xs text-muted-foreground">
          The rules run in their order here, as one block placed before or after each VM&apos;s own rules. They count toward each VM&apos;s rule limit.{changes}
        </p>
        {canManage && (
          <RuleForm allowLog={mode === "admin"} pending={addRule.isPending} onAdd={async (r) => {
            try { await addRule.mutateAsync(r); toast.success("Rule added"); return true } catch (e) { toast.error((e as Error).message); return false }
          }} />
        )}
        {group.rules.length === 0 ? (
          <p className="text-sm text-muted-foreground border rounded-md p-4">No rules yet. A group without rules changes nothing on its VMs.</p>
        ) : (
          <div className="border rounded-md overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted text-xs text-muted-foreground">
                <tr><th className="text-left p-3">Order</th><th className="text-left p-3">Rule</th><th className="text-left p-3">Action</th><th className="p-3"><span className="sr-only">Actions</span></th></tr>
              </thead>
              <tbody>
                {group.rules.map((r) => (
                  <tr key={r.id} className="border-t">
                    <td className="p-3 font-mono">{r.priority}</td>
                    <td className="p-3">{describeRule(r)}{r.log && <span className="block text-xs text-muted-foreground">Logged</span>}</td>
                    <td className="p-3"><StatusBadge variant="lamp" className="text-xs" status={r.action} /></td>
                    <td className="p-3 text-right">
                      {canManage && (
                        <Button size="sm" variant="ghost" onClick={() => confirm({
                          title: "Remove this rule?",
                          description: `${describeRule(r)}.${changes}`,
                          confirmLabel: "Remove",
                          destructive: true,
                          action: () => removeRule.mutateAsync(r.id),
                        })}>Remove</Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">VMs with this group</h3>
        {group.vms.length === 0 ? (
          <p className="text-sm text-muted-foreground">None yet. Attach it from a VM&apos;s Firewall tab{mode === "admin" ? ", or make it a plan's default" : ""}.</p>
        ) : (
          <ul className="text-sm space-y-1">
            {group.vms.map((vm) => (
              <li key={vm.id}>
                <Link href={vmHref(vm.id)} className="underline underline-offset-2">{vm.hostname}</Link>
                <span className="text-muted-foreground"> · {vm.position === "before" ? "before its own rules" : "after its own rules"}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

// FirewallGroupsManager lists and edits firewall groups: staff see every
// group and manage the ones for everyone; a customer manages their own.
export function FirewallGroupsManager({ mode }: { mode: "admin" | "client" }) {
  const can = useCan()
  const canManage = mode === "client" || can("firewall_group:update")
  const { data: groups, isLoading, error, refetch } = useFirewallGroups()
  const save = useSaveFirewallGroup()
  const [selected, setSelected] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")

  const owner = (g: FirewallGroup) => (g.user_id ? g.owner_email ?? "A customer" : "Everyone")

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Firewall groups</h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
            {mode === "admin"
              ? "Named sets of firewall rules attached to many VMs. A group for everyone can be put on any VM or given to new VMs by a plan; customers keep groups of their own."
              : "Named sets of firewall rules you can attach to any of your VMs. Changing a group changes every VM it is on."}
          </p>
        </div>
        {canManage && <Button onClick={() => setCreating(true)}>New group</Button>}
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading groups…</p>
      ) : error ? (
        <div className="border rounded-lg p-6">
          <p role="alert" className="text-sm text-destructive">{(error as Error).message}</p>
          <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : !groups?.length ? (
        <div className="rounded-lg border bg-card p-10 text-center">
          <p className="font-medium">No firewall groups yet</p>
          <p className="text-sm text-muted-foreground mt-1">
            {canManage ? "Create one, add its rules, then attach it to VMs." : "Staff have not created any."}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted text-xs text-muted-foreground">
              <tr>
                <th className="text-left p-3">Name</th>
                {mode === "admin" && <th className="text-left p-3">For</th>}
                <th className="text-left p-3">Rules</th>
                <th className="text-left p-3">VMs</th>
                <th className="p-3"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr key={g.id} className={`border-t ${selected === g.id ? "bg-muted/50" : ""}`}>
                  <td className="p-3 font-medium">{g.name}{g.description && <span className="block text-xs text-muted-foreground font-normal">{g.description}</span>}</td>
                  {mode === "admin" && <td className="p-3">{owner(g)}</td>}
                  <td className="p-3 tabular-nums">{g.rule_count}</td>
                  <td className="p-3 tabular-nums">{g.vm_count}</td>
                  <td className="p-3 text-right"><Button size="sm" variant="outline" onClick={() => setSelected(g.id)}>Open</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {selected && (
        <GroupDetail
          key={selected}
          groupId={selected}
          mode={mode}
          canManage={canManage}
          onClose={() => setSelected(null)}
        />
      )}

      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New firewall group</DialogTitle>
            <DialogDescription>
              {mode === "admin" ? "A group you create here is for everyone: it can go on any VM and be a plan's default." : "Only you can see and use this group."}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="new-group-name">Name</Label>
              <Input id="new-group-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Web servers" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="new-group-desc">Description (optional)</Label>
              <Input id="new-group-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreating(false)}>Cancel</Button>
            <Button disabled={save.isPending || !name.trim()} onClick={async () => {
              try {
                const g = await save.mutateAsync({ name: name.trim(), description })
                toast.success(`"${g.name}" created`)
                setCreating(false); setName(""); setDescription(""); setSelected(g.id)
              } catch (e) { toast.error((e as Error).message) }
            }}>
              {save.isPending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}Create group
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
