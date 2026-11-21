"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { useVMLimits } from "@/lib/hooks/use-vms"
import { usePlans } from "@/lib/hooks/use-plans"
import type { VM, VMLimits, VMLimitsResult } from "@/types"

export const NO_LIMITS: VMLimits = {
  cpu_cap_percent: 0, cpu_weight: 0, iops: 0, read_iops: 0, write_iops: 0, mbps: 0, read_mbps: 0, write_mbps: 0,
}

const PRIORITIES = [
  { label: "Normal", weight: 0 },
  { label: "Low", weight: 50 },
  { label: "High", weight: 200 },
]

function priorityWord(weight: number) {
  return PRIORITIES.find((p) => p.weight === weight)?.label ?? `Custom (${weight})`
}

const n = (v: number) => v.toLocaleString()

// limitsSummary words a set of limits, one line per kind; empty for none.
export function limitsSummary(l: VMLimits | null | undefined): string[] {
  if (!l) return []
  const out: string[] = []
  if (l.cpu_cap_percent > 0 && l.cpu_cap_percent < 100) out.push(`CPU capped at ${l.cpu_cap_percent}% of a core per vCPU`)
  if (l.cpu_weight > 0) out.push(`CPU priority ${priorityWord(l.cpu_weight).toLowerCase()}`)
  if (l.iops > 0) out.push(`Disk ${n(l.iops)} IOPS`)
  if (l.read_iops > 0 || l.write_iops > 0) {
    out.push(`Disk ${l.read_iops > 0 ? `${n(l.read_iops)} read` : "unlimited read"}, ${l.write_iops > 0 ? `${n(l.write_iops)} write` : "unlimited write"} IOPS`)
  }
  if (l.mbps > 0) out.push(`Disk ${n(l.mbps)} MB/s`)
  if (l.read_mbps > 0 || l.write_mbps > 0) {
    out.push(`Disk ${l.read_mbps > 0 ? `${n(l.read_mbps)} MB/s read` : "unlimited read"}, ${l.write_mbps > 0 ? `${n(l.write_mbps)} MB/s write` : "unlimited write"}`)
  }
  return out
}

// limitsProblem is what the panel would refuse, said before saving.
export function limitsProblem(l: VMLimits): string | null {
  if (l.cpu_cap_percent < 0 || l.cpu_cap_percent > 100) return "The CPU cap is a percentage from 1 to 100."
  if ([l.iops, l.read_iops, l.write_iops].some((v) => v !== 0 && (v < 10 || v > 1000000))) return "A disk IOPS limit is from 10 to 1,000,000."
  if ([l.mbps, l.read_mbps, l.write_mbps].some((v) => v < 0 || v > 100000)) return "A disk throughput limit is from 1 to 100,000 MB/s."
  if (l.iops > 0 && (l.read_iops > 0 || l.write_iops > 0)) return "Set a total IOPS limit or read and write limits, not both."
  if (l.mbps > 0 && (l.read_mbps > 0 || l.write_mbps > 0)) return "Set a total throughput limit or read and write limits, not both."
  return null
}

function NumberField({ id, label, value, onChange, suffix }: { id: string; label: string; value: number; onChange: (v: number) => void; suffix?: string }) {
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-xs font-medium text-muted-foreground">{label}</label>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="number"
          min={0}
          inputMode="numeric"
          placeholder="No limit"
          value={value > 0 ? value : ""}
          onChange={(e) => onChange(Math.max(0, Math.floor(Number(e.target.value) || 0)))}
        />
        {suffix && <span className="shrink-0 text-xs text-muted-foreground">{suffix}</span>}
      </div>
    </div>
  )
}

