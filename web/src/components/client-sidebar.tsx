"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import {
  LayoutDashboard,
  Monitor,
  PlusCircle,
  User as UserIcon,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { User } from "@/types"

// Client-facing navigation. Deliberately narrow: an end user only ever sees
// their own VMs and self-service actions — never nodes, users, IP pools, plans,
// storage, DNS, monitoring, or system settings (those are admin-only and live in
// the /(dashboard) area). Backend RBAC + per-resource ownership checks enforce
// this regardless of the UI, so this list is about UX, not security.
const navItems = [
  { href: "/client/dashboard", label: "Dashboard", icon: LayoutDashboard },
  { href: "/client/vms", label: "My VMs", icon: Monitor },
  { href: "/client/order", label: "Order VM", icon: PlusCircle },
]

const settingsNav = [
  { href: "/client/settings/profile", label: "Profile", icon: UserIcon },
]

interface ClientSidebarProps {
  user: User
}

export function ClientSidebar({ user }: ClientSidebarProps) {
  const pathname = usePathname()
  const isSettingsActive = pathname.startsWith("/client/settings") || pathname.startsWith("/settings")

  return (
    <aside className="fixed left-0 top-0 z-50 h-screen w-64 bg-white border-r-4 border-black overflow-hidden">
      {/* Logo */}
      <div className="h-16 flex items-center px-6 border-b-4 border-black bg-primary">
        <Link href="/client/dashboard" className="flex items-center gap-2">
          <div className="w-10 h-10 bg-black flex items-center justify-center">
            <Monitor className="w-6 h-6 text-primary" />
          </div>
          <div className="flex flex-col">
            <span className="text-lg font-black uppercase tracking-tighter leading-none">
              MaburVM
            </span>
            <span className="text-[10px] font-bold uppercase tracking-widest text-black">
              Client Area
            </span>
          </div>
        </Link>
      </div>

      {/* Navigation */}
      <nav className="flex flex-col h-[calc(100vh-4rem)] overflow-y-auto py-4">
        <div className="flex-1 px-3 space-y-1">
          {navItems.map((item) => {
            const isActive = pathname === item.href || pathname.startsWith(item.href + "/")
            const Icon = item.icon
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex items-center gap-3 px-3 py-2.5 text-sm font-bold uppercase tracking-wide border-2 transition-all",
                  isActive
                    ? "bg-primary text-black border-black shadow-neo-sm"
                    : "bg-white text-black border-transparent hover:border-black"
                )}
              >
                <Icon className="w-5 h-5" />
                {item.label}
              </Link>
            )
          })}

          <div className="pt-4">
            <p className="px-3 pb-2 text-[10px] font-black uppercase tracking-widest text-gray-500">
              Settings
            </p>
            <div className="space-y-1">
              {settingsNav.map((item) => {
                const isActive = pathname === item.href
                const Icon = item.icon
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={cn(
                      "flex items-center gap-3 px-3 py-2.5 text-sm font-bold uppercase tracking-wide border-2 transition-all",
                      isActive
                        ? "bg-primary text-black border-black shadow-neo-sm"
                        : "bg-white text-black border-transparent hover:border-black"
                    )}
                  >
                    <Icon className="w-5 h-5" />
                    {item.label}
                  </Link>
                )
              })}
            </div>
          </div>
        </div>

        {/* User footer */}
        <div className="px-3 pt-4 mt-auto border-t-2 border-black">
          <div className="flex items-center gap-3 px-3 py-3">
            <div className="w-8 h-8 bg-black text-primary flex items-center justify-center font-black uppercase">
              {user.email.charAt(0)}
            </div>
            <div className="flex flex-col min-w-0">
              <span className="text-xs font-bold truncate">{user.email}</span>
              <span className="text-[10px] font-bold uppercase tracking-widest text-gray-500">
                {isSettingsActive ? "Settings" : "Client"}
              </span>
            </div>
          </div>
        </div>
      </nav>
    </aside>
  )
}
