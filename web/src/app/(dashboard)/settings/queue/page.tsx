"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { type QueueJob, useQueue, useQueueJobAction, useSaveQueueWorkers } from "@/lib/hooks/use-ops"

const FILTERS = [
  { id: "", label: "All" },
  { id: "waiting", label: "Waiting" },
  { id: "running", label: "Running" },
  { id: "discarded", label: "Failed" },
  { id: "cancelled", label: "Cancelled" },
  { id: "completed", label: "Done" },
]

// The queue's own state names, in words an operator uses.
const STATE: Record<string, string> = {
  available: "Waiting", scheduled: "Scheduled", pending: "Waiting", retryable: "Will retry",
  running: "Running", completed: "Done", cancelled: "Cancelled", discarded: "Failed",
}

const QUEUES = [
  { id: "critical", name: "VM actions", does: "Create, start, stop, resize, rebuild, network changes." },
  { id: "default", name: "General", does: "Templates, snapshots and everything not listed elsewhere." },
  { id: "batch", name: "Long jobs", does: "Backups, restores, images and imports." },
  { id: "audit", name: "Audit log", does: "Writing audit entries." },
]

const KIND: Record<string, string> = {
  vm_operation: "VM action", template_sync: "Template sync", backup: "Backup", restore: "Restore",
  image: "Image", snapshot: "Snapshot", import: "Import", network_config: "Network change", audit: "Audit entry",
}

function when(iso: string) {
  return new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" })
}

