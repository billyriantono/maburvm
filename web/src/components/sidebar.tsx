"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { 
  LayoutDashboard, 
  Monitor, 
  HardDrive, 
  Folder,
  Box,
  Settings,
  Users,
  Activity,
  Zap,
  Database,
  User as UserIcon,
  Shield
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { User } from "@/types"

const navItems = [
  { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
  { href: "/vms", label: "Virtual Machines", icon: Monitor },
  { href: "/templates", label: "Templates", icon: Box },
  { href: "/nodes", label: "Nodes", icon: HardDrive },
  { href: "/storage", label: "Storage", icon: Database },
  { href: "/isos", label: "ISOs", icon: Folder },
  { href: "/networks", label: "Networks", icon: Zap },
  { href: "/users", label: "Users", icon: Users },
  { href: "/monitoring", label: "Monitoring", icon: Activity },
]

// Settings navigation with sub-items
const settingsNav = [
  { href: "/settings/profile", label: "Profile", icon: UserIcon },
  { href: "/settings/system", label: "System", icon: Shield },
]

interface SidebarProps {
  user: User
}

export function Sidebar({ user }: SidebarProps) {
  const pathname = usePathname()
  const isSettingsActive = pathname.startsWith("/settings")
  
  return (
    <aside className="fixed left-0 top-0 z-50 h-screen w-64 bg-white border-r-4 border-black overflow-hidden">
      {/* Logo */}
      <div className="h-16 flex items-center px-6 border-b-4 border-black bg-primary">
        <Link href="/dashboard" className="flex items-center gap-2">
          <div className="w-10 h-10 bg-black flex items-center justify-center">
            <Monitor className="w-6 h-6 text-primary" />
          </div>
          <div className="flex flex-col">
            <span className="text-lg font-black uppercase tracking-tighter leading-none">
              MaburVM
            </span>
            <span className="text-[10px] font-bold uppercase tracking-widest text-black/70">
              Virtualization
            </span>
          </div>
        </Link>
      </div>
      
      {/* Navigation */}
      <nav className="flex flex-col h-[calc(100vh-4rem)] overflow-y-auto py-4">
        <div className="px-3 space-y-1">
          {navItems.map((item) => {
            const isActive = pathname.startsWith(item.href)
            const Icon = item.icon
            
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex items-center gap-3 px-4 py-3 text-sm font-bold uppercase tracking-wide transition-all duration-150",
                  isActive 
                    ? "bg-black text-white shadow-neo" 
                    : "text-black hover:bg-gray-100 hover:translate-x-1"
                )}
              >
                <Icon className="w-5 h-5" />
                {item.label}
              </Link>
            )
          })}

          {/* Settings Section */}
          <div className="pt-4 mt-4 border-t-2 border-black">
            <div className="px-4 py-2">
              <span className="text-xs font-black uppercase tracking-wider text-gray-400">
                Settings
              </span>
            </div>
            {settingsNav.map((item) => {
              const isActive = pathname === item.href
              const Icon = item.icon
              
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={cn(
                    "flex items-center gap-3 px-4 py-3 text-sm font-bold uppercase tracking-wide transition-all duration-150 ml-2",
                    isActive 
                      ? "bg-black text-white shadow-neo" 
                      : "text-black hover:bg-gray-100 hover:translate-x-1"
                  )}
                >
                  <Icon className="w-4 h-4" />
                  {item.label}
                </Link>
              )
            })}
          </div>
        </div>
      </nav>
    </aside>
  )
}