"use client"

import { useId, useState, type ReactNode } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { BILLING_DELIVERIES, type BillingDelivery, useDeliveries, useRetryDelivery } from "@/lib/hooks/use-billing-deliveries"

// A state is a lamp and a word.
function stateOf(d: BillingDelivery): { word: string; lamp: string } {
  if (d.status === "delivered") return { word: "Delivered", lamp: "bg-success" }
  if (d.status === "failed") return { word: "Failed", lamp: "bg-destructive" }
  if (d.status === "retrying") return { word: "Retrying", lamp: "bg-warning" }
  return { word: "Waiting", lamp: "bg-muted-foreground" }
}

const EVENT_WORDS: Record<string, string> = {
  "vm.created": "Server created",
  "vm.failed": "Server installation failed",
  "vm.suspended": "Server suspended",
  "vm.unsuspended": "Server unsuspended",
  "vm.destroyed": "Server deleted",
  "bandwidth.overage": "Transfer quota exceeded",
  "floating_ip.usage": "Floating IP count changed",
  "backup.completed": "Backup finished",
  "backup.failed": "Backup failed",
  "alert.raised": "Alert opened",
  "alert.resolved": "Alert cleared",
  "user.created": "Account created",
  "user.suspended": "Account suspended",
  "user.unsuspended": "Account restored",
  "user.deleted": "Account deleted",
  "webhook.test": "Test event",
}

// What the event is about, from the body that was sent.
function subject(d: BillingDelivery): string {
  try {
    const b = JSON.parse(d.body) as { hostname?: string; external_ref?: string; address?: string; billable?: number; title?: string; email?: string }
    if (d.event === "floating_ip.usage") return `${b.billable ?? 0} billable${b.address ? `, ${b.address}` : ""}`
    if (d.event.startsWith("alert.")) return b.title ?? ""
    if (d.event.startsWith("user.")) return b.email ?? ""
    return [b.hostname, b.external_ref].filter(Boolean).join(", ")
  } catch {
    return ""
  }
}

const when = (iso: string) => new Date(iso).toLocaleString()

function outcome(d: BillingDelivery): string {
  const tries = `${d.attempts} ${d.attempts === 1 ? "try" : "tries"}`
  if (d.status === "delivered") return `Delivered ${d.delivered_at ? when(d.delivered_at) : ""} after ${tries}`
  if (d.status === "pending") return "Queued. It is sent within a few seconds."
  const reason = d.last_error || "no answer recorded"
  return d.status === "failed"
    ? `Gave up after ${tries}: ${reason}`
    : `${tries} so far, trying again with longer pauses: ${reason}`
}

// The log of events sent to the billing system's webhook, with a way to send
// a failed one again.
export function BillingDeliveries() {
  return (
    <DeliveryLog
      path={BILLING_DELIVERIES}
      heading="Events sent to your billing system"
      intro="Each event goes to the webhook URL above, signed with the HMAC secret. One that is not accepted is tried again up to 10 times over about four hours. The log is kept for 90 days."
      emptyHint="Nothing is sent, or recorded here, until a webhook URL is saved above."
      className="mt-6"
    />
  )
}

// One receiver's delivery log. canRetry hides "Send again" from people who
// may only read it.
export function DeliveryLog({ path, heading, intro, emptyHint, className, canRetry = true, as: Heading = "h2" }: {
  path: string
  heading: string
  as?: "h2" | "h3"
  intro: ReactNode
  emptyHint: string
  className?: string
  canRetry?: boolean
}) {
  const headingId = useId()
  const [failedOnly, setFailedOnly] = useState(false)
  const [page, setPage] = useState(1)
  const { data, isLoading, error, refetch, isPlaceholderData } = useDeliveries(path, failedOnly, page)
  const retry = useRetryDelivery(path)

  const rows = data?.deliveries ?? []
  const pages = data ? Math.max(1, Math.ceil(data.total / data.page_size)) : 1
  const show = (failed: boolean) => {
    setFailedOnly(failed)
    setPage(1)
  }
  const sendAgain = async (id: string) => {
    try {
      await retry.mutateAsync(id)
      toast.success("Queued to send again")
    } catch (err) {
      toast.error(`Could not queue it: ${(err as Error).message}`)
    }
  }

  return (
    <section aria-labelledby={headingId} className={className}>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <Heading id={headingId} className={Heading === "h2" ? "text-lg font-semibold" : "text-base font-semibold"}>{heading}</Heading>
          <p className="mt-1 max-w-prose text-sm text-muted-foreground">{intro}</p>
        </div>
        <div className="flex gap-2" role="group" aria-label="Which events to show">
          <Button type="button" size="sm" variant={failedOnly ? "outline" : "default"} aria-pressed={!failedOnly} onClick={() => show(false)}>
            All
          </Button>
          <Button type="button" size="sm" variant={failedOnly ? "default" : "outline"} aria-pressed={failedOnly} onClick={() => show(true)}>
            Failed
          </Button>
        </div>
      </div>

      <div className={cn("divide-y rounded-lg border bg-card shadow-sm", isPlaceholderData && "opacity-60")}>
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Loading events
          </p>
        ) : error && !data ? (
          <div className="p-5">
            <p className="text-sm font-medium">Could not load the events: {(error as Error).message}</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Retry</Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="p-5">
            <p className="text-sm font-medium">{failedOnly ? "No event has failed." : "No events sent yet."}</p>
            {!failedOnly && <p className="mt-1 text-sm text-muted-foreground">{emptyHint}</p>}
          </div>
        ) : (
          rows.map((d) => {
            const s = stateOf(d)
            const about = subject(d)
            return (
              <div key={d.id} className="flex flex-wrap items-start gap-x-4 gap-y-2 p-4 sm:p-5">
                <span className="inline-flex w-24 shrink-0 items-center gap-2 text-sm font-semibold">
                  <span className={cn("h-2 w-2 rounded-full", s.lamp)} aria-hidden="true" />
                  {s.word}
                </span>
                <div className="min-w-0 flex-1 basis-72">
                  <p className="font-medium">
                    {EVENT_WORDS[d.event] ?? d.event}
                    {about && <span className="font-normal text-muted-foreground"> ({about})</span>}
                  </p>
                  <p className="mt-0.5 max-w-prose break-words text-sm text-muted-foreground">{outcome(d)}</p>
                  <p className="mt-1.5 text-xs text-muted-foreground">
                    <span className="font-mono">{d.event}</span>, created {when(d.created_at)}
                  </p>
                </div>
                {canRetry && d.status !== "delivered" && (
                  <Button variant="outline" size="sm" className="shrink-0 self-center" disabled={retry.isPending} onClick={() => sendAgain(d.id)}>
                    Send again
                  </Button>
                )}
              </div>
            )
          })
        )}
      </div>

      {data && pages > 1 && (
        <div className="mt-3 flex items-center justify-between gap-3 text-sm">
          <p className="text-muted-foreground">Page {page} of {pages}, {data.total} events</p>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>Previous</Button>
            <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => setPage(page + 1)}>Next</Button>
          </div>
        </div>
      )}
    </section>
  )
}
