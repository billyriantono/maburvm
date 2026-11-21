"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCan } from "@/lib/hooks/use-auth"
import { useNodeCPUPool, useSetNodeCPUPool } from "@/lib/hooks/use-nodes"

// NodeCPUPool shows and changes the host cores dedicated-CPU VMs on the node
// take theirs from, and which VMs hold which.
export function NodeCPUPool({ nodeId }: { nodeId: string }) {
  const can = useCan()
  const { data, isLoading, error, refetch } = useNodeCPUPool(nodeId)
  const save = useSetNodeCPUPool(nodeId)
  const [draft, setDraft] = useState<string | null>(null)
  const value = draft ?? data?.pool ?? ""

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <h2 className="text-lg font-semibold text-foreground">Dedicated cores</h2>
      <p className="mb-4 mt-1 text-sm text-muted-foreground">
        Host cores that VMs with dedicated CPUs take one each per vCPU, as a CPU list such as 4-15. Keep the first cores out
        for the host itself. The node&apos;s other VMs still run on any core: isolate these from them on the host
        (for example with the kernel&apos;s isolcpus option) if dedicated VMs must have them alone.
      </p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : error || !data ? (
        <div>
          <p role="alert" className="text-sm text-destructive">{(error as Error)?.message ?? "Could not load the dedicated cores."}</p>
          <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : (
        <div className="space-y-4">
          <dl className="grid gap-4 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-xs text-muted-foreground">Pool</dt>
              <dd className="mt-1 font-mono">{data.pool || "None"}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">In use</dt>
              <dd className="mt-1 tabular-nums">{data.used} of {data.size} cores</dd>
            </div>
          </dl>
          {data.vms.length > 0 && (
            <ul className="space-y-1 text-sm">
              {data.vms.map((v) => (
                <li key={v.id} className="flex justify-between gap-3">
                  <Link href={`/vms/${v.id}`} className="underline underline-offset-2">{v.hostname}</Link>
                  <span className="font-mono text-muted-foreground">{v.cpus}</span>
                </li>
              ))}
            </ul>
          )}
          {can("node:update") && (
            <div className="flex flex-wrap items-end gap-2">
              <div>
                <label htmlFor="cpu-pool" className="mb-1 block text-xs font-medium text-muted-foreground">Cores</label>
                <Input id="cpu-pool" value={value} placeholder="4-15" onChange={(e) => setDraft(e.target.value)} className="w-48 font-mono" />
              </div>
              <Button size="sm" disabled={draft === null || draft === data.pool || save.isPending} onClick={async () => {
                try {
                  await save.mutateAsync(value.trim())
                  setDraft(null)
                  toast.success("Dedicated cores saved")
                } catch (e) { toast.error((e as Error).message) }
              }}>
                {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}Save cores
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
