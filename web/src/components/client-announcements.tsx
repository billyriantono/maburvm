"use client"

import { useEffect, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useCurrentAnnouncements, useSetMarketing } from "@/lib/hooks/use-admin-extras"
import { useProfile } from "@/lib/hooks/use-settings"

const HIDDEN_KEY = "maburvm.hiddenAnnouncements"

function readHidden(): string[] {
  try {
    return JSON.parse(localStorage.getItem(HIDDEN_KEY) ?? "[]")
  } catch {
    return []
  }
}

/** The provider's announcements meant for this customer, newest first. Each can be hidden in this browser. */
export function ClientAnnouncements() {
  const { data } = useCurrentAnnouncements()
  const [hidden, setHidden] = useState<string[]>([])
  useEffect(() => setHidden(readHidden()), [])
  const shown = (data ?? []).filter((a) => !hidden.includes(a.id))
  if (shown.length === 0) return null

  const hide = (id: string) => {
    const next = [...hidden, id]
    setHidden(next)
    try {
      localStorage.setItem(HIDDEN_KEY, JSON.stringify(next.slice(-50)))
    } catch {
      // Private mode: hidden for this visit only.
    }
  }

  return (
    <section aria-label="Announcements" className="space-y-3">
      {shown.map((a) => (
        <article key={a.id} className="rounded-lg border bg-card p-4 text-card-foreground">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <h2 className="font-semibold">{a.title}</h2>
            <Button size="sm" variant="ghost" onClick={() => hide(a.id)} aria-label={`Hide the announcement ${a.title}`}>Hide</Button>
          </div>
          <p className="mt-1 whitespace-pre-wrap break-words text-sm">{a.body}</p>
          <p className="mt-2 text-xs text-muted-foreground">{new Date(a.starts_at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}</p>
        </article>
      ))}
    </section>
  )
}

/** Whether the customer gets news and offers by email. Service notices are sent either way. */
export function EmailPreferences({ disabled }: { disabled?: boolean }) {
  const { data: profile } = useProfile()
  const set = useSetMarketing()
  if (!profile) return null
  const on = !profile.marketing_opt_out
  return (
    <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
      <div className="border-b px-5 py-4">
        <h2 className="text-lg font-semibold">Email</h2>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3 p-5">
        <div className="min-w-0 flex-1 basis-64">
          <p className="font-medium">News and offers</p>
          <p className="text-sm text-muted-foreground">
            {on ? "You get them." : "You do not get them."} Notices about your servers, such as planned maintenance, are sent either way.
          </p>
        </div>
        <Button variant="outline" size="sm" disabled={disabled || set.isPending} onClick={() =>
          set.mutate(!on, {
            onSuccess: () => toast.success(on ? "No more news and offers by email" : "You will get news and offers by email"),
            onError: (err) => toast.error(err.message),
          })}>
          {on ? "Stop them" : "Get them"}
        </Button>
      </div>
    </section>
  )
}
