"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"
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
  const { data: op, error } = useVMOperation(vm?.id ?? null, !!vm)
  // The hook polls for as long as no operation row exists. If the endpoint
  // errors (e.g. the VM is already gone) or nothing arrives within a minute,
  // stop holding the dialog open on a state we cannot observe.
  const [timedOut, setTimedOut] = useState(false)
  useEffect(() => {
    setTimedOut(false)
    if (!vm) return
    const t = setTimeout(() => setTimedOut(true), 60_000)
    return () => clearTimeout(t)
  }, [vm?.id]) // eslint-disable-line react-hooks/exhaustive-deps
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
  const unknown = !done && !failed && (!!error || (!op && timedOut))
  const finished = done || failed || unknown
  const label = unknown
    ? "Status unknown. Check the VM list."
    : failed
    ? "Deletion failed"
    : done
      ? "VM deleted"
      : op?.step_label || "Starting…"

  return (
    <Dialog open onOpenChange={(open) => { if (!open && finished) onClose() }}>
      <DialogContent
        className={`max-w-md${finished ? "" : " [&>button:last-child]:hidden"}`}
        onInteractOutside={(e) => { if (!finished) e.preventDefault() }}
        onEscapeKeyDown={(e) => { if (!finished) e.preventDefault() }}
      >
        <div>
          <DialogTitle className="mb-1">Deleting {vm.hostname}</DialogTitle>
          <DialogDescription asChild>
            <p className="text-sm font-medium flex items-center gap-2 text-foreground">
              {!finished && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
              <span className={failed ? "text-destructive" : done ? "text-emerald-600" : ""}>{label}</span>
              {!failed && !unknown && <span className="text-muted-foreground">· step {shownStep}/{total}</span>}
            </p>
          </DialogDescription>
        </div>

        <div className="w-full h-2 rounded-full border bg-muted overflow-hidden">
          <div
            className={`h-full transition-all duration-500 ${failed ? "bg-destructive" : done ? "bg-emerald-500" : "bg-primary"}`}
            style={{ width: `${failed ? 100 : pct}%` }}
          />
        </div>

        {failed && op?.error && (
          <p className="text-xs font-mono text-destructive border border-red-200 bg-red-50 rounded-md dark:bg-red-950 dark:border-red-900 p-2 break-words">{op.error}</p>
        )}
        {failed && (
          <p className="text-sm text-muted-foreground">The VM was not fully removed. It&apos;s marked as error, so you can retry the delete.</p>
        )}

        <div className="flex justify-end">
          <Button onClick={onClose} disabled={!finished} className="border">
            {finished ? "Close" : "Working…"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
