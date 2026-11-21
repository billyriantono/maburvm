"use client"

import { FormEvent, useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Meter } from "@/components/ui/meter"
import { useUpdateNode } from "@/lib/hooks/use-nodes"
import type { Node, NodeAllocation as Allocation } from "@/types"

const ratio = (r: number) => `${Number(r.toFixed(2))}x`

function Row({ label, allocated, total, limit, unit, ratioInForce, notMeasured }: {
  label: string; allocated: number; total: number; limit: number; unit: string; ratioInForce: number; notMeasured: string
}) {
  const over = limit > 0 && allocated > limit
  return (
    <div>
      <div className="flex flex-wrap items-baseline justify-between gap-x-4">
        <span className="text-sm font-medium">{label}</span>
        <span className="text-sm">
          {allocated.toLocaleString()} {unit}
          {total > 0 && <> of {total.toLocaleString()} ({ratio(allocated / total)})</>}
        </span>
      </div>
      {limit > 0 && <Meter percent={(allocated / limit) * 100} over={over} />}
      <p className="mt-1 text-xs">
        {total === 0 ? (
          <span className="text-muted-foreground">{notMeasured}</span>
        ) : limit === 0 ? (
          <span className="text-muted-foreground">No limit set.</span>
        ) : over ? (
          <><span className="font-medium text-red-600">Over the limit.</span> <span className="text-muted-foreground">The limit is {limit.toLocaleString()} {unit} ({ratio(ratioInForce)}). New VMs are refused here.</span></>
        ) : (
          <span className="text-muted-foreground">Limit {limit.toLocaleString()} {unit} ({ratio(ratioInForce)}). {(limit - allocated).toLocaleString()} {unit} left to allocate.</span>
        )}
      </p>
    </div>
  )
}

// NodeAllocationCard says how many vCPUs and how much RAM the node has
// promised to VMs against what it has, and sets the node's own vCPU ratio.
// Placement only puts a new VM on a node where it fits under these limits.
export function NodeAllocationCard({ node }: { node: Node }) {
  const a: Allocation | undefined = node.allocation
  const updateNode = useUpdateNode(node.id)
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState("")
  if (!a) return null

  const save = async (e: FormEvent) => {
    e.preventDefault()
    const r = value.trim() === "" ? 0 : Number(value)
    if (!Number.isFinite(r) || r < 0 || r > 100) {
      toast.error("The ratio is a number from 0 to 100")
      return
    }
    try {
      await updateNode.mutateAsync({ vcpu_ratio: r })
      toast.success(r > 0 ? `This node now carries at most ${ratio(r)} vCPUs per thread` : "This node uses the panel-wide vCPU ratio again")
      setEditing(false)
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <h2 className="mb-1 text-lg font-semibold text-foreground">Allocation</h2>
      <p className="mb-4 text-sm text-muted-foreground">
        What the VMs here have been given, against what the node has. New VMs only go to a node where they fit under its limits; disk is under Storage pools.
      </p>
      <div className="space-y-4">
        <Row label="vCPUs" unit="vCPUs" allocated={a.vcpu_allocated} total={a.cpu_count} limit={a.vcpu_limit} ratioInForce={a.vcpu_ratio}
          notMeasured="The node has not reported its CPU threads yet, so no limit applies." />
        <Row label="Memory" unit="MB" allocated={a.ram_allocated_mb} total={a.ram_total_mb} limit={a.ram_limit_mb} ratioInForce={a.ram_ratio}
          notMeasured="The node has not reported its memory yet, so no limit applies." />
      </div>
      <div className="mt-4 border-t pt-4 text-sm">
        {editing ? (
          <form onSubmit={save} className="flex flex-wrap items-end gap-3">
            <div>
              <label htmlFor="node-vcpu-ratio" className="mb-1 block text-xs font-medium text-muted-foreground">vCPU ratio for this node</label>
              <Input id="node-vcpu-ratio" type="number" min={0} max={100} step={0.5} value={value} onChange={(e) => setValue(e.target.value)} placeholder="Panel-wide" className="w-40" />
            </div>
            <Button type="submit" size="sm" disabled={updateNode.isPending}>
              {updateNode.isPending && <Loader2 className="h-4 w-4 animate-spin" />}Save
            </Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>
            <p className="basis-full text-xs text-muted-foreground">Empty or 0 uses the panel-wide ratio. Set it above what the node already carries, or new orders here are refused.</p>
          </form>
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-muted-foreground">
              {a.vcpu_ratio_node > 0
                ? `This node has its own vCPU ratio, ${ratio(a.vcpu_ratio_node)}.`
                : <>vCPU and RAM ratios come from <Link href="/settings/system" className="text-foreground underline underline-offset-2">System settings, Quotas</Link>.</>}
            </p>
            <Button size="sm" variant="outline" onClick={() => { setValue(a.vcpu_ratio_node > 0 ? String(a.vcpu_ratio_node) : ""); setEditing(true) }}>
              Set this node&apos;s vCPU ratio
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
