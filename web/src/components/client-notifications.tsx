"use client"

import { useRouter } from "next/navigation"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useMarkAllNotificationsRead, useMarkNotificationRead, useNotifications, type UserNotification } from "@/lib/hooks/use-notifications"

// Where a notification leads: its server's page, when it is about one.
function hrefOf(n: UserNotification): string | null {
  return n.resource_type === "vm" && n.resource_id ? `/client/vms/${n.resource_id}` : null
}

// The customer's notifications: their servers ready, suspended, a backup that
// failed, a change to how their account is signed in to.
export function ClientNotifications() {
  const router = useRouter()
  const { data, isLoading, isError, refetch } = useNotifications()
  const markRead = useMarkNotificationRead()
  const markAll = useMarkAllNotificationsRead()
  const items = data?.items ?? []
  const unread = data?.unread ?? 0

  const open = (n: UserNotification) => {
    if (!n.read_at) markRead.mutate(n.id)
    const href = hrefOf(n)
    if (href) router.push(href)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" className="gap-2 font-medium" aria-label={unread > 0 ? `Notifications, ${unread} unread` : "Notifications"}>
          Notifications
          {unread > 0 && (
            <span className="min-w-5 rounded-sm bg-destructive px-1.5 text-xs font-semibold tabular-nums text-destructive-foreground">{unread}</span>
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between gap-2 pr-1">
          <DropdownMenuLabel className="text-xs font-semibold text-muted-foreground">Notifications</DropdownMenuLabel>
          {unread > 0 && (
            <Button variant="ghost" size="sm" className="h-7 text-xs" disabled={markAll.isPending} onClick={() => markAll.mutate()}>
              Mark all as read
            </Button>
          )}
        </div>
        <DropdownMenuSeparator />
        <div className="max-h-80 overflow-y-auto">
          {isLoading ? (
            <p className="px-2 py-4 text-center text-xs text-muted-foreground">Loading notifications</p>
          ) : isError ? (
            <div className="px-2 py-4 text-center text-xs">
              <p className="text-destructive">Could not load notifications.</p>
              <Button variant="outline" size="sm" className="mt-2" onClick={() => refetch()}>Try again</Button>
            </div>
          ) : items.length === 0 ? (
            <p className="px-2 py-4 text-center text-xs text-muted-foreground">
              Nothing yet. You are told here when a server is ready or suspended, or a backup fails.
            </p>
          ) : (
            items.map((n) => (
              <DropdownMenuItem key={n.id} className="flex cursor-pointer flex-col items-start gap-1 py-3" onSelect={() => open(n)}>
                <span className="flex w-full items-start gap-2">
                  {!n.read_at && <span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-primary" aria-hidden="true" />}
                  <span className={`text-sm ${n.read_at ? "text-muted-foreground" : "font-medium"}`}>
                    {n.title}
                    {!n.read_at && <span className="sr-only"> (unread)</span>}
                  </span>
                </span>
                <span className="text-[11px] text-muted-foreground tabular-nums">{new Date(n.created_at).toLocaleString()}</span>
              </DropdownMenuItem>
            ))
          )}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
