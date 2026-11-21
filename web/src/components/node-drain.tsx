"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useConfirm } from "@/components/confirm-provider"
import { useCan } from "@/lib/hooks/use-auth"
import { useNodes } from "@/lib/hooks/use-nodes"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import { useAbortDrain, useDrainPlan, useNodeDrain, useStartDrain } from "@/lib/hooks/use-migration"
import type { DrainPlan, DrainReport, DrainReportItem, MigrationStatus, NodeLink } from "@/types"

const TONE = {
  busy: { text: "text-amber-700 dark:text-amber-400", dot: "bg-amber-500" },
  ok: { text: "text-emerald-700 dark:text-emerald-400", dot: "bg-emerald-500" },
  bad: { text: "text-red-700 dark:text-red-400", dot: "bg-red-500" },
  off: { text: "text-muted-foreground", dot: "bg-muted-foreground" },
}

function Lamp({ tone, word }: { tone: keyof typeof TONE; word: string }) {
  return (
    <span className={`inline-flex items-center gap-1.5 text-sm font-medium ${TONE[tone].text}`}>
      <span className={`h-2 w-2 rounded-full ${TONE[tone].dot}`} aria-hidden="true" />
      {word}
    </span>
  )
}

const MOVE_STATE: Record<MigrationStatus, { tone: keyof typeof TONE; word: string }> = {
  queued: { tone: "busy", word: "Waiting" },
  running: { tone: "busy", word: "Moving" },
  cancelling: { tone: "busy", word: "Cancelling" },
  completed: { tone: "ok", word: "Moved" },
  failed: { tone: "bad", word: "Failed" },
  cancelled: { tone: "off", word: "Cancelled" },
  skipped: { tone: "bad", word: "Not moved" },
}

const DRAIN_STATE = {
  running: { tone: "busy", word: "Draining" },
  aborting: { tone: "busy", word: "Stopping" },
  completed: { tone: "ok", word: "Drained" },
  incomplete: { tone: "bad", word: "Drained, VMs left" },
  aborted: { tone: "off", word: "Stopped" },
} as const

function LinkInfo({ link }: { link: NodeLink | null }) {
  if (!link || !link.key_fingerprint) {
    return (
      <p className="text-sm text-muted-foreground">
        Migration link not set up yet. The panel sets it up the first time a VM moves to or from this node: the node
        gets its own key, accepted on the other nodes only from this node&apos;s address and only for moving VMs, and
        its SSH host keys are pinned there.
      </p>
    )
  }
  return (
    <div className="text-sm">
      <p className="text-muted-foreground">
        Migration key <span className="font-mono text-xs text-foreground">{link.key_fingerprint}</span>
        {link.recorded_at && <> · recorded {new Date(link.recorded_at).toLocaleString()}</>}
      </p>
      {link.host_key_fingerprints.length > 0 && (
        <p className="mt-1 text-muted-foreground">
          SSH host keys pinned on the other nodes:{" "}
          {link.host_key_fingerprints.map((f) => <span key={f} className="mr-2 font-mono text-xs text-foreground">{f}</span>)}
        </p>
      )}
    </div>
  )
}

function ItemRow({ item }: { item: DrainReportItem }) {
  const s = MOVE_STATE[item.status] ?? MOVE_STATE.queued
  const p = item.bytes_total > 0 ? Math.min(100, Math.floor((item.bytes_done / item.bytes_total) * 100)) : null
  return (
    <li className="py-2">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="w-28 shrink-0"><Lamp tone={s.tone} word={s.word} /></span>
        <Link href={`/vms/${item.vm_id}`} className="text-sm font-medium hover:underline">{item.hostname || item.vm_id}</Link>
        {item.dest_node_name && <span className="text-sm text-muted-foreground">to {item.dest_node_name}</span>}
      </div>
      <p className="mt-0.5 text-sm text-muted-foreground sm:pl-[7.75rem]">
        {item.status === "running" || item.status === "queued" || item.status === "cancelling"
          ? <>{item.phase}{p !== null && ` · ${p}% (${formatBytes(item.bytes_done)} of ${formatBytes(item.bytes_total)})`}</>
          : item.error || item.note || (item.status === "completed" ? "Running on its new node." : "")}
      </p>
    </li>
  )
}

