"use client"

import { AlertTriangle, Loader2, RefreshCw, ShieldCheck, ShieldX } from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useIPReputation, useCheckReputationNow, type IPReputation } from "@/lib/hooks/use-reputation"
import { useState } from "react"

// Reputation of the addresses we hand to customers.
//
// An address keeps its reputation after the abuse that earned it has stopped.
// One of ours was used to scan the internet at 90k packets/sec and was handed to
// a paying customer days later, complete with whatever listings that earned —
// the customer sees mail rejections and endless CAPTCHAs while the panel shows a
// healthy VM. This page is the missing link between the two.
export default function IPReputationPage() {
  const [showAll, setShowAll] = useState(false)
  // Always load every record and narrow it here. The summary below has to
  // count the checks that could not be completed, and the server's "flagged
  // only" view leaves exactly those out.
  const { data: records, isLoading, isError, error, refetch } = useIPReputation(true)
  const checkNow = useCheckReputationNow()

  const all = records ?? []
  const flagged = all.filter((r) => (r.listings ?? []).length > 0 || r.abuse_score > 0)
  const incomplete = all.filter((r) => r.check_error)
  const unscored = all.filter((r) => r.abuse_score < 0)
  const rows = showAll ? all : flagged
  // The reason most checks share, so one sentence can explain the whole fleet.
  const topReason = (() => {
    const counts = new Map<string, number>()
    for (const r of incomplete) counts.set(r.check_error, (counts.get(r.check_error) ?? 0) + 1)
    return [...counts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0]
  })()

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold flex items-center gap-2">
            <ShieldX className="w-6 h-6" />
            IP Reputation
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            What the internet currently thinks of our address space. An address burned by abuse
            keeps its listings long after the abuse stops — and the next customer to receive it
            inherits them.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button type="button" variant="outline" onClick={() => setShowAll(!showAll)}>
            {showAll ? "Show flagged only" : "Show every address"}
          </Button>
          <Button
            type="button"
            disabled={checkNow.isPending}
            onClick={async () => {
              try {
                const res = await checkNow.mutateAsync()
                toast.success(res.checked === 0 ? "No addresses were due for a check" : `${res.checked} addresses checked`)
              } catch (err) {
                toast.error((err as Error).message)
              }
            }}
          >
            {checkNow.isPending ? (
              <Loader2 className="w-4 h-4 animate-spin" />
            ) : (
              <>
                <RefreshCw className="w-4 h-4 mr-1" />
                Check now
              </>
            )}
          </Button>
        </div>
      </div>

      {/* A check that could not be completed says nothing about the address.
          Without this, a fleet where every lookup was refused reads as clean. */}
      {!isLoading && !isError && incomplete.length > 0 && (
        <div role="status" className="mb-4 flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm dark:border-amber-900 dark:bg-amber-950">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-medium text-foreground">
              {incomplete.length === all.length
                ? `None of the ${all.length} checked addresses could be fully checked.`
                : `${incomplete.length} of ${all.length} checked addresses could not be fully checked.`}
            </p>
            {topReason && <p className="mt-1 break-words text-muted-foreground">Most common reason: {topReason}</p>}
            <p className="mt-1 text-muted-foreground">
              Those addresses are not known to be clean. They are checked again within a day, or use Check now.
            </p>
          </div>
        </div>
      )}
      {!isLoading && !isError && all.length > 0 && unscored.length === all.length && (
        <p className="mb-4 text-sm text-muted-foreground">
          Abuse scores are not collected: no AbuseIPDB key is set. Add one under{" "}
          <a href="/settings/system" className="font-medium text-foreground underline underline-offset-4">System settings, API</a>.
          Blocklist checks work without it.
        </p>
      )}

      {isLoading ? (
        <div className="flex items-center justify-center gap-3 py-20 text-sm text-muted-foreground">
          <Loader2 className="w-5 h-5 animate-spin" aria-hidden="true" />
          Loading reputation records
        </div>
      ) : isError ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <p className="font-medium text-destructive">Could not load reputation records</p>
          <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
          <Button type="button" variant="outline" className="mt-4" onClick={() => refetch()}>
            <RefreshCw className="w-4 h-4" aria-hidden="true" />Retry
          </Button>
        </div>
      ) : rows.length === 0 ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <ShieldCheck className="w-16 h-16 text-emerald-500/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">
            {all.length === 0 ? "Nothing checked yet" : "Nothing flagged"}
          </p>
          <p className="text-sm text-muted-foreground mt-1">
            {all.length === 0
              ? "Addresses are checked a few at a time in the background. Use Check now to start."
              : incomplete.length > 0
                ? `No listing was found, but ${incomplete.length} of ${all.length} checks were incomplete, so this is not a clean result yet.`
                : `None of the ${all.length} checked addresses is listed on a blocklist or carrying an abuse score.`}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-3">Address</div>
            <div className="col-span-3">Used by</div>
            <div className="col-span-3">Listed on</div>
            <div className="col-span-2 text-right">Abuse score</div>
            <div className="col-span-1 text-right">Checked</div>
          </div>
          {rows.map((r) => (
            <ReputationRow key={r.id} record={r} />
          ))}
        </div>
      )}
    </div>
  )
}

