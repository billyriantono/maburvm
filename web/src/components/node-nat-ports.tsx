"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCan } from "@/lib/hooks/use-auth"
import { useNodeNAT, useSetNodeNAT } from "@/lib/hooks/use-firewall-groups"

// NodeNATPorts shows and changes the node's range of public ports that NAT
// VMs take their blocks from, and the address those ports answer on.
export function NodeNATPorts({ nodeId }: { nodeId: string }) {
  const can = useCan()
  const { data, isLoading, error, refetch } = useNodeNAT(nodeId)
  const save = useSetNodeNAT(nodeId)
  const [start, setStart] = useState<string | null>(null)
  const [end, setEnd] = useState<string | null>(null)

  const startValue = start ?? String(data?.port_start ?? "")
  const endValue = end ?? String(data?.port_end ?? "")
  const changed = data && (startValue !== String(data.port_start) || endValue !== String(data.port_end))
  const offered = data && data.port_end > 0
  const size = data && offered ? data.port_end - data.port_start + 1 : 0

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <h2 className="text-lg font-semibold text-foreground">NAT ports</h2>
      <p className="mb-4 mt-1 text-sm text-muted-foreground">
        Each VM on a NAT plan gets a block of public ports from this range on the node&apos;s publish address. Ports already
        forwarded and the agent, libvirt and console ports are skipped; choose a range clear of anything else the node
        listens on, such as SSH on another port. 0 and 0 offers no NAT ports here.
      </p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : error || !data ? (
        <div>
          <p role="alert" className="text-sm text-destructive">{(error as Error)?.message ?? "Could not load the NAT range."}</p>
          <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : (
        <div className="space-y-4">
          <dl className="grid gap-4 sm:grid-cols-3 text-sm">
            <div>
              <dt className="text-xs text-muted-foreground">Publish address</dt>
              <dd className="mt-1 font-mono">{data.address || "Not reported by the agent"}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">Range</dt>
              <dd className="mt-1 tabular-nums">{offered ? `${data.port_start}–${data.port_end} (${size} ports)` : "None"}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">In use</dt>
              <dd className="mt-1 tabular-nums">{data.ports_used} ports in {data.blocks_used} blocks</dd>
            </div>
          </dl>
          {can("node:update") && (
            <div className="flex flex-wrap items-end gap-2">
              <div>
                <label htmlFor="nat-start" className="mb-1 block text-xs font-medium text-muted-foreground">First port</label>
                <Input id="nat-start" inputMode="numeric" value={startValue} onChange={(e) => setStart(e.target.value)} className="w-28 font-mono" />
              </div>
              <div>
                <label htmlFor="nat-end" className="mb-1 block text-xs font-medium text-muted-foreground">Last port</label>
                <Input id="nat-end" inputMode="numeric" value={endValue} onChange={(e) => setEnd(e.target.value)} className="w-28 font-mono" />
              </div>
              <Button size="sm" disabled={!changed || save.isPending} onClick={async () => {
                try {
                  await save.mutateAsync({ port_start: Number(startValue), port_end: Number(endValue) })
                  setStart(null); setEnd(null)
                  toast.success("NAT range saved")
                } catch (e) { toast.error((e as Error).message) }
              }}>
                {save.isPending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}Save range
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
