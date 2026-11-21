"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useConfirm } from "@/components/confirm-provider"
import { useCan } from "@/lib/hooks/use-auth"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import { useNodeRecovery, useRecoveryLeftovers, useRemoveLeftover, useStartRecovery } from "@/lib/hooks/use-migration"
import type { RecoveryLeftover, RecoveryOption, RecoveryPlanItem, RecoveryRun } from "@/types"

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

const RUN_STATE = {
  queued: { tone: "busy", word: "Waiting" },
  running: { tone: "busy", word: "Recovering" },
  completed: { tone: "ok", word: "Recovered" },
  failed: { tone: "bad", word: "Failed" },
} as const

const OLD_COPY: Record<string, string> = {
  pending: "Old copy: this node has not been told yet",
  fenced: "Old copy: shut off here and kept from starting",
  running: "Old copy: RUNNING on this node",
  removed: "Old copy removed",
}

type Choice = { node: string; newAddress: boolean; start: boolean }

// usable says whether the VM can go to this node, given the address choice.
function usable(o: RecoveryOption, newAddress: boolean) {
  return o.can_recover || (newAddress && o.can_with_new_address)
}

function how(o: RecoveryOption) {
  return o.method === "shared" ? "on its shared disks" : o.method === "backup" ? "from its backup" : "cannot take it"
}

function PlanRow({ item, choice, onChoice, mayRecover }: {
  item: RecoveryPlanItem; choice: Choice | undefined; onChoice: (c: Choice | undefined) => void; mayRecover: boolean
}) {
  const possible = item.options.filter((o) => o.can_recover || o.can_with_new_address)
  const opt = item.options.find((o) => o.node_id === choice?.node)
  const shown = opt ?? (possible.length === 0 && item.options.length === 1 ? item.options[0] : undefined)
  return (
    <tr className="border-b align-top">
      <td className="py-2 pr-4">
        <Link href={`/vms/${item.vm_id}`} className="font-medium hover:underline">{item.hostname}</Link>
        <p className="text-muted-foreground">{item.status} · {item.disk_gb} GB</p>
      </td>
      <td className="py-2 pr-4">
        {item.shared_pool ? <>Shared pool {item.shared_pool}</> : <>This node&apos;s own storage</>}
        <p className="text-muted-foreground">
          {item.backup
            ? <>Backup of {item.backup.completed_at ? new Date(item.backup.completed_at).toLocaleString() : "unknown date"} ({formatBytes(item.backup.size)})</>
            : "No finished backup"}
        </p>
      </td>
      <td className="py-2 pr-4">
        {item.active ? (
          <Lamp tone="busy" word="Being recovered" />
        ) : possible.length === 0 ? (
          <Lamp tone="bad" word="Not recoverable" />
        ) : (
          <div className="space-y-1.5">
            <select aria-label={`Recover ${item.hostname} onto`} className="rounded-md border bg-background px-2 py-1 text-sm" disabled={!mayRecover}
              value={choice?.node ?? ""}
              onChange={(e) => {
                const o = possible.find((x) => x.node_id === e.target.value)
                onChoice(o ? { node: o.node_id, newAddress: !o.can_recover, start: item.status === "running" } : undefined)
              }}>
              <option value="">Leave it</option>
              {possible.map((o) => (
                <option key={o.node_id} value={o.node_id}>{o.node_name}, {how(o)}{o.can_recover ? "" : " (new address)"}</option>
              ))}
            </select>
            {opt && choice && !opt.can_recover && (
              <label className="flex items-start gap-2">
                <input type="checkbox" className="mt-1" checked={choice.newAddress} onChange={(e) => onChoice({ ...choice, newAddress: e.target.checked })} />
                <span>Give it a new address from {opt.node_name}&apos;s pools. Its current address is released.</span>
              </label>
            )}
            {opt && choice && opt.method === "shared" && (
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={choice.start} onChange={(e) => onChoice({ ...choice, start: e.target.checked })} />
                <span>Start it once it is defined</span>
              </label>
            )}
          </div>
        )}
      </td>
      <td className="py-2">
        {item.reason && <p className={TONE.bad.text}>{item.reason}</p>}
        {shown && (
          <ul className="list-disc space-y-0.5 pl-5 text-muted-foreground">
            {!item.reason && shown.blockers.map((b) => <li key={b} className={TONE.bad.text}>{b}</li>)}
            {shown.address_blockers.map((b) => <li key={b} className={choice?.newAddress ? "" : TONE.bad.text}>{b}</li>)}
            {usable(shown, choice?.newAddress ?? false) && shown.warnings.map((w) => <li key={w}>{w}</li>)}
          </ul>
        )}
        {!item.reason && !shown && possible.length > 0 && <p className="text-muted-foreground">Choose a node to see what recovering it there takes.</p>}
      </td>
    </tr>
  )
}

