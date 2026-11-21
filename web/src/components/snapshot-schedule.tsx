"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useConfirm } from "@/components/confirm-provider"
import { useDeleteSnapshotSchedule, useSetSnapshotSchedule, useSnapshotSchedule } from "@/lib/hooks/use-snapshots"
import type { SnapshotSchedule, SnapshotScheduleRequest } from "@/types"

const INTERVALS = [
  { hours: 6, label: "Every 6 hours" },
  { hours: 12, label: "Every 12 hours" },
  { hours: 24, label: "Every day" },
  { hours: 168, label: "Every week" },
]

const selectClass =
  "h-10 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"

function intervalWord(hours: number) {
  return INTERVALS.find((i) => i.hours === hours)?.label ?? `Every ${hours} hours`
}

function draftOf(s: SnapshotSchedule | null | undefined): SnapshotScheduleRequest {
  return s
    ? { interval_hours: s.interval_hours, keep: s.keep, disk_only: s.disk_only, quiesce: s.quiesce, enabled: s.enabled }
    : { interval_hours: 24, keep: 3, disk_only: true, quiesce: false, enabled: true }
}

// SnapshotScheduleCard sets the VM's snapshot schedule: a snapshot every few
// hours, keeping the newest few the schedule took.
export function SnapshotScheduleCard({ vmId, readOnly }: { vmId: string; readOnly?: boolean }) {
  const { data: schedule, isLoading } = useSnapshotSchedule(vmId)
  const save = useSetSnapshotSchedule(vmId)
  const remove = useDeleteSnapshotSchedule(vmId)
  const confirm = useConfirm()
  const [draft, setDraft] = useState<SnapshotScheduleRequest | null>(null)
  const value = draft ?? draftOf(schedule)

  const submit = async (body: SnapshotScheduleRequest) => {
    try {
      await save.mutateAsync(body)
      setDraft(null)
      toast.success(body.enabled === false ? "Schedule paused" : "Schedule saved")
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground">
      <h2 className="text-lg font-semibold text-foreground">Snapshot schedule</h2>
      <p className="mb-4 mt-1 text-sm text-muted-foreground">
        Takes a snapshot on a schedule and keeps the newest ones it took; the oldest goes when a new one is taken.
        Snapshots you take yourself are never removed, and count against your snapshot allowance.
      </p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : (
        <div className="space-y-4">
          {schedule && (
            <dl className="grid gap-4 text-sm sm:grid-cols-3">
              <div>
                <dt className="text-xs text-muted-foreground">Schedule</dt>
                <dd className="mt-1">{schedule.enabled ? `${intervalWord(schedule.interval_hours)}, keeping ${schedule.keep}` : "Paused"}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Next snapshot</dt>
                <dd className="mt-1 tabular-nums">{schedule.enabled ? new Date(schedule.next_run_at).toLocaleString() : "None while paused"}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Last run</dt>
                <dd className="mt-1 tabular-nums">{schedule.last_run_at ? new Date(schedule.last_run_at).toLocaleString() : "Not yet"}</dd>
              </div>
            </dl>
          )}
          {schedule?.last_error && (
            <p role="status" className="text-sm"><span className="font-medium text-amber-600 dark:text-amber-400">Last run skipped.</span>{" "}
              <span className="text-muted-foreground">{schedule.last_error}</span></p>
          )}
          {!readOnly && (
            <>
              <div className="grid gap-4 sm:grid-cols-2">
                <div>
                  <label htmlFor="snap-interval" className="mb-1 block text-xs font-medium text-muted-foreground">How often</label>
                  <select id="snap-interval" className={selectClass} value={value.interval_hours}
                    onChange={(e) => setDraft({ ...value, interval_hours: Number(e.target.value) })}>
                    {INTERVALS.map((i) => <option key={i.hours} value={i.hours}>{i.label}</option>)}
                    {!INTERVALS.some((i) => i.hours === value.interval_hours) && <option value={value.interval_hours}>{intervalWord(value.interval_hours)}</option>}
                  </select>
                </div>
                <div>
                  <label htmlFor="snap-keep" className="mb-1 block text-xs font-medium text-muted-foreground">Keep</label>
                  <select id="snap-keep" className={selectClass} value={value.keep} onChange={(e) => setDraft({ ...value, keep: Number(e.target.value) })}>
                    {Array.from({ length: 10 }, (_, i) => i + 1).map((n) => <option key={n} value={n}>{n} snapshot{n > 1 ? "s" : ""}</option>)}
                  </select>
                </div>
              </div>
              <label htmlFor="snap-disk-only" className="flex items-start gap-2 text-sm">
                <input id="snap-disk-only" type="checkbox" className="mt-0.5 h-4 w-4 rounded border-input" checked={value.disk_only}
                  onChange={(e) => setDraft({ ...value, disk_only: e.target.checked, quiesce: e.target.checked && value.quiesce })} />
                <span><span className="font-medium">Disks only</span>
                  <span className="block text-xs text-muted-foreground">No memory saved and no pause while it runs. Reverting boots the VM from the snapshot&apos;s disks.</span></span>
              </label>
              <label htmlFor="snap-quiesce" className="flex items-start gap-2 text-sm">
                <input id="snap-quiesce" type="checkbox" className="mt-0.5 h-4 w-4 rounded border-input" checked={value.quiesce} disabled={!value.disk_only}
                  onChange={(e) => setDraft({ ...value, quiesce: e.target.checked })} />
                <span><span className="font-medium">Freeze the filesystems first</span>
                  <span className="block text-xs text-muted-foreground">Needs the guest agent; a run is skipped when it does not answer.</span></span>
              </label>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" disabled={save.isPending || (!!schedule?.enabled && !draft)} onClick={() => submit({ ...value, enabled: true })}>
                  {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                  {!schedule ? "Turn on" : schedule.enabled ? "Save schedule" : "Resume"}
                </Button>
                {schedule?.enabled && (
                  <Button size="sm" variant="outline" disabled={save.isPending} onClick={() => submit({ ...draftOf(schedule), enabled: false })}>Pause</Button>
                )}
                {schedule && (
                  <Button size="sm" variant="outline" disabled={remove.isPending} onClick={() => confirm({
                    title: "Remove the snapshot schedule?",
                    description: "No more snapshots are taken on a schedule. The ones it took are kept, as your own.",
                    confirmLabel: "Remove schedule",
                    action: async () => { await remove.mutateAsync(); setDraft(null); toast.success("Schedule removed") },
                  })}>Remove</Button>
                )}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}
