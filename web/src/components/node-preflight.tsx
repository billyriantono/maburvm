"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useNodePreflight, useRunNodePreflight } from "@/lib/hooks/use-nodes"
import { useCan } from "@/lib/hooks/use-auth"
import type { PreflightCheck, PreflightStatus } from "@/types"

const STATES: Record<PreflightStatus, { word: string; text: string; dot: string; order: number }> = {
  fail: { word: "Failed", text: "text-red-700 dark:text-red-400", dot: "bg-red-500", order: 0 },
  warn: { word: "Warning", text: "text-amber-700 dark:text-amber-400", dot: "bg-amber-500", order: 1 },
  pass: { word: "Pass", text: "text-emerald-700 dark:text-emerald-400", dot: "bg-emerald-500", order: 2 },
}

function state(s: string) {
  return STATES[s as PreflightStatus] ?? STATES.warn
}

function Row({ check }: { check: PreflightCheck }) {
  const s = state(check.status)
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className={`inline-flex w-20 shrink-0 items-center gap-1.5 text-sm font-medium ${s.text}`}>
          <span className={`h-2 w-2 rounded-full ${s.dot}`} aria-hidden="true" />
          {s.word}
        </span>
        <span className="text-sm font-medium">{check.name}</span>
      </div>
      <p className="mt-1 text-sm text-muted-foreground sm:pl-[5.75rem]">{check.detail}</p>
      {check.fix && check.status !== "pass" && (
        <p className="mt-1 text-sm sm:pl-[5.75rem]">
          <span className="font-medium">How to fix: </span>
          <span className="text-muted-foreground">{check.fix}</span>
        </p>
      )}
    </li>
  )
}

// NodePreflight shows what the node's agent found when it last checked the
// host (KVM, libvirt, bridges, storage pools, tools, clock) and asks again.
export function NodePreflight({ nodeId }: { nodeId: string }) {
  const { data, isLoading, error } = useNodePreflight(nodeId)
  const run = useRunNodePreflight(nodeId)
  const mayCheck = useCan()("node:update")
  const [showPassed, setShowPassed] = useState(false)

  const checkNow = async () => {
    try {
      await run.mutateAsync()
    } catch (err) {
      toast.error(`The node was not checked: ${(err as Error).message}`)
    }
  }

  const checks = [...(data?.checks ?? [])].sort((a, b) => state(a.status).order - state(b.status).order)
  const failed = checks.filter((c) => c.status === "fail").length
  const warned = checks.filter((c) => c.status === "warn").length
  const passed = checks.length - failed - warned
  const shown = showPassed ? checks : checks.filter((c) => c.status !== "pass")

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <div className="mb-1 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold text-foreground">Host checks</h2>
        {mayCheck && (
          <Button variant="outline" size="sm" onClick={checkNow} disabled={run.isPending}>
            {run.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
            {run.isPending ? "Checking…" : "Check now"}
          </Button>
        )}
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        Whether the node is set up the way VMs need: KVM, libvirt, qemu-img, its bridges and storage pools, bridge
        netfilter, the tools the agent runs, and the clock. Checking reads the node and changes nothing on it.
      </p>

      {isLoading ? (
        <Skeleton className="h-16 w-full" />
      ) : error ? (
        <p className="text-sm text-red-700 dark:text-red-400">The last report could not be loaded: {(error as Error).message}</p>
      ) : !data?.supported ? (
        <p className="text-sm">
          <span className="font-medium">Not available on this node.</span>{" "}
          <span className="text-muted-foreground">Its agent is too old to check the host. Update the agent (Settings, System) to see the host checks.</span>
        </p>
      ) : !data.checked_at ? (
        <p className="text-sm text-muted-foreground">
          Not checked yet.{mayCheck ? " Check now asks the node's agent; the node must be online." : " Staff who can edit nodes can check it."}
        </p>
      ) : (
        <>
          <p className="text-sm">
            {failed > 0 && <span className={`font-medium ${STATES.fail.text}`}>{failed} failed</span>}
            {failed > 0 && (warned > 0 || passed > 0) && ", "}
            {warned > 0 && <span className={`font-medium ${STATES.warn.text}`}>{warned} {warned === 1 ? "warning" : "warnings"}</span>}
            {warned > 0 && passed > 0 && ", "}
            {passed > 0 && <span className={`font-medium ${STATES.pass.text}`}>{passed} passed</span>}
            <span className="text-muted-foreground"> · checked {new Date(data.checked_at).toLocaleString()}</span>
          </p>
          {shown.length > 0 && <ul className="mt-2 divide-y">{shown.map((c) => <Row key={c.id} check={c} />)}</ul>}
          {passed > 0 && (
            <button type="button" className="mt-2 text-sm font-medium underline-offset-2 hover:underline" onClick={() => setShowPassed(!showPassed)}>
              {showPassed ? "Hide passed checks" : `Show ${passed} passed ${passed === 1 ? "check" : "checks"}`}
            </button>
          )}
        </>
      )}
    </div>
  )
}
