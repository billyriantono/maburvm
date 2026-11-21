"use client"

import { useState } from "react"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"

// One email the panel sent a customer, or is still trying to.
interface LoggedEmail {
  id: string
  template: string
  recipient: string
  subject: string
  status: "queued" | "retrying" | "sent" | "failed"
  attempts: number
  last_error: string
  created_at: string
  sent_at: string | null
}

interface EmailPage {
  emails: LoggedEmail[]
  total: number
  page: number
  page_size: number
}

function stateOf(e: LoggedEmail): { word: string; lamp: string } {
  if (e.status === "sent") return { word: "Sent", lamp: "bg-success" }
  if (e.status === "failed") return { word: "Failed", lamp: "bg-destructive" }
  if (e.status === "retrying") return { word: "Retrying", lamp: "bg-warning" }
  return { word: "Waiting", lamp: "bg-muted-foreground" }
}

const when = (iso: string) => new Date(iso).toLocaleString()

function outcome(e: LoggedEmail): string {
  const tries = `${e.attempts} ${e.attempts === 1 ? "try" : "tries"}`
  if (e.status === "sent") return `Sent ${e.sent_at ? when(e.sent_at) : ""} after ${tries}`
  if (e.status === "queued") return "Queued. It is sent within a few seconds."
  const reason = e.last_error || "no answer recorded"
  return e.status === "failed" ? `Gave up after ${tries}: ${reason}` : `${tries} so far, trying again with longer pauses: ${reason}`
}

// The log of emails sent to customers, with a way to send a failed one again.
export function EmailLog() {
  const queryClient = useQueryClient()
  const [failedOnly, setFailedOnly] = useState(false)
  const [page, setPage] = useState(1)
  const { data, isLoading, error, refetch, isPlaceholderData } = useQuery<EmailPage>({
    queryKey: ["email-log", failedOnly, page],
    queryFn: async () =>
      (await api.get<EmailPage>(`/api/v1/settings/system/emails?page=${page}&page_size=10${failedOnly ? "&status=failed" : ""}`)).data.data,
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
  const retry = useMutation<void, Error, string>({
    mutationFn: async (id) => {
      await api.post(`/api/v1/settings/system/emails/${id}/retry`)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["email-log"] }),
  })

  const rows = data?.emails ?? []
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
    <section aria-labelledby="email-log-heading" className="mt-6">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="email-log-heading" className="text-lg font-semibold">Emails sent to customers</h2>
          <p className="mt-1 max-w-prose text-sm text-muted-foreground">
            An email the mail server does not accept is tried again up to 8 times over about an hour. The log is kept
            for 90 days. Test sends and alerts to staff are not listed here.
          </p>
        </div>
        <div className="flex gap-2" role="group" aria-label="Which emails to show">
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
            Loading emails
          </p>
        ) : error && !data ? (
          <div className="p-5">
            <p className="text-sm font-medium">Could not load the email log: {(error as Error).message}</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>Retry</Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="p-5">
            <p className="text-sm font-medium">{failedOnly ? "No email has failed." : "No emails sent yet."}</p>
            {!failedOnly && (
              <p className="mt-1 text-sm text-muted-foreground">Nothing is sent, or recorded here, until a mail server is saved above.</p>
            )}
          </div>
        ) : (
          rows.map((e) => {
            const s = stateOf(e)
            return (
              <div key={e.id} className="flex flex-wrap items-start gap-x-4 gap-y-2 p-4 sm:p-5">
                <span className="inline-flex w-24 shrink-0 items-center gap-2 text-sm font-semibold">
                  <span className={cn("h-2 w-2 rounded-full", s.lamp)} aria-hidden="true" />
                  {s.word}
                </span>
                <div className="min-w-0 flex-1 basis-72">
                  <p className="font-medium break-words">{e.subject}</p>
                  <p className="mt-0.5 text-sm">
                    To <span className="font-mono">{e.recipient}</span>
                  </p>
                  <p className="mt-0.5 max-w-prose break-words text-sm text-muted-foreground">{outcome(e)}</p>
                  <p className="mt-1.5 text-xs text-muted-foreground">
                    <span className="font-mono">{e.template}</span>, created {when(e.created_at)}
                  </p>
                </div>
                {e.status === "failed" && (
                  <Button variant="outline" size="sm" className="shrink-0 self-center" disabled={retry.isPending} onClick={() => sendAgain(e.id)}>
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
          <p className="text-muted-foreground">Page {page} of {pages}, {data.total} emails</p>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>Previous</Button>
            <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => setPage(page + 1)}>Next</Button>
          </div>
        </div>
      )}
    </section>
  )
}
