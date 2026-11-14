"use client"

import { useEffect, useState } from "react"
import { usePathname } from "next/navigation"
import { Menu, X } from "lucide-react"
import { cn } from "@/lib/utils"

// On a phone the sidebar is a drawer: closed until the menu button opens it,
// and closed again by picking a page, pressing Escape or tapping outside. From
// the lg breakpoint up it is always there and none of this shows.
export function useNavDrawer() {
  const pathname = usePathname()
  const [open, setOpen] = useState(false)

  useEffect(() => setOpen(false), [pathname])
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false)
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [open])

  const iconButton = "inline-flex h-10 w-10 items-center justify-center rounded-md border border-input bg-card text-foreground hover:bg-accent lg:hidden"
  return {
    /** Add to the <aside>. Hidden from keyboard and screen readers while closed. */
    asideClass: cn("transition-transform duration-200 lg:visible lg:translate-x-0", open ? "translate-x-0" : "invisible -translate-x-full"),
    /** Render beside the <aside>: the menu button and the dimmed backdrop. */
    controls: (
      <>
        <button type="button" className={cn(iconButton, "fixed left-3 top-3 z-50")} aria-label="Open menu" aria-expanded={open} aria-controls="app-nav" onClick={() => setOpen(true)}>
          <Menu className="h-5 w-5" aria-hidden="true" />
        </button>
        {open && <div className="fixed inset-0 z-50 bg-black/50 lg:hidden" aria-hidden="true" onClick={() => setOpen(false)} />}
      </>
    ),
    /** Render inside the <aside>, in its top row. */
    closeButton: (
      <button type="button" className={cn(iconButton, "ml-auto shrink-0")} aria-label="Close menu" onClick={() => setOpen(false)}>
        <X className="h-5 w-5" aria-hidden="true" />
      </button>
    ),
  }
}
