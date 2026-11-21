"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { useConfirm } from "@/components/confirm-provider"
import { AlertSettings } from "@/components/alert-settings"
import { type Alert, useAlerts, useCloseAlert } from "@/lib/hooks/use-alerts"

// A state is a lamp and a word. An open alert says how bad; a closed one says
// how it ended.
function stateOf(a: Alert): { word: string; lamp: string } {
  if (a.state === "resolved") return { word: "Resolved", lamp: "bg-success" }
  if (a.state === "event") return { word: "Failed", lamp: "bg-destructive" }
  return a.severity === "critical" ? { word: "Critical", lamp: "bg-destructive" } : { word: "Warning", lamp: "bg-warning" }
}

function resourceLink(a: Alert): string | null {
  if (!a.resource_id) return null
  if (a.resource_type === "node") return `/nodes/${a.resource_id}`
  if (a.resource_type === "vm") return `/vms/${a.resource_id}`
  if (a.resource_type === "storage_pool") return "/storage"
  return null
}

const when = (iso: string) => new Date(iso).toLocaleString()

export default function AlertsPage() {
  const confirm = useConfirm()
  const [openOnly, setOpenOnly] = useState(true)
  const [page, setPage] = useState(1)
  const { data, isLoading, error, refetch, isPlaceholderData } = useAlerts(openOnly, page)
  const close = useCloseAlert()

  const alerts = data?.alerts ?? []
  const pages = data ? Math.max(1, Math.ceil(data.total / data.page_size)) : 1
  const show = (open: boolean) => {
    setOpenOnly(open)
    setPage(1)
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Alerts</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          What the panel noticed and told you about: nodes that stopped answering, storage filling up, backups and jobs
          that failed for good.
        </p>
      </div>

      <section aria-labelledby="alerts-heading">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <h2 id="alerts-heading" className="text-lg font-semibold">{openOnly ? "Open now" : "History"}</h2>
          <div className="flex gap-2" role="group" aria-label="Which alerts to show">
            <Button type="button" size="sm" variant={openOnly ? "default" : "outline"} aria-pressed={openOnly} onClick={() => show(true)}>
              Open
            </Button>
            <Button type="button" size="sm" variant={openOnly ? "outline" : "default"} aria-pressed={!openOnly} onClick={() => show(false)}>
              All
            </Button>
          </div>
        </div>

        <div className={cn("divide-y rounded-lg border bg-card shadow-sm", isPlaceholderData && "opacity-60")}>
          {isLoading ? (
            <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              Loading alerts
            </p>
          ) : error && !data ? (
            <div className="p-5">
              <p className="text-sm font-medium">Could not load the alerts: {(error as Error).message}</p>
              <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Retry</Button>
            </div>
          ) : alerts.length === 0 ? (
            <div className="p-5">
              <p className="text-sm font-medium">{openOnly ? "No alert is open." : "No alerts yet."}</p>
              <p className="mt-1 text-sm text-muted-foreground">
                {openOnly
                  ? "Nodes are checked once a minute, storage pools as often. An alert stays here until the problem has stayed away for the settle time."
                  : "Alerts are recorded here whether or not delivery is set up below. Closed ones are kept for 90 days."}
              </p>
            </div>
          ) : (
            alerts.map((a) => {
              const s = stateOf(a)
              const link = resourceLink(a)
              return (
                <div key={a.id} className="flex flex-wrap items-start gap-x-4 gap-y-2 p-4 sm:p-5">
                  <span className="inline-flex w-24 shrink-0 items-center gap-2 text-sm font-semibold">
                    <span className={cn("h-2 w-2 rounded-full", s.lamp)} aria-hidden="true" />
                    {s.word}
                  </span>
                  <div className="min-w-0 flex-1 basis-72">
                    <p className="font-medium">{a.title}</p>
                    {a.detail && <p className="mt-0.5 max-w-prose break-words text-sm text-muted-foreground">{a.detail}</p>}
                    <p className="mt-1.5 text-xs text-muted-foreground">
                      {a.state === "event" ? "Happened" : "Opened"} {when(a.fired_at)}
                      {a.resolved_at && `, closed ${when(a.resolved_at)}`}
                      {a.state === "firing" && a.clear_since && `. Clear since ${when(a.clear_since)}, closes if it stays clear`}
                    </p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {a.delivery || (a.notified_at ? "Sent" : "Not sent yet. Alerts go out within a minute.")}
                    </p>
                  </div>
                  <div className="flex shrink-0 items-center gap-3 self-center">
                    {link && (
                      <Link href={link} className="text-sm font-medium underline underline-offset-4">
                        Open {a.resource_type === "storage_pool" ? "storage" : a.resource_name || a.resource_type}
                      </Link>
                    )}
                    {a.state === "firing" && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() =>
                          confirm({
                            title: "Close this alert?",
                            description:
                              "It moves to the history and no recovery message is sent. If the problem is still there at the next check, a new alert opens and is sent again.",
                            confirmLabel: "Close alert",
                            action: () => close.mutateAsync(a.id),
                          })
                        }
                      >
                        Close alert
                      </Button>
                    )}
                  </div>
                </div>
              )
            })
          )}
        </div>

        {data && pages > 1 && (
          <div className="mt-3 flex items-center justify-between gap-3 text-sm">
            <p className="text-muted-foreground">Page {page} of {pages}, {data.total} alerts</p>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>Previous</Button>
              <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => setPage(page + 1)}>Next</Button>
            </div>
          </div>
        )}
      </section>

      <section aria-labelledby="delivery-heading">
        <h2 id="delivery-heading" className="text-lg font-semibold">Delivery</h2>
        <p className="mb-4 mt-1 max-w-prose text-sm text-muted-foreground">
          Where alerts are sent and what raises one. An alert is sent once when it opens and once when it closes; several at
          the same moment arrive as one message.
        </p>
        <div className="rounded-lg border bg-card p-5 shadow-sm sm:p-6">
          <AlertSettings />
        </div>
      </section>
    </div>
  )
}