function Report({ report }: { report: DrainReport }) {
  const d = report.drain!
  const s = DRAIN_STATE[d.status] ?? DRAIN_STATE.running
  return (
    <div className="space-y-2">
      <p className="flex flex-wrap items-center gap-x-3 text-sm">
        <Lamp tone={s.tone} word={s.word} />
        <span>{report.moved} moved</span>
        {report.failed > 0 && <span className={TONE.bad.text}>{report.failed} failed</span>}
        {report.skipped > 0 && <span className={TONE.bad.text}>{report.skipped} could not move</span>}
        <span className="text-muted-foreground">{report.remaining} still on this node · started {new Date(d.started_at).toLocaleString()}</span>
      </p>
      {d.status !== "running" && d.status !== "aborting" && report.remaining > 0 && (
        <p className="text-sm text-muted-foreground">The VMs left here stay as they were. New VMs stay off this node until it is resumed.</p>
      )}
      {report.items.length > 0 && <ul className="divide-y">{report.items.map((i) => <ItemRow key={i.id} item={i} />)}</ul>}
    </div>
  )
}

function PlanTable({ plan, destinations, onDestination, nodeId }: {
  plan: DrainPlan; destinations: Record<string, string>; onDestination: (vmId: string, nodeId: string) => void; nodeId: string
}) {
  const { data: nodes } = useNodes()
  const others = (nodes ?? []).filter((n) => n.id !== nodeId)
  if (plan.items.length === 0) {
    return <p className="text-sm text-muted-foreground">There are no VMs on this node.</p>
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b text-left text-muted-foreground">
            <th className="py-2 pr-4 font-medium">VM</th>
            <th className="py-2 pr-4 font-medium">How</th>
            <th className="py-2 pr-4 font-medium">Destination</th>
            <th className="py-2 font-medium">Plan</th>
          </tr>
        </thead>
        <tbody>
          {plan.items.map((item) => (
            <tr key={item.vm_id} className="border-b align-top">
              <td className="py-2 pr-4"><Link href={`/vms/${item.vm_id}`} className="font-medium hover:underline">{item.hostname}</Link></td>
              <td className="py-2 pr-4">{item.mode === "live" ? "Live" : item.mode === "offline" ? "Offline" : "—"}</td>
              <td className="py-2 pr-4">
                <select aria-label={`Destination for ${item.hostname}`} className="rounded-md border bg-background px-2 py-1 text-sm"
                  value={destinations[item.vm_id] ?? ""} onChange={(e) => onDestination(item.vm_id, e.target.value)}>
                  <option value="">Best fit{!destinations[item.vm_id] && item.dest_node_name ? ` (${item.dest_node_name})` : ""}</option>
                  {others.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
                </select>
              </td>
              <td className="py-2">
                {item.can_move ? <Lamp tone="ok" word="Moves" /> : (
                  <>
                    <Lamp tone="bad" word="Stays" />
                    <ul className="mt-1 list-disc space-y-0.5 pl-5 text-muted-foreground">{item.blockers.map((b) => <li key={b}>{b}</li>)}</ul>
                  </>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// NodeDrain empties a node before maintenance: it stops new VMs landing on
// it, moves the VMs one (or a few) at a time, and reports what stayed and why.
export function NodeDrain({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const { data, isLoading, error } = useNodeDrain(nodeId)
  const mayDrain = useCan()("node:drain")
  const planner = useDrainPlan(nodeId)
  const start = useStartDrain(nodeId)
  const abort = useAbortDrain(nodeId)
  const confirm = useConfirm()
  const [destinations, setDestinations] = useState<Record<string, string>>({})
  const [concurrency, setConcurrency] = useState(1)
  const running = data?.report.drain?.status === "running" || data?.report.drain?.status === "aborting"
  const plan = planner.data

  const makePlan = async (dest: Record<string, string>) => {
    try {
      await planner.mutateAsync(dest)
    } catch (err) {
      toast.error(`The plan could not be made: ${(err as Error).message}`)
    }
  }
  const setDestination = (vmId: string, node: string) => {
    const next = { ...destinations }
    if (node) next[vmId] = node
    else delete next[vmId]
    setDestinations(next)
    makePlan(next)
  }

  const startDrain = async () => {
    if (!plan) return
    const ok = await confirm({
      title: `Drain ${nodeName}?`,
      description: `New VMs stop being placed on ${nodeName}. Its VMs are moved ${concurrency === 1 ? "one at a time" : `${concurrency} at a time`}: running ones live, stopped ones offline. A VM that cannot move, or fails twice, stays here as it is and the report says why.`,
      confirmLabel: "Start drain",
      details: [
        { label: "Will move", value: String(plan.movable) },
        { label: "Will stay", value: String(plan.blocked) },
      ],
    })
    if (!ok) return
    try {
      await start.mutateAsync({ concurrency, destinations })
      planner.reset()
      toast.success(`Draining ${nodeName}`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const stopDrain = async () => {
    const ok = await confirm({
      title: `Stop draining ${nodeName}?`,
      description: "No further VM starts moving and the moves under way are cancelled. Every VM stays where it is now. New VMs stay off this node until it is resumed.",
      confirmLabel: "Stop drain",
      cancelLabel: "Keep draining",
    })
    if (!ok) return
    try {
      await abort.mutateAsync()
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <div className="mb-1 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold text-foreground">Drain</h2>
        {mayDrain && (running ? (
          <Button variant="outline" size="sm" onClick={stopDrain} disabled={abort.isPending || data?.report.drain?.status === "aborting"}>
            {abort.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
            Stop drain
          </Button>
        ) : (
          <Button variant="outline" size="sm" onClick={() => makePlan(destinations)} disabled={planner.isPending}>
            {planner.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
            {plan ? "Plan again" : "Plan a drain"}
          </Button>
        ))}
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        Moves every VM off this node before maintenance. Planning asks both nodes about each VM and changes nothing.
      </p>

      {isLoading ? (
        <Skeleton className="h-16 w-full" />
      ) : error ? (
        <p className="text-sm text-red-700 dark:text-red-400">The drain state could not be loaded: {(error as Error).message}</p>
      ) : (
        <div className="space-y-4">
          {plan && !running && (
            <div className="space-y-3">
              <p className="text-sm">
                <span className={`font-medium ${TONE.ok.text}`}>{plan.movable} can move</span>
                {plan.blocked > 0 && <>, <span className={`font-medium ${TONE.bad.text}`}>{plan.blocked} would stay</span></>}
              </p>
              <PlanTable plan={plan} destinations={destinations} onDestination={setDestination} nodeId={nodeId} />
              {mayDrain && plan.movable > 0 && (
                <div className="flex flex-wrap items-end gap-3">
                  <div className="space-y-1">
                    <label htmlFor="drain-concurrency" className="text-sm font-medium">At once</label>
                    <select id="drain-concurrency" className="block rounded-md border bg-background px-3 py-2 text-sm" value={concurrency}
                      onChange={(e) => setConcurrency(Number(e.target.value))}>
                      <option value={1}>1 VM</option>
                      <option value={2}>2 VMs</option>
                      <option value={3}>3 VMs</option>
                    </select>
                  </div>
                  <Button onClick={startDrain} disabled={start.isPending || planner.isPending}>
                    {start.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
                    Start drain
                  </Button>
                </div>
              )}
            </div>
          )}
          {data?.report.drain ? <Report report={data.report} /> : !plan && <p className="text-sm text-muted-foreground">This node has not been drained.</p>}
          <LinkInfo link={data?.link ?? null} />
        </div>
      )}
    </div>
  )
}