// LimitsFields edits CPU and disk caps. Shared by the plan form and the VM's
// own limits; an empty field is no limit.
export function LimitsFields({ idPrefix, value, onChange }: { idPrefix: string; value: VMLimits; onChange: (l: VMLimits) => void }) {
  const set = (k: keyof VMLimits) => (v: number) => onChange({ ...value, [k]: v })
  const custom = !PRIORITIES.some((p) => p.weight === value.cpu_weight)
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <NumberField id={`${idPrefix}-cap`} label="CPU cap per vCPU" suffix="% of a core" value={value.cpu_cap_percent} onChange={set("cpu_cap_percent")} />
        <div>
          <label htmlFor={`${idPrefix}-weight`} className="mb-1 block text-xs font-medium text-muted-foreground">CPU priority</label>
          <select
            id={`${idPrefix}-weight`}
            value={value.cpu_weight}
            onChange={(e) => onChange({ ...value, cpu_weight: Number(e.target.value) })}
            className="h-10 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            {PRIORITIES.map((p) => <option key={p.weight} value={p.weight}>{p.label}</option>)}
            {custom && <option value={value.cpu_weight}>Custom ({value.cpu_weight})</option>}
          </select>
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        The cap holds even on an idle node. Priority only matters when the node is busy: a low-priority VM gets half the CPU time of a normal one, a high one double.
      </p>
      <div className="grid gap-4 sm:grid-cols-3">
        <NumberField id={`${idPrefix}-iops`} label="Disk IOPS, total" value={value.iops} onChange={set("iops")} />
        <NumberField id={`${idPrefix}-riops`} label="Read IOPS" value={value.read_iops} onChange={set("read_iops")} />
        <NumberField id={`${idPrefix}-wiops`} label="Write IOPS" value={value.write_iops} onChange={set("write_iops")} />
        <NumberField id={`${idPrefix}-mbps`} label="Disk MB/s, total" value={value.mbps} onChange={set("mbps")} />
        <NumberField id={`${idPrefix}-rmbps`} label="Read MB/s" value={value.read_mbps} onChange={set("read_mbps")} />
        <NumberField id={`${idPrefix}-wmbps`} label="Write MB/s" value={value.write_mbps} onChange={set("write_mbps")} />
      </div>
      <p className="text-xs text-muted-foreground">
        Disk limits apply to each of the VM&apos;s disks. Set a total, or read and write separately, not both.
      </p>
    </div>
  )
}

function resultToast(res: VMLimitsResult, done: string) {
  if (res.applied) toast.success(done)
  else toast.warning(res.note ?? "Saved, but the node does not have them yet.")
}

