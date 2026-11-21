"use client"

import { useState } from "react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { ListPager } from "@/components/list-pager"
import { useMarkAllNotificationsRead, useMarkNotificationRead, useNotificationPage } from "@/lib/hooks/use-notifications"

const PAGE = 25

// Every notification the account was sent in the last 90 days, a page at a time.
export default function ClientNotificationsPage() {
  const [page, setPage] = useState(1)
  const { data, isLoading, isError, refetch } = useNotificationPage(page, PAGE)
  const markRead = useMarkNotificationRead()
  const markAll = useMarkAllNotificationsRead()

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Notifications</h1>
          <p className="text-sm text-muted-foreground">What happened to your servers and your account in the last 90 days.</p>
        </div>
        {!!data?.unread && (
          <Button variant="outline" disabled={markAll.isPending} onClick={() => markAll.mutate()}>Mark all {data.unread} read</Button>
        )}
      </div>
      <section className="rounded-lg border bg-card text-card-foreground">
        {isLoading ? (
          <p className="p-5 text-sm text-muted-foreground">Loading…</p>
        ) : isError ? (
          <div className="p-5">
            <p role="alert" className="text-sm text-destructive">Notifications could not be loaded.</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !data?.items.length ? (
          <p className="p-5 text-sm text-muted-foreground">Nothing yet.</p>
        ) : (
          <ul className="divide-y">
            {data.items.map((n) => (
              <li key={n.id} className="flex flex-wrap items-baseline gap-x-4 gap-y-1 px-5 py-3">
                <span className={`min-w-0 flex-1 text-sm ${n.read_at ? "text-muted-foreground" : "font-medium"}`}>
                  {n.resource_type === "vm" && n.resource_id ? <Link href={`/client/vms/${n.resource_id}`} className="hover:underline">{n.title}</Link> : n.title}
                </span>
                <time className="text-xs text-muted-foreground tabular-nums" dateTime={n.created_at}>{new Date(n.created_at).toLocaleString()}</time>
                {!n.read_at && (
                  <Button size="sm" variant="ghost" disabled={markRead.isPending} onClick={() => markRead.mutate(n.id)}>Mark read</Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
      <ListPager page={page} pageSize={PAGE} total={data?.total ?? 0} onPage={setPage} noun="notifications" />
    </div>
  )
}
