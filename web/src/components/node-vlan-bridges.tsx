"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useCan } from "@/lib/hooks/use-auth"
import { useNodeVLANBridges, useSetNodeVLANBridges } from "@/lib/hooks/use-nodes"
import { parseVLANBridges, vlanBridgesProblem } from "@/lib/network-input"

// NodeVLANBridges turns per-VM VLANs on for some of the node's bridges. It is
// opt-in: nothing on the node changes until a VM on such a bridge is given a
// VLAN, and then the bridge's VLAN filtering is switched on.
export function NodeVLANBridges({ nodeId }: { nodeId: string }) {
  const can = useCan()
  const { data, isLoading, error, refetch } = useNodeVLANBridges(nodeId)
  const save = useSetNodeVLANBridges(nodeId)
  const [text, setText] = useState<string | null>(null)

  const current = (data?.bridges ?? []).map((b) => `${b.bridge}:${b.uplink}`).join("\n")
  const draft = text ?? current
  const list = parseVLANBridges(draft)
  const problem = vlanBridgesProblem(list)

  const submit = async (useEnvironment: boolean) => {
    try {
      const out = await save.mutateAsync({ use_environment: useEnvironment, bridges: useEnvironment ? [] : list })
      setText(null)
      const broken = out.bridges.filter((b) => b.problem)
      if (broken.length) {
        toast.warning("Saved, but the node sees a problem", { description: broken.map((b) => `${b.bridge}: ${b.problem}`).join("; ") })
      } else {
        toast.success(useEnvironment ? "VLAN bridges now come from the agent's environment" : "VLAN bridges saved")
      }
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <h2 className="text-lg font-semibold text-foreground">VLAN trunking</h2>
      <p className="mb-4 mt-1 text-sm text-muted-foreground">
        Bridges that may put VMs in their own VLAN, each with the uplink port that carries the VLANs tagged to the switch. When the first VM on a
        bridge gets a VLAN, the bridge&apos;s VLAN filtering is switched on: untagged traffic carries on, but tagged frames that pass the bridge
        today need their VLAN added. The switch port must carry the VLANs tagged. Run the host checks after saving.
      </p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : error || !data ? (
        <div>
          <p role="alert" className="text-sm text-destructive">{(error as Error)?.message ?? "Could not load the VLAN bridges."}</p>
          <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : (
        <div className="space-y-3">
          <p className="text-sm">
            {data.use_environment
              ? "Set by the agent's environment (MABURVM_VLAN_BRIDGES) on the node. Saving a list here replaces it."
              : data.bridges.length
                ? `Enabled on ${data.bridges.map((b) => b.bridge).join(", ")}.`
                : "Enabled on no bridge."}
          </p>
          {data.bridges.some((b) => b.problem) && (
            <ul role="alert" className="text-sm text-destructive list-disc pl-5">
              {data.bridges.filter((b) => b.problem).map((b) => <li key={b.bridge}>{b.bridge}: {b.problem}</li>)}
            </ul>
          )}
          {can("node:update") && (
            <>
              <label htmlFor="vlan-bridges" className="block text-xs font-medium text-muted-foreground">One bridge per line, as bridge:uplink</label>
              <textarea
                id="vlan-bridges"
                className="w-full min-h-20 rounded border bg-background p-2 font-mono text-sm"
                placeholder="br1:bond0"
                value={draft}
                onChange={(e) => setText(e.target.value)}
                aria-invalid={!!problem}
                aria-describedby="vlan-bridges-help"
              />
              <p id="vlan-bridges-help" className={`text-xs ${problem ? "text-destructive" : "text-muted-foreground"}`}>
                {problem || "An empty list turns VLANs off for new assignments; bridges keep the filtering they have."}
              </p>
              <div className="flex flex-wrap gap-2">
                <Button type="button" size="sm" disabled={!!problem || text === null || save.isPending} onClick={() => submit(false)}>
                  {save.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}Save
                </Button>
                {!data.use_environment && (
                  <Button type="button" size="sm" variant="outline" disabled={save.isPending} onClick={() => submit(true)}>
                    Use the agent&apos;s environment
                  </Button>
                )}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}
