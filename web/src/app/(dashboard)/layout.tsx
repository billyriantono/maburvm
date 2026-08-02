"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import { 
  Bell, 
  ChevronRight, 
  Home, 
  LogOut, 
  Settings, 
  User,
  Search,
  Loader2
} from "lucide-react"
import { Sidebar } from "@/components/sidebar"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useAuth, useLogout } from "@/lib/hooks/use-auth"
import { useDashboardStats } from "@/lib/hooks/use-dashboard"

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const router = useRouter()
  const { data: user, isLoading, error, isError } = useAuth()
  const logout = useLogout()
  const { data: stats } = useDashboardStats()

  // Redirect to login if not authenticated
  useEffect(() => {
    if (isError && !isLoading) {
      router.push("/login")
    }
  }, [isError, isLoading, router])

  // The (dashboard) area is the admin console. Non-admin (client) users belong in
  // the dedicated /client area, so bounce them there. The backend enforces
  // permissions regardless; this is about showing each role the right UI.
  useEffect(() => {
    if (user && user.role !== "admin") {
      router.replace("/client/dashboard")
    }
  }, [user, router])

  // Handle logout
  const handleLogout = () => {
    logout.mutate(undefined, {
      onSuccess: () => {
        router.push("/login")
      }
    })
  }

  // Show loading state (also while a client is being redirected to /client, to
  // avoid flashing the admin console at them).
  if (isLoading || (user && user.role !== "admin")) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <Loader2 className="w-8 h-8 animate-spin text-primary" />
          <p className="text-sm font-medium text-muted-foreground">Loading...</p>
        </div>
      </div>
    )
  }

  // Show error state
  if (isError || !user) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <p className="text-sm font-medium text-destructive">
            {error?.message || "Authentication failed"}
          </p>
          <Button onClick={() => router.push("/login")}>
            Go to Login
          </Button>
        </div>
      </div>
    )
  }

  // Derive display name from email (use part before @)
  const displayName = user.email.split('@')[0]
  const displayRole = user.role === 'admin' ? 'Administrator' : 'Client'

  return (
    <div className="min-h-screen bg-background">
      {/* Sidebar */}
      <Sidebar user={user} />

      {/* Main Content Area */}
      <div className="lg:pl-64">
        {/* Top Header Bar */}
        <header className="sticky top-0 z-40 bg-card border-b">
          <div className="flex items-center justify-between h-16 px-4 lg:px-8">
            {/* Left: Mobile menu + Breadcrumbs placeholder */}
            <div className="flex items-center gap-4">
              {/* Breadcrumbs - shown on desktop */}
              <nav className="hidden md:flex items-center gap-1">
                <Link
                  href="/dashboard"
                  className="p-2 text-muted-foreground hover:text-foreground transition-colors"
                >
                  <Home className="w-4 h-4" />
                </Link>
                <ChevronRight className="w-4 h-4 text-muted-foreground" />
                <span className="text-sm font-medium text-foreground">
                  Dashboard
                </span>
              </nav>
            </div>

            {/* Right: Search, Notifications, User Menu */}
            <div className="flex items-center gap-3">
              {/* Search - Desktop */}
              <div className="hidden md:flex items-center">
                <div className="relative">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                  <input
                    type="text"
                    placeholder="Search..."
                    className="w-48 lg:w-64 h-10 pl-10 pr-4 bg-background border border-input rounded-md text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 transition-all"
                  />
                </div>
              </div>

              {/* Notifications */}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="outline"
                    size="icon"
                    className="relative"
                  >
                    <Bell className="w-5 h-5" />
                    {/* Notification Badge */}
                    {(stats?.alerts ?? 0) > 0 && (
                      <span className="absolute -top-1 -right-1 w-5 h-5 bg-destructive text-destructive-foreground text-[10px] font-semibold flex items-center justify-center rounded-full z-20">
                        {stats?.alerts ?? 0}
                      </span>
                    )}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-80">
                  <DropdownMenuLabel className="text-xs font-semibold text-muted-foreground">
                    Notifications
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <div className="max-h-64 overflow-y-auto">
                    {stats?.recent_activity && stats.recent_activity.length > 0 ? (
                      stats.recent_activity.slice(0, 5).map((activity) => (
                        <DropdownMenuItem key={activity.id} className="flex flex-col items-start gap-1 py-3 cursor-pointer">
                          <p className="font-medium text-sm">
                            {activity.action.toLowerCase()} {activity.resource_type.replace(/_/g, ' ').toLowerCase()}
                          </p>
                          <p className="text-xs text-muted-foreground">
                            {activity.resource_id ? `Resource ${activity.resource_id.slice(0, 8)}...` : 'System event'}
                          </p>
                          <p className="text-[10px] text-muted-foreground">
                            {new Date(activity.created_at).toLocaleString()}
                          </p>
                        </DropdownMenuItem>
                      ))
                    ) : (
                      <div className="py-4 text-center">
                        <p className="text-xs text-muted-foreground">No notifications</p>
                      </div>
                    )}
                  </div>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem asChild className="justify-center font-medium text-xs text-primary cursor-pointer">
                    <Link href="/audit-logs">View all notifications</Link>
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>

              {/* User Menu */}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="outline"
                    className="flex items-center gap-2 px-3"
                  >
                    <div className="w-8 h-8 bg-muted flex items-center justify-center rounded-md">
                      <User className="w-4 h-4" />
                    </div>
                    <span className="hidden md:block text-sm font-medium">
                      {displayName}
                    </span>
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-56">
                  <DropdownMenuLabel className="text-xs font-semibold text-muted-foreground">
                    {displayName}
                  </DropdownMenuLabel>
                  <p className="px-2 py-1 text-xs text-muted-foreground">{user.email}</p>
                  <p className="px-2 pb-2 text-xs font-medium text-primary">{displayRole}</p>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem className="font-medium cursor-pointer">
                    <User className="w-4 h-4 mr-2" />
                    Profile
                  </DropdownMenuItem>
                  <DropdownMenuItem className="font-medium cursor-pointer">
                    <Settings className="w-4 h-4 mr-2" />
                    Settings
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem 
                    className="font-medium text-destructive focus:text-destructive cursor-pointer"
                    onClick={handleLogout}
                    disabled={logout.isPending}
                  >
                    <LogOut className="w-4 h-4 mr-2" />
                    {logout.isPending ? "Logging out..." : "Logout"}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>
        </header>

        {/* Main Content */}
        <main className="p-4 lg:p-8">
          {children}
        </main>
      </div>
    </div>
  )
}