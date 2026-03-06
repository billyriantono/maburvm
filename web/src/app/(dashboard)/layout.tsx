import { cookies } from "next/headers"
import { redirect } from "next/navigation"
import Link from "next/link"
import { 
  Bell, 
  ChevronRight, 
  Home, 
  LogOut, 
  Settings, 
  User,
  Search
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
import { cn } from "@/lib/utils"

// Mock user data - in production, this would come from the auth cookie/session
const mockUser = {
  name: "Admin User",
  email: "admin@maburvm.local",
  role: "Administrator",
}

// Simulated auth check - in production, verify JWT or session cookie
async function getUser() {
  const cookieStore = await cookies()
  const authCookie = cookieStore.get("auth_token")
  
  // For now, we'll allow access if there's any session or for demo purposes
  // In production: if (!authCookie) redirect("/login")
  if (!authCookie) {
    // Demo mode: return mock user
    return mockUser
  }
  
  return mockUser
}

// Generate breadcrumbs from current path
function generateBreadcrumbs(pathname: string) {
  const segments = pathname.split("/").filter(Boolean)
  const breadcrumbs = [{ label: "Home", href: "/dashboard" }]
  
  let currentPath = ""
  segments.forEach((segment) => {
    currentPath += `/${segment}`
    const label = segment
      .split("-")
      .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
      .join(" ")
    breadcrumbs.push({ label, href: currentPath })
  })
  
  return breadcrumbs
}

export default async function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  // Auth guard - check for authentication
  // In production, uncomment the redirect below
  // const user = await getUser()
  const user = mockUser // Demo mode
  
  // Get current path for breadcrumbs
  // Note: In Next.js App Router, we need to use usePathname in client components
  // For demo, we'll pass a default
  
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
                <ChevronRight className="w-4 h-4 text-gray-400" />
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
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                  <input
                    type="text"
                    placeholder="Search..."
                    className="w-48 lg:w-64 h-10 pl-10 pr-4 bg-white border-2 border-black text-sm font-medium placeholder:text-gray-400 focus:outline-none focus:shadow-neo-sm transition-all"
                  />
                </div>
              </div>

              {/* Notifications */}
              <DropdownMenu>
                <DropdownMenuTrigger>
                  <Button 
                    variant="ghost" 
                    size="icon"
                    className="relative border-2 border-transparent hover:border-black hover:bg-gray-50"
                  >
                    <Bell className="w-5 h-5" />
                    {/* Notification Badge */}
                    <span className="absolute -top-1 -right-1 w-5 h-5 bg-accent text-white text-[10px] font-black flex items-center justify-center border-2 border-white">
                      3
                    </span>
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-80">
                  <DropdownMenuLabel className="font-black uppercase text-xs">
                    Notifications
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <div className="max-h-64 overflow-y-auto">
                    <DropdownMenuItem className="flex flex-col items-start gap-1 py-3 cursor-pointer">
                      <p className="font-bold text-sm">VM alert</p>
                      <p className="text-xs text-gray-500">vm-01 is offline</p>
                      <p className="text-[10px] text-gray-400">2 min ago</p>
                    </DropdownMenuItem>
                    <DropdownMenuItem className="flex flex-col items-start gap-1 py-3 cursor-pointer">
                      <p className="font-bold text-sm">New node added</p>
                      <p className="text-xs text-gray-500">node-03 is now online</p>
                      <p className="text-[10px] text-gray-400">1 hour ago</p>
                    </DropdownMenuItem>
                    <DropdownMenuItem className="flex flex-col items-start gap-1 py-3 cursor-pointer">
                      <p className="font-bold text-sm">Backup complete</p>
                      <p className="text-xs text-gray-500">Daily backup finished</p>
                      <p className="text-[10px] text-gray-400">3 hours ago</p>
                    </DropdownMenuItem>
                  </div>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem className="justify-center font-bold text-xs text-primary cursor-pointer">
                    View all notifications
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>

              {/* User Menu */}
              <DropdownMenu>
                <DropdownMenuTrigger>
                  <Button 
                    variant="ghost"
                    className="flex items-center gap-2 px-3 h-10 border-2 border-black bg-white hover:bg-gray-50"
                  >
                    <div className="w-8 h-8 bg-primary flex items-center justify-center border border-black">
                      <User className="w-4 h-4" />
                    </div>
                    <span className="hidden md:block text-sm font-bold uppercase">
                      {user.name}
                    </span>
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-56">
                  <DropdownMenuLabel className="font-black uppercase text-xs">
                    {user.name}
                  </DropdownMenuLabel>
                  <p className="px-2 py-1 text-xs text-gray-500">{user.email}</p>
                  <p className="px-2 pb-2 text-xs font-bold text-primary">{user.role}</p>
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
                  <DropdownMenuItem className="font-medium text-destructive focus:text-destructive cursor-pointer">
                    <LogOut className="w-4 h-4 mr-2" />
                    Logout
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