function LimitsDialog({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const { set } = useVMLimits(vm.id)
  const { data: plans } = usePlans()
  const plan = plans?.find((p) => p.id === vm.plan_id)
  const [value, setValue] = useState<VMLimits>({ ...NO_LIMITS, ...vm.limits })
  const problem = limitsProblem(value)

  const save = async () => {
    try {
      resultToast(await set.mutateAsync(value), "Limits saved and applied on the node")
      onClose()
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle className="text-lg font-semibold">Limits for {vm.hostname}</DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            What this VM may take from its node, so it cannot slow down the others there. A running VM takes the change at once, without a restart.
            {vm.throttle && " The throttle stays on top until you remove it."}
          </DialogDescription>
        </DialogHeader>
        <div className="py-2">
          <LimitsFields idPrefix="vm-limits" value={value} onChange={setValue} />
          {plan?.limits && (
            <button type="button" onClick={() => setValue({ ...NO_LIMITS, ...plan.limits })} className="mt-3 text-sm font-medium underline underline-offset-2">
              Use the limits of plan {plan.name}
            </button>
          )}
          {problem && <p role="alert" className="mt-3 text-sm text-destructive">{problem}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button onClick={save} disabled={!!problem || set.isPending}>
            {set.isPending && <Loader2 className="h-4 w-4 animate-spin" />}Save limits
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ThrottleDialog({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const { throttle } = useVMLimits(vm.id)
  const [cap, setCap] = useState(25)
  const [iops, setIOPS] = useState(0)
  const [mbps, setMBps] = useState(0)
  const problem = cap === 0 && iops === 0 && mbps === 0
    ? "Set at least one cap."
    : limitsProblem({ ...NO_LIMITS, cpu_cap_percent: cap, iops, mbps })

  const save = async () => {
    try {
      resultToast(await throttle.mutateAsync({ cpu_cap_percent: cap, iops, mbps }), `${vm.hostname} is throttled`)
      onClose()
    } catch (err) {
      toast.error(`Not throttled: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="text-lg font-semibold">Throttle {vm.hostname}</DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            Holds a VM that is hurting its neighbours, without stopping it. Its own limits are kept, and come back when you remove the throttle. Where the VM&apos;s own limit is tighter, that one stays.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <NumberField id="throttle-cap" label="CPU cap per vCPU" suffix="% of a core" value={cap} onChange={setCap} />
          <div className="grid gap-4 sm:grid-cols-2">
            <NumberField id="throttle-iops" label="Disk IOPS" value={iops} onChange={setIOPS} />
            <NumberField id="throttle-mbps" label="Disk MB/s" value={mbps} onChange={setMBps} />
          </div>
          {problem && <p role="alert" className="text-sm text-destructive">{problem}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button onClick={save} disabled={!!problem || throttle.isPending}>
            {throttle.isPending && <Loader2 className="h-4 w-4 animate-spin" />}Throttle
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// VMLimitsSection is the VM page's account of its CPU and disk caps. Staff
// change them and throttle the VM here; an owner sees the limits their plan
// gives them, and nothing when there are none.
export function VMLimitsSection({ vm, isAdmin }: { vm: VM; isAdmin: boolean }) {
  const { apply, unthrottle } = useVMLimits(vm.id)
  const confirm = useConfirm()
  const [dialog, setDialog] = useState<"limits" | "throttle" | null>(null)
  const own = limitsSummary(vm.limits)
  const throttled = isAdmin && !!vm.throttle
  const busy = vm.status === "creating" || vm.status === "deleting"
  if (!isAdmin && own.length === 0) return null

  const removeThrottle = () => confirm({
    title: `Remove the throttle from ${vm.hostname}?`,
    description: own.length ? "It goes back to its own limits at once." : "It goes back to having no limits at once.",
    confirmLabel: "Remove throttle",
    action: async () => resultToast(await unthrottle.mutateAsync(), "Throttle removed"),
  })

  return (
    <section className="p-4">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <h2 className="text-xs font-medium text-muted-foreground">Limits</h2>
        {isAdmin && <Button variant="outline" size="sm" disabled={busy} onClick={() => setDialog("limits")}>Change</Button>}
      </div>
      {own.length ? (
        <ul className="space-y-0.5">{own.map((line) => <li key={line}>{line}</li>)}</ul>
      ) : (
        <p className="text-muted-foreground">None. It may use as much CPU and disk as the node has free.</p>
      )}
      {throttled && (
        <div className="mt-3">
          <p className="font-medium"><span className="text-amber-600 dark:text-amber-400">Throttled</span>{" "}<span className="font-normal text-muted-foreground">to</span></p>
          <ul className="space-y-0.5">{limitsSummary(vm.throttle).map((line) => <li key={line}>{line}</li>)}</ul>
        </div>
      )}
      {isAdmin && vm.limits_note && (
        <p role="status" className="mt-3 text-xs">
          <span className="font-medium text-amber-600 dark:text-amber-400">Not in force.</span>{" "}
          <span className="text-muted-foreground">{vm.limits_note}</span>
        </p>
      )}
      {isAdmin && (
        <div className="mt-3 flex flex-wrap gap-2">
          {throttled
            ? <Button size="sm" variant="outline" disabled={busy || unthrottle.isPending} onClick={removeThrottle}>Remove throttle</Button>
            : <Button size="sm" variant="outline" disabled={busy} onClick={() => setDialog("throttle")}>Throttle</Button>}
          {vm.limits_note && (
            <Button size="sm" variant="outline" disabled={busy || apply.isPending} onClick={async () => {
              try { resultToast(await apply.mutateAsync(), "Limits applied on the node") } catch (err) { toast.error(`Not applied: ${(err as Error).message}`) }
            }}>
              {apply.isPending && <Loader2 className="h-4 w-4 animate-spin" />}Apply again
            </Button>
          )}
        </div>
      )}
      {dialog === "limits" && <LimitsDialog vm={vm} onClose={() => setDialog(null)} />}
      {dialog === "throttle" && <ThrottleDialog vm={vm} onClose={() => setDialog(null)} />}
    </section>
  )
}
