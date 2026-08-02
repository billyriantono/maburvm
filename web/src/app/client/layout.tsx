"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { LogOut, Loader2 } from "lucide-react"
import { ClientSidebar } from "@/components/client-sidebar"
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

export default function ClientLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const router = useRouter()
  const { data: user, isLoading, error, isError } = useAuth()
  const logout = useLogout()

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

  const handleLogout = () => {
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
          <div className="flex items-center justify-between h-16 px-4 lg:px-8">
            <span className="text-sm font-semibold text-foreground">
              Client Area
            </span>

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
                  Log out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>

        <main className="p-4 lg:p-8">{children}</main>
      </div>
    </div>
  )
}
