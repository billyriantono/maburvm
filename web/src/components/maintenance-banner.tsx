"use client"

import Link from "next/link"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { useDismissUpdate, useMaintenance, useUpdateStatus } from "@/lib/hooks/use-admin-extras"

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "")

/**
 * The strip under the header while the panel is in maintenance mode. Customers
 * read why their changes wait; staff read that customers are held, and where
 * the switch is.
 */
export function MaintenanceBanner({ staff }: { staff: boolean }) {
  const { data } = useMaintenance()
  if (!data?.enabled) return null
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-warning/60 bg-warning/15 px-4 py-2 text-sm lg:px-8">
      <span className="h-2 w-2 shrink-0 rounded-full bg-warning" aria-hidden="true" />
      {staff ? (
        <span>
          <span className="font-semibold">Maintenance mode</span> since {when(data.since)}
          {data.by ? ` (${data.by})` : ""}: customers and the billing system cannot make changes
          {data.hold_jobs ? ", and new jobs wait" : ""}. Staff carry on.{" "}
          <Link href="/settings/maintenance" className="font-medium underline underline-offset-4">
            Maintenance settings
          </Link>
        </span>
      ) : (
        <span>
          <span className="font-semibold">Maintenance</span>: {data.message} You can look around; changes are paused until it ends.
        </span>
      )}
    </div>
  )
}

/** The Owner's notice of a newer release, with its notes and the way to upgrade. */
export function UpdateNotice({ owner }: { owner: boolean }) {
  const { data } = useUpdateStatus(owner)
  const dismiss = useDismissUpdate()
  const [open, setOpen] = useState(false)
  const st = data?.status
  if (!owner || !st?.notice || !st.latest) return null
  const docs = `https://github.com/${st.repository}/blob/main/docs/upgrading.md`
  return (
    <div role="status" className="border-b bg-card px-4 py-2 text-sm lg:px-8">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className={`h-2 w-2 shrink-0 rounded-full ${st.security ? "bg-destructive" : "bg-muted-foreground"}`} aria-hidden="true" />
        <span className="min-w-0 flex-1">
          <span className="font-semibold">{st.latest.name || st.latest.tag}</span> is out; this panel runs {st.current}.
          {st.security && <span className="font-semibold text-destructive"> It fixes a security problem.</span>}
        </span>
        <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          {open ? "Hide release notes" : "Release notes"}
        </Button>
        <a href={docs} target="_blank" rel="noreferrer" className="font-medium underline underline-offset-4">
          How to upgrade
        </a>
        <Button size="sm" variant="outline" disabled={dismiss.isPending} onClick={() => dismiss.mutate(st.latest!.tag)}>
          Dismiss
        </Button>
      </div>
      {open && (
        <div className="mt-2 max-h-72 overflow-y-auto rounded-md border bg-background p-3">
          <pre className="whitespace-pre-wrap break-words font-sans text-sm">{st.latest.notes || "No notes were published with this release."}</pre>
          <a href={st.latest.url} target="_blank" rel="noreferrer" className="mt-2 inline-block text-sm font-medium underline underline-offset-4">
            Open the release
          </a>
        </div>
      )}
    </div>
  )
}
