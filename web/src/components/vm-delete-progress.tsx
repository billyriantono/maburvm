"use client"

import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useVMOperation } from "@/lib/hooks/use-vms"

// DeleteProgressDialog follows a delete through to the end.
//
// Deleting a VM is asynchronous and multi-step — destroy on the host, then
// release its addressing and records — so the moment the request is accepted
// tells you nothing about whether it worked. The VM detail page used to show
// "VM deleted" the instant the job was queued and navigate away, which was a
// claim it had no basis for: the delete that failed on a domain with snapshots
// reported success there while leaving the machine behind.
export function DeleteProgressDialog({ vm, onClose }: { vm: { id: string; hostname: string } | null; onClose: () => void }) {
  const { data: op } = useVMOperation(vm?.id ?? null, !!vm)
  if (!vm) return null

  const total = op?.total_steps || 3
  const step = op?.current_step || 0
  const done = op?.status === "completed"
  const failed = op?.status === "failed"
  // A finished operation is at its last step. The backend now says so too, but
  // the UI must not contradict itself on rows written before that fix — a full
  // bar beside "step 2/3" is three statements where one denies the other two.
  const shownStep = done ? total : Math.min(step, total)
  const pct = done ? 100 : Math.round((Math.max(step - (op?.status === "running" ? 1 : 0), 0) / total) * 100)
  const finished = done || failed
  const label = failed
    ? "Deletion failed"
    : done
      ? "VM deleted"
      : op?.step_label || "Starting…"

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Delete progress">
      <div className="absolute inset-0 bg-black/50" />
      <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-md w-full mx-4">
        <h3 className="text-lg font-semibold mb-1">Deleting {vm.hostname}</h3>
        <p className="text-sm font-medium mb-4 flex items-center gap-2">
          {!finished && <Loader2 className="w-4 h-4 animate-spin" />}
          <span className={failed ? "text-destructive" : done ? "text-emerald-600" : ""}>{label}</span>
          {!failed && <span className="text-muted-foreground">· step {shownStep}/{total}</span>}
        </p>

        <div className="w-full h-2 rounded-full border bg-muted overflow-hidden mb-4">
          <div
            className={`h-full transition-all duration-500 ${failed ? "bg-destructive" : done ? "bg-emerald-500" : "bg-primary"}`}
            style={{ width: `${failed ? 100 : pct}%` }}
          />
        </div>

        {failed && op?.error && (
          <p className="text-xs font-mono text-destructive border border-red-200 bg-red-50 rounded-md dark:bg-red-950 dark:border-red-900 p-2 mb-4 break-words">{op.error}</p>
        )}
        {failed && (
          <p className="text-sm text-muted-foreground mb-4">The VM was not fully removed. It&apos;s marked as error — you can retry the delete.</p>
        )}

        <div className="flex justify-end">
          <Button onClick={onClose} disabled={!finished} className="border">
            {finished ? "Close" : "Working…"}
          </Button>
        </div>
      </div>
    </div>
  )
}
