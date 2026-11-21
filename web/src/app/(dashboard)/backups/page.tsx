"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useNodes } from "@/lib/hooks/use-nodes"
import { useBackupOverview } from "@/lib/hooks/use-backups"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import type { BackupDestinationTotal, BackupOverviewRow, BackupState } from "@/types"

const STATES: { state: BackupState; word: string; lamp: string }[] = [
  { state: "failing", word: "Failing", lamp: "bg-destructive" },
  { state: "stale", word: "Stale", lamp: "bg-warning" },
  { state: "unscheduled", word: "No schedule", lamp: "bg-muted-foreground" },
  { state: "ok", word: "Up to date", lamp: "bg-success" },
]
const stateOf = (s: BackupState) => STATES.find((x) => x.state === s) ?? STATES[3]

const KIND_WORDS: Record<BackupDestinationTotal["kind"], string> = {
  s3: "S3 bucket",
  sftp: "SFTP server",
  path: "Directory on the nodes",
  node: "Kept by each node",
}

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : "")

function chainWords(r: BackupOverviewRow): string {
  if (r.full_backups + r.incremental_backups === 0) return "None kept"
  const full = `${r.full_backups} full`
  return r.incremental_backups ? `${full}, ${r.incremental_backups} incremental` : full
}

function scheduleWords(r: BackupOverviewRow): string {
  if (!r.schedule) return "None"
  const kind = r.full_every_days > 0 ? `incremental, full every ${r.full_every_days} days` : "full each time"
  return `${r.schedule} (${kind})${r.schedule_status && r.schedule_status !== "active" ? `, ${r.schedule_status}` : ""}`
}