export default function QueuePage() {
  const confirm = useConfirm()
  const [filter, setFilter] = useState("")
  const { data, isLoading, error, refetch } = useQueue(filter)
  const act = useQueueJobAction()
  const saveWorkers = useSaveQueueWorkers()
  const [workers, setWorkers] = useState<Record<string, string>>({})

  // Start the form from what is saved, or from what is running if nothing is.
  useEffect(() => {
    if (!data || Object.keys(workers).length) return
    setWorkers(Object.fromEntries(QUEUES.map((q) => [q.id, String(data.saved_workers[q.id] ?? data.workers[q.id] ?? "")])))
  }, [data, workers])

  const count = (states: string[]) => (data?.counts ?? []).filter((c) => states.includes(c.state)).reduce((n, c) => n + c.count, 0)
  const waiting = count(["available", "scheduled", "pending", "retryable"])
  const pendingRestart = !!data && QUEUES.some((q) => data.saved_workers[q.id] !== undefined && data.saved_workers[q.id] !== data.workers[q.id])

  const run = async (job: QueueJob, action: "retry" | "cancel") => {
    try {
      await act.mutateAsync({ id: job.id, action })
      toast.success(action === "retry" ? `Job ${job.id} is queued to run again` : `Job ${job.id} cancelled`)
    } catch (e) {
      toast.error(`Could not ${action} job ${job.id}: ${(e as Error).message}`)
    }
  }

  const handleSaveWorkers = async (e: React.FormEvent) => {
    e.preventDefault()
    const parsed: Record<string, number> = {}
    for (const q of QUEUES) {
      const n = Number(workers[q.id])
      if (!Number.isInteger(n) || n < 1 || n > 200) { toast.error(`${q.name}: enter a whole number from 1 to 200.`); return }
      parsed[q.id] = n
    }
    try {
      await saveWorkers.mutateAsync(parsed)
      toast.success("Saved. The new limits apply the next time the panel starts.")
    } catch (err) {
      toast.error(`Could not save: ${(err as Error).message}`)
    }
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Job queue</h1>
        <p className="text-muted-foreground text-sm mt-1 max-w-2xl">
          Everything the panel does on a node runs as a job. This is where to look when a VM is stuck at “creating” or a backup never finished.
        </p>
      </div>

      <section aria-labelledby="jobs-heading">
        <div className="flex flex-wrap items-center justify-between gap-3 mb-3">
          <h2 id="jobs-heading" className="text-lg font-semibold">Jobs</h2>
          {data && (
            <p className="text-sm text-muted-foreground" role="status">
              {waiting} waiting, {count(["running"])} running, {count(["discarded"])} failed. Refreshes every 5 seconds.
            </p>
          )}
        </div>

        <div role="group" aria-label="Show jobs that are" className="flex flex-wrap gap-1 mb-3">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              aria-pressed={filter === f.id}
              onClick={() => setFilter(f.id)}
              className={`rounded-md px-3 py-1.5 text-sm font-medium cursor-pointer transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring ${filter === f.id ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted"}`}
            >
              {f.label}
            </button>
          ))}
        </div>

        <div className="rounded-lg border bg-card shadow-sm overflow-x-auto">
          {isLoading ? (
            <p className="p-5 text-sm text-muted-foreground flex items-center gap-2"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Reading the queue…</p>
          ) : error ? (
            <div className="p-5">
              <p className="text-sm font-medium">The queue could not be read: {(error as Error).message}</p>
              <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Try again</Button>
            </div>
          ) : !data?.jobs.length ? (
            <p className="p-5 text-sm text-muted-foreground">
              {filter ? "No jobs in this state." : "No jobs yet. They appear here as soon as someone creates or changes a VM."}
            </p>
          ) : (
            <table className="w-full text-sm">
              <thead className="bg-muted text-xs text-muted-foreground">
                <tr>
                  <th className="p-3 text-left font-medium">Job</th>
                  <th className="p-3 text-left font-medium">What</th>
                  <th className="p-3 text-left font-medium">State</th>
                  <th className="p-3 text-left font-medium">Tries</th>
                  <th className="p-3 text-left font-medium">Queued</th>
                  <th className="p-3 text-right font-medium"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {data.jobs.map((job) => {
                  const canRetry = ["discarded", "cancelled", "retryable", "scheduled"].includes(job.state)
                  const canCancel = ["available", "scheduled", "pending", "retryable", "running"].includes(job.state)
                  return (
                    <tr key={job.id} className="border-t align-top">
                      <td className="p-3 font-mono tabular-nums">{job.id}</td>
                      <td className="p-3">
                        <p className="font-medium">{KIND[job.kind] ?? job.kind}</p>
                        {job.subject && <p className="text-xs text-muted-foreground font-mono break-all">{job.subject}</p>}
                        {job.last_error && job.state !== "completed" && (
                          <p className="text-xs text-destructive mt-1 max-w-md break-words">{job.last_error}</p>
                        )}
                      </td>
                      <td className="p-3 whitespace-nowrap">{STATE[job.state] ?? job.state}</td>
                      <td className="p-3 tabular-nums whitespace-nowrap">{job.attempt} of {job.max_attempts}</td>
                      <td className="p-3 whitespace-nowrap text-muted-foreground">{when(job.created_at)}</td>
                      <td className="p-3 text-right whitespace-nowrap">
                        {canRetry && <Button variant="outline" size="sm" disabled={act.isPending} onClick={() => run(job, "retry")}>Run again</Button>}
                        {canCancel && (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="ml-1"
                            disabled={act.isPending}
                            onClick={async () => {
                              const ok = await confirm({
                                title: `Cancel job ${job.id}?`,
                                description: job.state === "running"
                                  ? "The panel stops waiting for it. Work already started on the node may still finish."
                                  : "It will not run.",
                                confirmLabel: "Cancel job",
                                destructive: true,
                              })
                              if (ok) run(job, "cancel")
                            }}
                          >
                            Cancel
                          </Button>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
        {data && data.jobs.length === 100 && <p className="text-xs text-muted-foreground mt-2">Showing the newest 100. Use a filter to narrow it down.</p>}
      </section>

      <section aria-labelledby="workers-heading" className="max-w-3xl">
        <h2 id="workers-heading" className="text-lg font-semibold">How many run at once</h2>
        <p className="text-sm text-muted-foreground mt-1 mb-4 max-w-prose">
          Each kind of work has its own lane, so a long backup never holds up starting a VM. Raise a number if jobs wait while nodes are idle; lower it if nodes are overloaded.
        </p>
        <form onSubmit={handleSaveWorkers} className="rounded-lg border bg-card shadow-sm">
          <div className="divide-y">
            {QUEUES.map((q) => (
              <div key={q.id} className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 p-4">
                <label htmlFor={`workers-${q.id}`} className="min-w-0 flex-1 basis-56">
                  <span className="font-medium block">{q.name}</span>
                  <span className="text-sm text-muted-foreground">{q.does}</span>
                </label>
                <div className="flex items-center gap-3">
                  <span className="text-xs text-muted-foreground whitespace-nowrap">running now: {data?.workers[q.id] ?? "–"}</span>
                  <Input
                    id={`workers-${q.id}`}
                    inputMode="numeric"
                    value={workers[q.id] ?? ""}
                    onChange={(e) => setWorkers((w) => ({ ...w, [q.id]: e.target.value }))}
                    className="w-20 font-mono"
                  />
                </div>
              </div>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-3 border-t p-4">
            <Button type="submit" disabled={saveWorkers.isPending}>
              {saveWorkers.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              Save limits
            </Button>
            <p className="text-sm text-muted-foreground" role="status">
              {pendingRestart ? "Saved limits differ from what is running. They take effect when the panel next restarts." : "Changes take effect when the panel next restarts."}
            </p>
          </div>
        </form>
      </section>
    </div>
  )
}
