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

  // Handle logout
  const handleLogout = () => {
    logout.mutate(undefined, {
      onSuccess: () => {
        router.push("/login")
      }
    })
  }

  // Show loading state
  if (isLoading) {
    return (
      <div className="min-h-screen bg-[#f5f5f0] flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <Loader2 className="w-8 h-8 animate-spin text-primary" />
          <p className="text-sm font-medium text-gray-500">Loading...</p>
        </div>
      </div>
    )
  }

  // Show error state
  if (isError || !user) {
    return (
      <div className="min-h-screen bg-[#f5f5f0] flex items-center justify-center">
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
    <div className="min-h-screen bg-[#f5f5f0]">
      {/* Sidebar */}
      <Sidebar user={user} />
      
      {/* Main Content Area */}
      <div className="lg:pl-64">
        {/* Top Header Bar */}
        <header className="sticky top-0 z-40 bg-white border-b-4 border-black">
          <div className="flex items-center justify-between h-16 px-4 lg:px-8">
            {/* Left: Mobile menu + Breadcrumbs placeholder */}
            <div className="flex items-center gap-4">
              {/* Breadcrumbs - shown on desktop */}
              <nav className="hidden md:flex items-center gap-1">
                <Link 
                  href="/dashboard" 
                  className="p-2 text-gray-500 hover:text-black transition-colors"
                >
                  <Home className="w-4 h-4" />
                </Link>
                <ChevronRight className="w-4 h-4 text-gray-600" />
                <span className="text-sm font-bold uppercase text-black">
                  Dashboard
                </span>
              </nav>
            </div>

            {/* Right: Search, Notifications, User Menu */}
            <div className="flex items-center gap-3">
              {/* Search - Desktop */}
              <div className="hidden md:flex items-center">
                <div className="relative">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
                  <input
                    type="text"
                    placeholder="Search..."
                    className="w-48 lg:w-64 h-10 pl-10 pr-4 bg-white border-2 border-black text-sm font-medium placeholder:text-gray-600 focus:outline-none focus:shadow-neo-sm transition-all"
                  />
                </div>
              </div>

              {/* Notifications */}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="relative w-10 h-10 p-0 border-2 border-black bg-white text-black hover:bg-gray-50"
                  >
                    <Bell className="w-5 h-5 text-black" />
                    {/* Notification Badge */}
                    {(stats?.alerts ?? 0) > 0 && (
                      <span className="absolute -top-1 -right-1 w-5 h-5 bg-accent text-white text-[10px] font-black flex items-center justify-center border-2 border-black z-20">
                        {stats?.alerts ?? 0}
                      </span>
                    )}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-80">
                  <DropdownMenuLabel className="font-black uppercase text-xs">
                    Notifications
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <div className="max-h-64 overflow-y-auto">
                    {stats?.recent_activity && stats.recent_activity.length > 0 ? (
                      stats.recent_activity.slice(0, 5).map((activity) => (
                        <DropdownMenuItem key={activity.id} className="flex flex-col items-start gap-1 py-3 cursor-pointer">
                          <p className="font-bold text-sm">
                            {activity.action.toLowerCase()} {activity.resource_type.replace(/_/g, ' ').toLowerCase()}
                          </p>
                          <p className="text-xs text-gray-500">
                            {activity.resource_id ? `Resource ${activity.resource_id.slice(0, 8)}...` : 'System event'}
                          </p>
                          <p className="text-[10px] text-gray-600">
                            {new Date(activity.created_at).toLocaleString()}
                          </p>
                        </DropdownMenuItem>
                      ))
                    ) : (
                      <div className="py-4 text-center">
                        <p className="text-xs text-gray-500">No notifications</p>
                      </div>
                    )}
                  </div>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem asChild className="justify-center font-bold text-xs text-primary cursor-pointer">
                    <Link href="/audit-logs">View all notifications</Link>
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>

              {/* User Menu */}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button 
                    variant="ghost"
                    className="flex items-center gap-2 px-3 h-10 border-2 border-black bg-white hover:bg-gray-50"
                  >
                    <div className="w-8 h-8 bg-primary flex items-center justify-center border border-black">
                      <User className="w-4 h-4" />
                    </div>
                    <span className="hidden md:block text-sm font-bold uppercase">
                      {displayName}
                    </span>
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-56">
                  <DropdownMenuLabel className="font-black uppercase text-xs">
                    {displayName}
                  </DropdownMenuLabel>
                  <p className="px-2 py-1 text-xs text-gray-500">{user.email}</p>
                  <p className="px-2 pb-2 text-xs font-bold text-primary">{displayRole}</p>
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