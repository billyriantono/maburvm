"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { 
  LayoutDashboard, 
  Monitor, 
  HardDrive, 
  Folder,
  Box,
  Users,
  Activity,
  Zap,
  Network,
  Database,
  User as UserIcon,
  Shield,
  KeyRound,
  KeySquare,
  Boxes,
  Globe,
  ScrollText,
  Layers,
  Waypoints,
  ShieldAlert,
  ShieldX,
  Palette,
  Mail,
  ShieldCheck,
  ListChecks,
  FileDown,
  Rocket,
  BellRing,
  Archive,
  BrickWall,
  ArrowDownToLine,
  History,
  FileText,
  BarChart3,
  Megaphone,
  Wrench,
  Inbox,
  Store,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useNavDrawer } from "@/components/nav-drawer"
import { BrandMark, useBranding } from "@/lib/branding/provider"
import { can } from "@/lib/hooks/use-auth"
import type { User } from "@/types"

// perm is what the page needs to show anything (any one of them, when a page
// has parts behind different permissions); the role decides. Pages without
// one read what every staff account may read.
type NavItem = { href: string; label: string; icon: typeof Monitor; perm?: string | string[] }

const allowed = (user: User, perm?: string | string[]) =>
  !perm || (Array.isArray(perm) ? perm.some((p) => can(user, p)) : can(user, perm))

const navItems: NavItem[] = [
  { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
  { href: "/vms", label: "Virtual Machines", icon: Monitor, perm: "vm:read" },
  { href: "/plans", label: "Plans", icon: Boxes },
  { href: "/templates", label: "Templates", icon: Box },
  { href: "/images", label: "Images", icon: Layers, perm: "vm:read" },
  { href: "/regions", label: "Regions", icon: Globe },
  { href: "/nodes", label: "Nodes", icon: HardDrive, perm: "node:read" },
  { href: "/storage", label: "Storage", icon: Database, perm: "node:read" },
  { href: "/backups", label: "Backups", icon: Archive, perm: "backup:read" },
  { href: "/isos", label: "ISOs", icon: Folder },
  { href: "/networks", label: "Networks", icon: Zap, perm: "network:read" },
  { href: "/ip-pools", label: "IP Pools", icon: Network, perm: "network:read" },
  { href: "/ip-requests", label: "Address requests", icon: Inbox, perm: "network:read" },
  { href: "/floating-ips", label: "Floating IPs", icon: Waypoints, perm: "floating_ip:read" },
  { href: "/firewall-groups", label: "Firewall groups", icon: BrickWall, perm: "firewall_group:read" },
  { href: "/dns", label: "DNS", icon: Globe, perm: "network:read" },
  { href: "/users", label: "Users", icon: Users, perm: "user:read" },
  { href: "/staff-roles", label: "Staff roles", icon: KeySquare },
  { href: "/monitoring", label: "Monitoring", icon: Activity, perm: "node:read" },
  { href: "/alerts", label: "Alerts", icon: BellRing, perm: "ops:read" },
  { href: "/announcements", label: "Announcements", icon: Megaphone, perm: "announcement:read" },
  { href: "/reports", label: "Reports", icon: BarChart3, perm: "report:read" },
  { href: "/logs", label: "Logs", icon: FileText, perm: ["audit:read", "network:read", "settings:read"] },
  { href: "/audit-logs", label: "Audit log", icon: History, perm: "audit:read" },
  { href: "/abuse", label: "Abuse", icon: ShieldAlert, perm: "network:read" },
  { href: "/ip-reputation", label: "IP Reputation", icon: ShieldX, perm: "network:read" },
  { href: "/security", label: "Security", icon: ShieldCheck, perm: "settings:read" },
]

// Settings navigation with sub-items
const settingsNav: NavItem[] = [
  { href: "/settings/profile", label: "Profile", icon: UserIcon },
  { href: "/settings/api-keys", label: "API Keys", icon: KeyRound },
  { href: "/settings/ssh-keys", label: "SSH Keys", icon: KeySquare },
  { href: "/settings/recipes", label: "Recipes", icon: ScrollText },
  { href: "/settings/branding", label: "Branding", icon: Palette, perm: "settings:read" },
  { href: "/settings/customer-area", label: "Customer area", icon: Store, perm: "settings:read" },
  { href: "/settings/emails", label: "Emails", icon: Mail, perm: "settings:read" },
  { href: "/settings/queue", label: "Job queue", icon: ListChecks, perm: "ops:read" },
  { href: "/settings/maintenance", label: "Maintenance", icon: Wrench, perm: "settings:read" },
  { href: "/settings/export", label: "Export data", icon: FileDown, perm: "admin:access" },
  { href: "/settings/import", label: "Import", icon: ArrowDownToLine, perm: "import:manage" },
  { href: "/setup", label: "Setup guide", icon: Rocket },
  { href: "/settings/system", label: "System", icon: Shield, perm: "settings:read" },
]

interface SidebarProps {
  user: User
}

// Every page reachable from this sidebar, for the header search and breadcrumb.
export const adminPages: { href: string; label: string; perm?: string | string[] }[] = [...navItems, ...settingsNav].map(
  ({ href, label, perm }) => ({ href, label, perm })
)

/** The pages this account's role can open. */
export function visibleAdminPages(user: User) {
  return adminPages.filter((p) => allowed(user, p.perm))
}

export function Sidebar({ user }: SidebarProps) {
  const pathname = usePathname()
  const { branding } = useBranding()
  const drawer = useNavDrawer()

  return (
    <>
    {drawer.controls}
    <aside id="app-nav" className={cn("fixed left-0 top-0 z-50 h-screen w-64 bg-card border-r overflow-hidden", drawer.asideClass)}>
      {/* Logo */}
      <div className="h-16 flex items-center gap-2 pl-6 pr-3 border-b">
        <Link href="/dashboard" className="flex items-center gap-2">
          <BrandMark />
          <div className="flex flex-col min-w-0">
            <span className="text-lg font-bold leading-none truncate">
              {branding.name}
            </span>
            <span className="text-[10px] font-medium text-muted-foreground truncate">
              {branding.tagline || "Virtualization"}
            </span>
          </div>
        </Link>
        {drawer.closeButton}
      </div>

      {/* Navigation */}
      <nav className="flex flex-col h-[calc(100vh-4rem)] overflow-y-auto py-4">
        <div className="px-3 space-y-1">
          {navItems.filter((item) => allowed(user, item.perm)).map((item) => {
            const isActive = pathname.startsWith(item.href)
            const Icon = item.icon

            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors",
                  isActive
                    ? "bg-muted text-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground"
                )}
              >
                <Icon className="w-5 h-5" />
                {item.label}
              </Link>
            )
          })}

          {/* Settings Section */}
          <div className="pt-4 mt-4 border-t">
            <div className="px-3 py-2">
              <span className="text-xs font-semibold text-muted-foreground">
                Settings
              </span>
            </div>
            {settingsNav.filter((item) => allowed(user, item.perm)).map((item) => {
              const isActive = pathname === item.href
              const Icon = item.icon

              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={cn(
                    "flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors ml-2",
                    isActive
                      ? "bg-muted text-foreground"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground"
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
    </>
  )
}