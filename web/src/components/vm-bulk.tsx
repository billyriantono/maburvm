"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useCan } from "@/lib/hooks/use-auth"
import { useBulkVMs, type BulkRequest, type BulkResult } from "@/lib/hooks/use-admin-extras"
import { useFirewallGroups } from "@/lib/hooks/use-firewall-groups"
import { useNodes } from "@/lib/hooks/use-nodes"
import { usePlans } from "@/lib/hooks/use-plans"

// The actions the VM list takes on several VMs, the permissions each needs
// (the panel checks them again), and the word for the button that does it.
export const BULK_ACTIONS: { id: string; label: string; verb: string; perms: string[] }[] = [
  { id: "start", label: "Start", verb: "Start", perms: ["vm:power"] },
  { id: "stop", label: "Shut down", verb: "Shut down", perms: ["vm:power"] },
  { id: "restart", label: "Restart", verb: "Restart", perms: ["vm:power"] },
  { id: "suspend", label: "Suspend", verb: "Suspend", perms: ["vm:lifecycle"] },
  { id: "unsuspend", label: "Unsuspend", verb: "Unsuspend", perms: ["vm:lifecycle"] },
  { id: "lock", label: "Lock against deletion", verb: "Lock", perms: ["vm:update"] },
  { id: "unlock", label: "Unlock", verb: "Unlock", perms: ["vm:update"] },
  { id: "apply_plan", label: "Move to a plan", verb: "Apply the plan", perms: ["vm:update", "billing:update"] },
  { id: "add_firewall_group", label: "Add a firewall group", verb: "Add the group", perms: ["vm:update", "firewall_group:read"] },
  { id: "migrate", label: "Move to another node", verb: "Queue the moves", perms: ["vm:migrate"] },
]

const selectClass =
  "h-10 w-full rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"

function Results({ rows, yes, no }: { rows: BulkResult[]; yes: string; no: string }) {
  return (
    <ul className="max-h-80 divide-y overflow-y-auto rounded-md border text-sm">
      {rows.map((r) => (
        <li key={r.vm_id} className="flex flex-wrap items-start gap-x-3 gap-y-1 px-3 py-2">
          <span className="inline-flex w-24 shrink-0 items-center gap-2 font-medium">
            <span className={`h-2 w-2 rounded-full ${r.ok ? "bg-success" : "bg-muted-foreground"}`} aria-hidden="true" />
            {r.ok ? yes : no}
          </span>
          <span className="min-w-0 flex-1 basis-56">
            <span className="font-medium [font-stretch:112%]">{r.hostname || r.vm_id}</span>
            <span className="block break-words text-muted-foreground">{r.message}</span>
            {r.changes && r.changes.length > 0 && <span className="block text-xs text-muted-foreground">{r.changes.join("; ")}</span>}
          </span>
        </li>
      ))}
    </ul>
  )
}

/**
 * The bar above the VM list while VMs are ticked: choose what to do, see what
 * it does to each VM, then do it and see what happened to each.
 */
