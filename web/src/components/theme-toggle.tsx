"use client"

import { useEffect, useState } from "react"
import { useTheme } from "next-themes"
import { Moon, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"

// Switches between the light and dark themes. Until the first click the panel
// follows the device setting; after it, the choice is remembered on this device.
export function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  // The theme is only known in the browser. Render a same-sized, inert button
  // until then so the header does not shift.
  const [ready, setReady] = useState(false)
  useEffect(() => setReady(true), [])
  const dark = ready && resolvedTheme === "dark"
  const label = dark ? "Switch to light theme" : "Switch to dark theme"
  return (
    <Button variant="outline" size="icon" aria-label={label} title={label} disabled={!ready} onClick={() => setTheme(dark ? "light" : "dark")}>
      {dark ? <Sun className="h-5 w-5" aria-hidden="true" /> : <Moon className="h-5 w-5" aria-hidden="true" />}
    </Button>
  )
}
