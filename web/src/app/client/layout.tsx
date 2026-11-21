"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { LogOut, Loader2 } from "lucide-react"
import { ClientSidebar, clientPages } from "@/components/client-sidebar"
import { GlobalSearch } from "@/components/global-search"
import { Button } from "@/components/ui/button"
import { ThemeToggle } from "@/components/theme-toggle"
import { ClientNotifications } from "@/components/client-notifications"
import { MaintenanceBanner } from "@/components/maintenance-banner"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useAuth, useLogout } from "@/lib/hooks/use-auth"
import { useEndImpersonation } from "@/lib/hooks/use-staff"
import { toast } from "sonner"

const clientVmHref = (id: string) => `/client/vms/${id}`

export default function ClientLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const router = useRouter()
  const { data: user, isLoading, error, isError } = useAuth()
  const logout = useLogout()
  const endView = useEndImpersonation()

  useEffect(() => {
    if (isError && !isLoading) {
      router.replace("/login")
    }
  }, [isError, isLoading, router])

  // Admins don't belong in the client area — send them to the admin dashboard.
  useEffect(() => {
    if (user && user.role === "admin") {
      router.replace("/dashboard")
    }
  }, [user, router])

  // A full page load after, so every query starts again as the staff account.
  const handleEndView = () => {
    endView.mutate(undefined, {
      onSuccess: (res) => { window.location.href = res.redirect },
      onError: (err) => toast.error(`The view did not end: ${err.message}`),
    })
  }

  const handleLogout = () => {
    if (user?.impersonator) return handleEndView()
    logout.mutate(undefined, { onSuccess: () => router.push("/login") })
  }

  if (isLoading || (user && user.role === "admin")) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <Loader2 className="w-8 h-8 animate-spin text-primary" />
          <p className="text-sm font-medium text-muted-foreground">Loading...</p>
        </div>
      </div>
    )
  }

  if (isError || !user) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <p className="text-sm font-medium text-destructive">
            {error?.message || "Authentication failed"}
          </p>
          <Button onClick={() => router.push("/login")}>Go to Login</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background">
      <ClientSidebar user={user} />

      <div className="lg:pl-64">
        <header className="sticky top-0 z-40 bg-card border-b">
          {user.impersonator && (
            <div role="status" className="flex flex-wrap items-center justify-between gap-3 border-b border-warning/60 bg-warning/15 px-4 py-2 text-sm lg:px-8">
              <span className="flex items-center gap-2">
                <span className="h-2 w-2 rounded-full bg-warning" aria-hidden="true" />
                <span>
                  You are viewing as <span className="font-semibold">{user.email}</span>
                  <span className="text-muted-foreground"> (signed in as {user.impersonator.email}; every action is recorded under both)</span>
                </span>
              </span>
              <Button size="sm" variant="outline" onClick={handleEndView} disabled={endView.isPending}>
                {endView.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}End
              </Button>
            </div>
          )}
          <div className="flex items-center justify-between h-16 pl-16 pr-4 lg:px-8">
            <span className="text-sm font-semibold text-foreground">
              Client Area
            </span>

            <div className="hidden md:block ml-auto mr-3">
              <GlobalSearch pages={clientPages} vmHref={clientVmHref} admin={false} />
            </div>

            <div className="mr-3"><ClientNotifications /></div>

            <div className="mr-3"><ThemeToggle /></div>

            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="outline"
                  className="gap-2 font-medium"
                >
                  <div className="w-6 h-6 bg-muted text-foreground flex items-center justify-center rounded-md font-semibold uppercase text-xs">
                    {user.email.charAt(0)}
                  </div>
                  <span className="hidden sm:inline text-sm">{user.email}</span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuLabel className="text-xs font-semibold text-muted-foreground">
                  {user.email}
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={handleLogout}
                  className="cursor-pointer font-medium text-destructive"
                >
                  <LogOut className="w-4 h-4 mr-2" />
                  {user.impersonator ? "End staff view" : "Log out"}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
          <MaintenanceBanner staff={false} />
        </header>

        <main className="p-4 lg:p-8">{children}</main>
      </div>
    </div>
  )
}