export function BulkBar({ ids, onClear }: { ids: string[]; onClear: () => void }) {
  const allow = useCan()
  const actions = BULK_ACTIONS.filter((a) => a.perms.every((p) => allow(p)))
  const [action, setAction] = useState("")
  const [planId, setPlanId] = useState("")
  const [groupId, setGroupId] = useState("")
  const [nodeId, setNodeId] = useState("")
  const [preview, setPreview] = useState<BulkResult[] | null>(null)
  const [done, setDone] = useState<{ rows: BulkResult[]; ok: number } | null>(null)
  const bulk = useBulkVMs()
  const { data: plans } = usePlans()
  const { data: groups } = useFirewallGroups("global", action === "add_firewall_group")
  const { data: nodes } = useNodes(action === "migrate")
  const chosen = BULK_ACTIONS.find((a) => a.id === action)

  if (actions.length === 0) return null

  const body = (previewOnly: boolean): BulkRequest => ({
    action, vm_ids: ids, preview: previewOnly,
    ...(action === "apply_plan" ? { plan_id: planId } : {}),
    ...(action === "add_firewall_group" ? { group_id: groupId } : {}),
    ...(action === "migrate" && nodeId ? { node_id: nodeId } : {}),
  })
  const missing = (action === "apply_plan" && !planId) || (action === "add_firewall_group" && !groupId)

  const runPreview = async () => {
    try {
      setPreview((await bulk.mutateAsync(body(true))).results)
    } catch (err) {
      toast.error(`Could not check: ${(err as Error).message}`)
    }
  }
  const apply = async () => {
    try {
      const out = await bulk.mutateAsync(body(false))
      setPreview(null)
      setDone({ rows: out.results, ok: out.ok })
    } catch (err) {
      toast.error(`Not done: ${(err as Error).message}`)
    }
  }
  const willDo = preview?.filter((r) => r.ok).length ?? 0

  return (
    <div role="region" aria-label="Act on the chosen VMs" className="mb-4 flex flex-wrap items-end gap-3 rounded-lg border bg-card p-4">
      <p className="w-full text-sm font-medium">
        {ids.length} {ids.length === 1 ? "VM" : "VMs"} chosen{" "}
        <button type="button" onClick={onClear} className="ml-2 font-normal text-muted-foreground underline underline-offset-2">Clear</button>
      </p>
      <div className="min-w-[12rem] flex-1">
        <label htmlFor="bulk-action" className="mb-1 block text-xs font-medium text-muted-foreground">Action</label>
        <select id="bulk-action" value={action} onChange={(e) => setAction(e.target.value)} className={selectClass}>
          <option value="">Choose an action</option>
          {actions.map((a) => <option key={a.id} value={a.id}>{a.label}</option>)}
        </select>
      </div>
      {action === "apply_plan" && (
        <div className="min-w-[12rem] flex-1">
          <label htmlFor="bulk-plan" className="mb-1 block text-xs font-medium text-muted-foreground">Plan</label>
          <select id="bulk-plan" value={planId} onChange={(e) => setPlanId(e.target.value)} className={selectClass}>
            <option value="">Choose a plan</option>
            {(plans ?? []).map((p) => <option key={p.id} value={p.id}>{p.name} ({p.cpu} vCPU, {p.ram} MB, {p.disk} GB)</option>)}
          </select>
        </div>
      )}
      {action === "add_firewall_group" && (
        <div className="min-w-[12rem] flex-1">
          <label htmlFor="bulk-group" className="mb-1 block text-xs font-medium text-muted-foreground">Firewall group</label>
          <select id="bulk-group" value={groupId} onChange={(e) => setGroupId(e.target.value)} className={selectClass}>
            <option value="">Choose a group</option>
            {(groups ?? []).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </select>
        </div>
      )}
      {action === "migrate" && (
        <div className="min-w-[12rem] flex-1">
          <label htmlFor="bulk-node" className="mb-1 block text-xs font-medium text-muted-foreground">To node</label>
          <select id="bulk-node" value={nodeId} onChange={(e) => setNodeId(e.target.value)} className={selectClass}>
            <option value="">The best node for each VM</option>
            {(nodes ?? []).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </select>
        </div>
      )}
      <Button variant="outline" disabled={!action || missing || bulk.isPending} onClick={runPreview}>
        {bulk.isPending && !preview && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
        Check what happens
      </Button>

      <Dialog open={!!preview} onOpenChange={(o) => !o && setPreview(null)}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{chosen?.label}: {willDo} of {preview?.length ?? 0} VMs</DialogTitle>
            <DialogDescription>
              Nothing has changed yet. VMs marked Skipped are left as they are.
              {action === "migrate" && " Moves wait their turn in the job queue and run a few at a time."}
            </DialogDescription>
          </DialogHeader>
          {preview && <Results rows={preview} yes="Applies" no="Skipped" />}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPreview(null)}>Cancel</Button>
            <Button disabled={willDo === 0 || bulk.isPending} variant={action === "suspend" ? "destructive" : "default"} onClick={apply}>
              {bulk.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              {chosen?.verb} {willDo} {willDo === 1 ? "VM" : "VMs"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!done} onOpenChange={(o) => { if (!o) { setDone(null); onClear() } }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{chosen?.label}: done for {done?.ok ?? 0} of {done?.rows.length ?? 0} VMs</DialogTitle>
            <DialogDescription>What happened to each VM. Power changes and moves run as jobs; follow them on each VM or in the job queue.</DialogDescription>
          </DialogHeader>
          {done && <Results rows={done.rows} yes="Done" no="Not done" />}
          <DialogFooter>
            <Button onClick={() => { setDone(null); onClear() }}>Close</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
