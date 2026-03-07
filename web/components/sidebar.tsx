"use client"

import { useState } from "react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { 
  LayoutDashboard, 
  Monitor, 
  Server, 
  FileCode, 
  Users, 
  Network, 
  FileText, 
  Settings,
  Menu,
  ChevronDown,
  Shield,
  HardDrive,
  LogOut,
  User,
  X
} from "lucide-react"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetTrigger,
  SheetClose,
} from "@/components/ui/sheet"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

interface NavItem {
  title: string
  href: string
  icon: React.ReactNode
}

interface NavGroup {
  title: string
  items: NavItem[]
}

const navigation: NavGroup[] = [
  {
    title: "Overview",
    items: [
      { title: "Dashboard", href: "/dashboard", icon: <LayoutDashboard className="w-5 h-5" /> },
      { title: "VMs", href: "/vms", icon: <Monitor className="w-5 h-5" /> },
    ],
  },
  {
    title: "Infrastructure",
    items: [
      { title: "Nodes", href: "/nodes", icon: <Server className="w-5 h-5" /> },
      { title: "Templates", href: "/templates", icon: <FileCode className="w-5 h-5" /> },
      { title: "Networks", href: "/networks", icon: <Network className="w-5 h-5" /> },
    ],
  },
  {
    title: "Admin",
    items: [
      { title: "Users", href: "/users", icon: <Users className="w-5 h-5" /> },
      { title: "Audit Logs", href: "/audit-logs", icon: <FileText className="w-5 h-5" /> },
      { title: "Settings", href: "/settings", icon: <Settings className="w-5 h-5" /> },
    ],
  },
]

interface SidebarProps {
  user?: {
    name?: string | null
    email?: string | null
    role?: string
  }
}

export function Sidebar({ user }: SidebarProps) {
  const pathname = usePathname()
  const [openGroups, setOpenGroups] = useState<string[]>(["Overview", "Infrastructure", "Admin"])

  const toggleGroup = (title: string) => {
    setOpenGroups(prev => 
      prev.includes(title) 
        ? prev.filter(t => t !== title)
        : [...prev, title]
    )
  }

  const NavContent = ({ mobile = false }: { mobile?: boolean }) => (
    <div className="flex flex-col h-full">
      {/* Logo */}
      <div className="p-4 border-b-4 border-black">
        <Link href="/dashboard" className="flex items-center gap-3">
          <div className="w-10 h-10 bg-black flex items-center justify-center shadow-neo">
            <span className="text-primary font-black text-xl">M</span>
          </div>
          <div>
            <span className="font-black text-lg uppercase tracking-tight">MaburVM</span>
            <p className="text-[10px] font-bold text-gray-500 uppercase tracking-wider -mt-0.5">
              Platform
            </p>
          </div>
        </Link>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto py-4 px-3">
        {navigation.map((group) => (
          <div key={group.title} className="mb-4">
            {/* Group Header - Always visible */}
            <button
              type="button"
              onClick={() => toggleGroup(group.title)}
              className={cn(
                "w-full flex items-center justify-between px-3 py-2",
                "text-xs font-black uppercase tracking-wider text-gray-500",
                "hover:text-black transition-colors"
              )}
            >
              <span className="flex items-center gap-2">
                {group.title === "Admin" && <Shield className="w-3 h-3" />}
                {group.title === "Infrastructure" && <HardDrive className="w-3 h-3" />}
                {group.title}
              </span>
              <ChevronDown 
                className={cn(
                  "w-4 h-4 transition-transform duration-200",
                  openGroups.includes(group.title) ? "rotate-180" : ""
                )} 
              />
            </button>
            
            {/* Group Items */}
            <div className={cn(
              "space-y-1 overflow-hidden transition-all duration-200",
              openGroups.includes(group.title) ? "mt-1" : "max-h-0"
            )}>
              {group.items.map((item) => {
                const isActive = pathname === item.href
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={cn(
                      "flex items-center gap-3 px-3 py-2.5 text-sm font-bold uppercase tracking-wide",
                      "border-l-4 transition-all duration-150",
                      isActive
                        ? "border-black bg-gray-100 text-black"
                        : "border-transparent text-gray-600 hover:text-black hover:bg-gray-50"
                    )}
                  >
                    <span className={cn(
                      "shrink-0",
                      isActive ? "text-black" : "text-gray-400"
                    )}>
                      {item.icon}
                    </span>
                    {item.title}
                  </Link>
                )
              })}
            </div>
          </div>
        ))}
      </nav>

      {/* User Profile - Bottom */}
      <div className="p-3 border-t-4 border-black">
        <DropdownMenu>
          <DropdownMenuTrigger>
            <button
              type="button"
              className={cn(
                "w-full flex items-center gap-3 p-2",
                "border-2 border-black bg-white",
                "hover:bg-gray-50 transition-colors",
                "shadow-neo-sm"
              )}
            >
              <div className="w-9 h-9 bg-primary flex items-center justify-center border-2 border-black shrink-0">
                <User className="w-5 h-5" />
              </div>
              <div className="flex-1 text-left min-w-0">
                <p className="text-sm font-black uppercase truncate">
                  {user?.name || "Admin"}
                </p>
                <p className="text-[10px] font-medium text-gray-500 truncate">
                  {user?.email || "admin@maburvm.local"}
                </p>
              </div>
              <ChevronDown className="w-4 h-4 text-gray-400 shrink-0" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuLabel className="font-black uppercase text-xs">
              My Account
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="font-medium">
              <User className="w-4 h-4 mr-2" />
              Profile
            </DropdownMenuItem>
            <DropdownMenuItem className="font-medium">
              <Settings className="w-4 h-4 mr-2" />
              Settings
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="font-medium text-destructive focus:text-destructive">
              <LogOut className="w-4 h-4 mr-2" />
              Logout
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  )

  // Mobile Sheet version
  const MobileNav = () => (
    <Sheet>
      <SheetTrigger>
        <Button variant="ghost" size="icon" className="lg:hidden">
          <Menu className="w-6 h-6" />
        </Button>
      </SheetTrigger>
      <SheetContent side="left" className="w-72 p-0 border-4 border-black">
        <NavContent mobile />
      </SheetContent>
    </Sheet>
  )

  return (
    <>
      {/* Desktop Sidebar */}
      <aside className="hidden lg:flex lg:flex-col lg:fixed lg:inset-y-0 lg:left-0 lg:w-64 lg:bg-white lg:border-r-4 lg:border-black">
        <NavContent />
      </aside>

      {/* Mobile Navigation */}
      <MobileNav />
    </>
  )
}

export default Sidebar