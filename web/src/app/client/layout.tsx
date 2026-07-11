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
      <div className="min-h-screen bg-[#f5f5f0] flex items-center justify-center">
        <div className="flex flex-col items-center gap-4">
          <Loader2 className="w-8 h-8 animate-spin text-primary" />
          <p className="text-sm font-medium text-gray-500">Loading...</p>
        </div>
      </div>
    )
  }

  if (isError || !user) {
    return (
      <div className="min-h-screen bg-[#f5f5f0] flex items-center justify-center">
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
    <div className="min-h-screen bg-[#f5f5f0]">
      <ClientSidebar user={user} />

      <div className="lg:pl-64">
        <header className="sticky top-0 z-40 bg-white border-b-4 border-black">
          <div className="flex items-center justify-between h-16 px-4 lg:px-8">
            <span className="text-sm font-black uppercase tracking-wider text-black">
              Client Area
            </span>

            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  className="h-10 gap-2 border-2 border-black bg-white text-black hover:bg-gray-50 font-bold"
                >
                  <div className="w-6 h-6 bg-black text-primary flex items-center justify-center font-black uppercase text-xs">
                    {user.email.charAt(0)}
                  </div>
                  <span className="hidden sm:inline text-sm">{user.email}</span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuLabel className="font-black uppercase text-xs">
                  {user.email}
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={handleLogout}
                  className="cursor-pointer font-bold text-destructive"
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
