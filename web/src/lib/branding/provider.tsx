"use client"

import { createContext, useContext, useEffect, useState } from "react"
import { type Branding, markOf, themeVars, faviconOf } from "./core"

interface BrandingContextValue {
  branding: Branding
  /** Replace the live branding (after a save), without a page reload. */
  setBranding: (b: Branding) => void
}

const BrandingContext = createContext<BrandingContextValue | null>(null)

/**
 * Holds the branding the server rendered the page with. The colours are
 * already on <html> from the server, so nothing flashes; this only keeps
 * them in step when an admin saves new branding in this tab.
 */
export function BrandingProvider({ initial, children }: { initial: Branding; children: React.ReactNode }) {
  const [branding, setBranding] = useState(initial)

  useEffect(() => {
    const root = document.documentElement
    const vars = themeVars(branding)
    // Clearing the colour hands the theme back to the stylesheet.
    for (const k of ["--brand", "--brand-foreground", "--brand-dark", "--brand-dark-foreground"]) {
      if (k in vars) root.style.setProperty(k, vars[k])
      else root.style.removeProperty(k)
    }
    root.toggleAttribute("data-brand", "--brand" in vars)
    document.title = branding.name
    let icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (!icon) {
      icon = document.createElement("link")
      icon.rel = "icon"
      document.head.appendChild(icon)
    }
    icon.href = faviconOf(branding)
  }, [branding])

  return <BrandingContext.Provider value={{ branding, setBranding }}>{children}</BrandingContext.Provider>
}

export function useBranding(): BrandingContextValue {
  const ctx = useContext(BrandingContext)
  if (!ctx) throw new Error("useBranding must be used inside BrandingProvider")
  return ctx
}

/** The operator's logo, or the generated monogram when there is none. */
export function BrandMark({ branding, className }: { branding?: Branding; className?: string }) {
  const ctx = useContext(BrandingContext)
  const b = branding ?? ctx?.branding
  if (!b) return null
  // The name sits beside the mark wherever it is used, so the image is decorative.
  // eslint-disable-next-line @next/next/no-img-element
  return <img src={markOf(b)} alt="" className={className ?? "h-10 w-10 rounded-md object-contain"} />
}