function RunRow({ run }: { run: RecoveryRun }) {
  const s = RUN_STATE[run.status] ?? RUN_STATE.queued
  const busy = run.status === "queued" || run.status === "running"
  return (
    <li className="py-2">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="w-28 shrink-0"><Lamp tone={s.tone} word={s.word} /></span>
        <Link href={`/vms/${run.vm_id}`} className="text-sm font-medium hover:underline">{run.hostname || run.vm_id}</Link>
        <span className="text-sm text-muted-foreground">
          to {run.dest_node_name || run.dest_node_id}, {run.method === "shared" ? "on its shared disks" : "from its backup"} · {new Date(run.created_at).toLocaleString()}
        </span>
      </div>
      <p className="mt-0.5 text-sm text-muted-foreground sm:pl-[7.75rem]">
        {busy ? run.phase : run.error || run.note || (run.method === "shared" ? "Defined on its new node." : "Restored and running on its new node.")}
      </p>
      {run.moved && run.old_copy && (
        <p className={`mt-0.5 text-sm sm:pl-[7.75rem] ${run.old_copy === "running" ? TONE.bad.text : "text-muted-foreground"}`}>
          {OLD_COPY[run.old_copy]}
          {(run.old_copy === "pending" || run.old_copy === "running") && <> · VM id <span className="font-mono text-xs">{run.vm_id}</span></>}
        </p>
      )}
    </li>
  )
}

