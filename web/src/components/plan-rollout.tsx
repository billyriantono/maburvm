"use client"

import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { StatusBadge } from "@/components/status-badge"
import { useApplyPlan, usePlanRollout } from "@/lib/hooks/use-plans"
import type { Plan, PlanRolloutVM } from "@/types/plan"

// Applying a plan's current settings to the VMs already on it: a preview of
// every VM and what changes, then the result for each.
export function PlanRolloutDialog({ plan, onClose }: { plan: Plan | null; onClose: () => void }) {
  const preview = usePlanRollout(plan?.id ?? null)
  const apply = useApplyPlan()
  const result = apply.data?.plan_id === plan?.id ? apply.data : undefined
  const vms = result?.vms ?? preview.data?.vms ?? []
  const toChange = vms.filter((v) => v.changes.length > 0 && v.status !== "creating")
  const waiting = vms.filter((v) => v.needs_stop)

  const close = () => {
    apply.reset()
    onClose()
  }
  const run = async () => {
    if (!plan) return
    try {
      const out = await apply.mutateAsync(plan.id)
      const failed = out.vms.filter((v) => v.outcome === "failed" || v.outcome === "partial").length
      if (failed) toast.error(`${failed} of ${out.vms.length} VMs did not take every change. Each one says why.`)
      else toast.success(`Plan "${plan.name}" applied to its VMs`)
    } catch (err) {
      toast.error(`The plan could not be applied: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open={!!plan} onOpenChange={(open) => !open && close()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Apply &ldquo;{plan?.name}&rdquo; to its VMs</DialogTitle>
          <DialogDescription>
            Brings each VM on this plan to its current size, CPU and disk limits, speed, transfer quota and mail block, the way a package change does. A running VM keeps its size until it is stopped: nothing is restarted. Snapshot, backup and address allowances already apply without this.
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-[50vh] overflow-y-auto rounded-md border">
          {preview.isLoading ? (
            <p className="flex items-center gap-2 p-4 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              Reading the VMs on this plan
            </p>
          ) : preview.error ? (
            <p role="alert" className="p-4 text-sm text-destructive">The VMs on this plan could not be read: {(preview.error as Error).message}</p>
          ) : vms.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">No VMs are on this plan. New orders on it get its settings as they are.</p>
          ) : (
            <ul className="divide-y">
              {vms.map((vm) => (
                <RolloutRow key={vm.id} vm={vm} />
              ))}
            </ul>
          )}
        </div>

        {vms.length > 0 && (
          <p className="text-sm text-muted-foreground" aria-live="polite">
            {result
              ? `Applied to ${vms.length} ${vms.length === 1 ? "VM" : "VMs"}.`
              : `${vms.length} ${vms.length === 1 ? "VM" : "VMs"}: ${toChange.length} to change, ${vms.length - toChange.length} already match.`}
            {waiting.length > 0 && ` ${waiting.length} must be stopped for the new size, then applied again.`}
          </p>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={close}>{result ? "Close" : "Cancel"}</Button>
          {!result && (
            <Button onClick={run} disabled={apply.isPending || toChange.length === 0}>
              {apply.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
              {apply.isPending ? "Applying" : "Apply changes"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RolloutRow({ vm }: { vm: PlanRolloutVM }) {
  return (
    <li className="space-y-1.5 p-4 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Link href={`/vms/${vm.id}`} className="font-medium underline-offset-4 hover:underline">{vm.hostname}</Link>
        <span className="flex items-center gap-3">
          <StatusBadge status={vm.status} />
          {vm.outcome && <StatusBadge status={vm.outcome} />}
        </span>
      </div>
      {vm.changes.length > 0 ? (
        <ul className="list-disc pl-5 text-muted-foreground">
          {vm.changes.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">Already matches the plan.</p>
      )}
      {vm.notes.map((n) => (
        <p key={n} className="text-muted-foreground">{n}</p>
      ))}
      {vm.errors?.map((e) => (
        <p key={e} role="alert" className="text-destructive">{e}</p>
      ))}
    </li>
  )
}