function ReputationRow({ record }: { record: IPReputation }) {
  const listings = record.listings ?? []
  const listed = listings.length > 0
  // -1 means the score was never obtained. Rendering that as 0 would report an
  // unchecked address as clean, which is the one mistake this page must not make.
  const scored = record.abuse_score >= 0

  return (
    <div className="grid grid-cols-12 gap-3 items-start p-4 border-b last:border-0">
      <div className="col-span-3 min-w-0">
        <div className="font-mono font-medium">{record.address}</div>
        <div className="text-xs text-muted-foreground truncate">{record.pool_name || "—"}</div>
      </div>

      <div className="col-span-3 min-w-0 text-sm">
        {record.vm_hostname ? (
          <span className="truncate block">{record.vm_hostname}</span>
        ) : record.assigned ? (
          <span className="text-muted-foreground">assigned</span>
        ) : (
          // Worth distinguishing: a listing on an unused address costs nothing
          // today, but hands the problem to whoever receives it next.
          <span className="text-muted-foreground">free — will be handed out</span>
        )}
      </div>

      <div className="col-span-3">
        {listed ? (
          <div className="flex flex-wrap gap-1">
            {listings.map((zone) => (
              <Badge key={zone} variant="destructive" className="font-mono text-[10px]">
                {zone}
              </Badge>
            ))}
          </div>
        ) : record.check_error ? (
          <span className="text-xs text-amber-600 flex items-start gap-1">
            <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
            {/* Not "clean". A blocklist that refuses the query has told us
                nothing, and saying otherwise is how a provider convinces itself
                its space is fine while customers' mail bounces. */}
            <span className="break-words">could not check</span>
          </span>
        ) : (
          <span className="text-xs text-emerald-600">not listed</span>
        )}
        {record.check_error && (
          <p className="text-[10px] text-muted-foreground mt-1 break-words">{record.check_error}</p>
        )}
      </div>

      <div className="col-span-2 text-right">
        {scored ? (
          <span
            className={`font-mono font-semibold ${
              record.abuse_score >= 50
                ? "text-red-600"
                : record.abuse_score > 0
                  ? "text-amber-600"
                  : "text-muted-foreground"
            }`}
          >
            {record.abuse_score}%
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">not checked</span>
        )}
        {scored && record.total_reports > 0 && (
          <p className="text-[10px] text-muted-foreground">{record.total_reports} reports</p>
        )}
      </div>

      <div className="col-span-1 text-right text-[10px] text-muted-foreground">
        {new Date(record.checked_at).toLocaleDateString()}
      </div>
    </div>
  )
}