function Leftovers({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const [asked, setAsked] = useState(false)
  const { data, isFetching, error, refetch } = useRecoveryLeftovers(nodeId, asked)
  const remove = useRemoveLeftover(nodeId)
  const confirm = useConfirm()

  const removeCopy = async (l: RecoveryLeftover) => {
    const running = l.state !== "shutoff"
    const local = (l.disks ?? []).filter((d) => !d.shared)
    const ok = await confirm({
      title: `Remove the old copy of ${l.hostname || l.vm_id} from ${nodeName}?`,
      description: l.method === "shared"
        ? `Its definition on ${nodeName} is removed. Its disks are on shared storage and are the disks it runs on now on ${l.dest_node_name}: they are not touched.`
        : `Its definition and its disks on ${nodeName} are deleted for good. Those disks hold what the VM wrote after the backup it was restored from: copy off anything you still need first.`,
      confirmLabel: running ? "Power off and remove" : "Remove the old copy",
      destructive: true,
      details: [
        { label: "On this node it is", value: l.state },
        ...local.map((d) => ({ label: `Deletes ${d.target}`, value: `${d.path}${d.size_bytes ? ` (${formatBytes(d.size_bytes)})` : ""}` })),
      ],
    })
    if (!ok) return
    try {
      await remove.mutateAsync({ vmId: l.vm_id, forceStop: running })
      toast.success(`Old copy of ${l.hostname || l.vm_id} removed from ${nodeName}`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-3">
        <h3 className="text-sm font-semibold">Recovery leftovers</h3>
        <Button variant="outline" size="sm" disabled={isFetching} onClick={() => (asked ? refetch() : setAsked(true))}>
          {isFetching && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
          {asked ? "Ask the node again" : "Ask the node what is left"}
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        What this node still holds of the VMs that were recovered elsewhere. The node keeps them shut off and will not start them.
      </p>
      {error && <p className={`text-sm ${TONE.bad.text}`}>The node could not be asked: {(error as Error).message}</p>}
      {data && data.length === 0 && <p className="text-sm text-muted-foreground">Nothing is left on this node.</p>}
      {data && data.length > 0 && (
        <ul className="divide-y">
          {data.map((l) => (
            <li key={l.id} className="flex flex-wrap items-start justify-between gap-3 py-2">
              <div className="text-sm">
                <Link href={`/vms/${l.vm_id}`} className="font-medium hover:underline">{l.hostname || l.vm_id}</Link>{" "}
                <span className="text-muted-foreground">now on {l.dest_node_name}</span>
                <p className={l.state === "shutoff" ? "text-muted-foreground" : TONE.bad.text}>
                  {l.state === "shutoff" ? "Shut off here" : `${l.state} on this node: two copies must not run`}
                </p>
                {(l.disks ?? []).map((d) => (
                  <p key={d.path} className="font-mono text-xs text-muted-foreground">
                    {d.target} {d.path}{d.size_bytes ? ` · ${formatBytes(d.size_bytes)}` : ""}{d.shared ? " · shared, stays" : ""}
                  </p>
                ))}
              </div>
              <Button variant="outline" size="sm" disabled={remove.isPending} onClick={() => removeCopy(l)}>Remove</Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// NodeRecovery brings the VMs of a node that went down back on other nodes,
// VM by VM, and afterwards lists what the node still holds of them. It shows
// nothing for a node that is up and was never recovered from.
export function NodeRecovery({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const { data: plan, isLoading, error } = useNodeRecovery(nodeId)
  const mayRecover = useCan()("node:recover")
  const start = useStartRecovery(nodeId)
  const [choices, setChoices] = useState<Record<string, Choice>>({})
  const [typed, setTyped] = useState("")
  const [refused, setRefused] = useState<{ hostname: string; reason: string }[]>([])

  if (isLoading) return <Skeleton className="mb-6 h-24 w-full" />
  if (error || !plan) return null
  const down = plan.node_status === "offline"
  if (!down && plan.runs.length === 0) return null

  const chosen = plan.items
    .filter((i) => !i.active && choices[i.vm_id])
    .map((i) => ({ item: i, choice: choices[i.vm_id], opt: i.options.find((o) => o.node_id === choices[i.vm_id].node) }))
    .filter((c) => c.opt && usable(c.opt, c.choice.newAddress))
  const confirmed = typed.trim() === nodeName
  const hasLeftovers = !down && plan.runs.some((r) => r.moved && r.old_copy !== "removed" && r.old_copy !== "")

  const recover = async () => {
    try {
      const out = await start.mutateAsync({
        node_is_down: true,
        vms: chosen.map((c) => ({ vm_id: c.item.vm_id, dest_node_id: c.choice.node, new_address: c.choice.newAddress, start: c.choice.start })),
      })
      setChoices({})
      setTyped("")
      setRefused(out.refused)
      if (out.queued.length > 0) toast.success(`Recovering ${out.queued.length} ${out.queued.length === 1 ? "VM" : "VMs"} from ${nodeName}`)
      if (out.refused.length > 0) toast.error(`${out.refused.length} could not be recovered; see below`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <h2 className="mb-1 text-lg font-semibold text-foreground">Recover VMs on other nodes</h2>
      <p className="mb-4 text-sm text-muted-foreground">
        For a node that is down and not coming back soon. A VM on shared storage is defined on another node on the same disks;
        any other VM is restored from its latest backup. Each stays the same VM, with its owner and, where the other node carries
        its address pool, its address. Nothing happens until you choose VMs and confirm.
      </p>

      <div className="space-y-5">
        {down && (
          <div className="space-y-3">
            {plan.refusal && <p className={`text-sm ${TONE.bad.text}`}>Nothing can be recovered yet: {plan.refusal}.</p>}
            {plan.items.length === 0 ? (
              <p className="text-sm text-muted-foreground">No VMs are left on this node.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b text-left text-muted-foreground">
                      <th className="py-2 pr-4 font-medium">VM</th>
                      <th className="py-2 pr-4 font-medium">Disks and backup</th>
                      <th className="py-2 pr-4 font-medium">Recover onto</th>
                      <th className="py-2 font-medium">What it takes</th>
                    </tr>
                  </thead>
                  <tbody>
                    {plan.items.map((item) => (
                      <PlanRow key={item.vm_id} item={item} choice={choices[item.vm_id]} mayRecover={mayRecover}
                        onChoice={(c) => setChoices((prev) => {
                          const next = { ...prev }
                          if (c) next[item.vm_id] = c
                          else delete next[item.vm_id]
                          return next
                        })} />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {mayRecover && chosen.length > 0 && !plan.refusal && (
              <div className="space-y-3 rounded-md border border-red-200 p-4 dark:border-red-900">
                <p className="text-sm">
                  <span className="font-semibold">Make sure {nodeName} is really off.</span> If it is only unreachable from the panel and its VMs
                  still run, recovering them starts a second copy: two copies writing one disk destroy it, and two copies with one address
                  break both. Power the node off, or cut it off from its storage and its VM network, before you go on. Your confirmation is
                  recorded in the audit log.
                </p>
                <div className="flex flex-wrap items-end gap-3">
                  <div className="space-y-1">
                    <label htmlFor="recover-confirm" className="text-sm font-medium">
                      Type <span className="font-mono">{nodeName}</span> to confirm it is powered off or cut off
                    </label>
                    <Input id="recover-confirm" className="w-64" autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />
                  </div>
                  <Button variant="destructive" onClick={recover} disabled={!confirmed || start.isPending}>
                    {start.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
                    Recover {chosen.length} {chosen.length === 1 ? "VM" : "VMs"}
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}

        {refused.length > 0 && (
          <ul className={`list-disc space-y-0.5 pl-5 text-sm ${TONE.bad.text}`}>
            {refused.map((r) => <li key={r.hostname + r.reason}>{r.hostname || "A VM"} was not recovered: {r.reason}</li>)}
          </ul>
        )}

        {plan.runs.length > 0 && (
          <div>
            <h3 className="text-sm font-semibold">Recovered from this node</h3>
            <ul className="divide-y">{plan.runs.map((r) => <RunRow key={r.id} run={r} />)}</ul>
          </div>
        )}

        {hasLeftovers && mayRecover && <Leftovers nodeId={nodeId} nodeName={nodeName} />}
      </div>
    </div>
  )
}