export default function BackupsPage() {
  const [state, setState] = useState<BackupState | "">("")
  const [nodeId, setNodeId] = useState("")
  const [q, setQ] = useState("")
  const [staleDays, setStaleDays] = useState(2)
  const { data: nodes } = useNodes()
  const { data, isLoading, error, refetch, isPlaceholderData } = useBackupOverview({ state, node_id: nodeId, q, stale_days: staleDays })

  const rows = data?.rows ?? []
  const total = STATES.reduce((n, s) => n + (data?.counts[s.state] ?? 0), 0)

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Backups</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          Every VM and how recently it was backed up. Failing and stale ones come first. Open a VM to take, restore or delete
          its backups.
        </p>
      </div>

      <section aria-labelledby="vms-heading" className="space-y-3">
        <h2 id="vms-heading" className="sr-only">VMs</h2>
        <div className="flex flex-wrap gap-2" role="group" aria-label="Show VMs by state">
          <Button type="button" size="sm" variant={state === "" ? "default" : "outline"} aria-pressed={state === ""} onClick={() => setState("")}>
            All {data ? total : ""}
          </Button>
          {STATES.map((s) => (
            <Button key={s.state} type="button" size="sm" variant={state === s.state ? "default" : "outline"} aria-pressed={state === s.state}
              onClick={() => setState(s.state)}>
              <span className={cn("h-2 w-2 rounded-full", s.lamp)} aria-hidden="true" />
              {s.word} {data ? data.counts[s.state] ?? 0 : ""}
            </Button>
          ))}
        </div>
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-0 flex-1 basis-56">
            <label htmlFor="bk-q" className="mb-1 block text-xs text-muted-foreground">Hostname or owner email</label>
            <Input id="bk-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search" autoComplete="off" />
          </div>
          <div>
            <label htmlFor="bk-node" className="mb-1 block text-xs text-muted-foreground">Node</label>
            <select id="bk-node" value={nodeId} onChange={(e) => setNodeId(e.target.value)}
              className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="">All nodes</option>
              {(nodes ?? []).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
            </select>
          </div>
          <div>
            <label htmlFor="bk-stale" className="mb-1 block text-xs text-muted-foreground">Stale after</label>
            <select id="bk-stale" value={staleDays} onChange={(e) => setStaleDays(Number(e.target.value))}
              className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              {[1, 2, 3, 7, 14].map((d) => <option key={d} value={d}>{d === 1 ? "1 day" : `${d} days`} without a success</option>)}
            </select>
          </div>
        </div>

        <div className={cn("overflow-x-auto rounded-lg border bg-card shadow-sm", isPlaceholderData && "opacity-60")}>
          {isLoading ? (
            <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              Loading backups
            </p>
          ) : error && !data ? (
            <div className="p-5">
              <p className="text-sm font-medium">Could not load the backups: {(error as Error).message}</p>
              <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Retry</Button>
            </div>
          ) : rows.length === 0 ? (
            <div className="p-5">
              <p className="text-sm font-medium">{total === 0 ? "No VMs yet." : "No VM matches these filters."}</p>
              {total > 0 && <p className="mt-1 text-sm text-muted-foreground">Choose All, or clear the search.</p>}
            </div>
          ) : (
            <table className="w-full min-w-[960px] text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-4 py-2 font-medium">VM</th>
                  <th scope="col" className="px-4 py-2 font-medium">State</th>
                  <th scope="col" className="px-4 py-2 font-medium">Last success</th>
                  <th scope="col" className="px-4 py-2 font-medium">Last result</th>
                  <th scope="col" className="px-4 py-2 font-medium">Schedule</th>
                  <th scope="col" className="px-4 py-2 font-medium">Kept</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-hair">
                {rows.map((r) => {
                  const s = stateOf(r.state)
                  return (
                    <tr key={r.vm_id} className="align-top">
                      <td className="px-4 py-3">
                        <Link href={`/vms/${r.vm_id}?tab=backups`} className="font-medium underline-offset-4 hover:underline">{r.hostname}</Link>
                        <p className="text-xs text-muted-foreground">{r.owner_email || "No owner"}</p>
                        <p className="text-xs text-muted-foreground">{r.node_name || "No node"}</p>
                      </td>
                      <td className="px-4 py-3">
                        <span className="inline-flex items-center gap-2 font-medium">
                          <span className={cn("h-2 w-2 rounded-full", s.lamp)} aria-hidden="true" />
                          {s.word}
                        </span>
                        {(r.running > 0 || r.queued > 0) && (
                          <p className="text-xs text-muted-foreground">
                            {r.running > 0 ? "Backing up now" : "Queued"}
                          </p>
                        )}
                      </td>
                      <td className="px-4 py-3 tabular-nums">
                        {r.last_success_at ? (
                          <>
                            <p>{when(r.last_success_at)}</p>
                            <p className="text-xs text-muted-foreground">
                              {formatBytes(r.last_success_size)}, {r.last_success_mode === "incremental" ? "incremental" : "full"}
                            </p>
                          </>
                        ) : (
                          <span className="text-muted-foreground">Never</span>
                        )}
                      </td>
                      <td className="max-w-xs px-4 py-3">
                        {r.last_result === "failed" ? (
                          <>
                            <p className="font-medium text-destructive">Failed {when(r.last_result_at)}</p>
                            {r.last_error && <p className="break-words text-xs text-muted-foreground">{r.last_error}</p>}
                          </>
                        ) : r.last_result ? (
                          <p>Completed</p>
                        ) : (
                          <span className="text-muted-foreground">No backup finished yet</span>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <p className="break-words">{scheduleWords(r)}</p>
                        {r.next_run_at && r.schedule_status === "active" && (
                          <p className="text-xs text-muted-foreground">Next {when(r.next_run_at)}</p>
                        )}
                      </td>
                      <td className="px-4 py-3 tabular-nums">
                        <p>{chainWords(r)}</p>
                        {r.stored_bytes > 0 && <p className="text-xs text-muted-foreground">{formatBytes(r.stored_bytes)}</p>}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      </section>

      {data && (
        <div className="grid gap-8 lg:grid-cols-2">
          <section aria-labelledby="dest-heading">
            <h2 id="dest-heading" className="text-lg font-semibold">Where backups are kept</h2>
            <p className="mb-3 mt-1 text-sm text-muted-foreground">
              Completed backups in each destination. New backups go to the default; change it under{" "}
              <Link href="/settings/system" className="underline underline-offset-4">System settings</Link>.
            </p>
            <div className="divide-y divide-hair rounded-lg border bg-card shadow-sm">
              {data.destinations.length === 0 ? (
                <p className="p-4 text-sm text-muted-foreground">
                  No destination is set, and no backup is kept yet. Add one under System settings before scheduling backups.
                </p>
              ) : (
                data.destinations.map((d) => (
                  <div key={d.id ?? "node"} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 p-4">
                    <div className="min-w-0">
                      <p className="font-medium">{d.name}{d.is_default && <span className="ml-2 text-xs font-normal text-muted-foreground">Default</span>}</p>
                      <p className="text-xs text-muted-foreground">{KIND_WORDS[d.kind]}</p>
                    </div>
                    <p className="text-sm tabular-nums">{d.backups} {d.backups === 1 ? "backup" : "backups"}, {formatBytes(d.bytes)}</p>
                  </div>
                ))
              )}
            </div>
          </section>

          <section aria-labelledby="nodes-heading">
            <h2 id="nodes-heading" className="text-lg font-semibold">Backing up now</h2>
            <p className="mb-3 mt-1 text-sm text-muted-foreground">
              Backups running and waiting on each node. A node runs at most its limit at once; the rest wait their turn.
            </p>
            <div className="divide-y divide-hair rounded-lg border bg-card shadow-sm">
              {data.nodes.length === 0 ? (
                <p className="p-4 text-sm text-muted-foreground">No nodes yet.</p>
              ) : (
                data.nodes.map((n) => (
                  <div key={n.node_id} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 p-4">
                    <p className="font-medium">{n.name}</p>
                    <p className="text-sm tabular-nums">
                      {n.running} of {n.limit} running, {n.queued} waiting
                    </p>
                  </div>
                ))
              )}
            </div>
          </section>
        </div>
      )}
    </div>
  )
}
