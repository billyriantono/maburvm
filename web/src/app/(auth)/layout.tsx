"use client"

import { Megaphone } from "lucide-react"
import { BrandMark, useBranding } from "@/lib/branding/provider"
import { brandFont } from "@/lib/branding/font"

// One sheet, tinted faintly with the operator's colour. Their name is the
// picture: set very large along the bottom and cut by the edge of the screen.
// The colour itself appears only on the mark, the button and that name.
export default function AuthLayout({ children }: { children: React.ReactNode }) {
  const { branding: b } = useBranding()
  const footer = b.footer_text || `© ${new Date().getFullYear()} ${b.name}`

  return (
    <div
      className={`${brandFont.className} min-h-screen flex flex-col overflow-hidden text-foreground bg-[color-mix(in_oklab,hsl(var(--primary))_5%,hsl(var(--card)))]`}
    >
      <header className="flex flex-wrap items-center justify-between gap-x-8 gap-y-3 px-6 pt-7 sm:px-10 lg:px-16 lg:pt-8">
        <div className="flex items-center gap-3 min-w-0">
          <BrandMark className="h-9 w-9 shrink-0 rounded-md object-contain" />
          <span className="truncate text-lg font-semibold tracking-tight">{b.name}</span>
        </div>
        {(b.support_url || b.support_email) && (
          <nav aria-label="Help" className="flex flex-wrap items-center gap-x-6 gap-y-1 text-sm">
            {b.support_url && (
              <a href={b.support_url} target="_blank" rel="noreferrer" className="underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground">
                Get help
              </a>
            )}
            {b.support_email && (
              <a href={`mailto:${b.support_email}`} className="underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground">
                {b.support_email}
              </a>
            )}
          </nav>
        )}
      </header>

      <main className="flex-1 px-6 pb-6 pt-8 sm:px-10 lg:px-16 lg:pt-10">
        <h1
          className="max-w-[18ch] text-[clamp(2.25rem,5.2vw,4.5rem)] font-semibold leading-[0.98] tracking-[-0.02em] text-balance"
          style={{ fontStretch: "84%" }}
        >
          {b.login_headline || b.tagline || "Sign in to manage your servers"}
        </h1>

        {/* The form sits straight under the headline; a notice, when the
            operator has written one, stands beside it and says what it is. */}
        <div className="mt-8 grid items-start gap-x-16 gap-y-8 lg:grid-cols-[24rem_minmax(0,26rem)]">
          <div>{children}</div>
          {b.login_message && (
            <aside
              aria-label={`Notice from ${b.name}`}
              className="rounded-lg border border-[color-mix(in_oklab,hsl(var(--primary))_30%,transparent)] bg-background/70 px-5 py-4"
            >
              <p className="flex items-center gap-2 text-sm font-semibold">
                <Megaphone className="h-4 w-4 text-primary" aria-hidden="true" />
                Notice from {b.name}
              </p>
              <p className="mt-2 leading-relaxed text-foreground/80">{b.login_message}</p>
            </aside>
          )}
        </div>
      </main>

      <p className="px-6 pb-2 text-sm text-foreground/60 sm:px-10 lg:px-16">{footer}</p>
      <div
        aria-hidden="true"
        className="pointer-events-none select-none whitespace-nowrap pl-[calc(1.5rem-0.04em)] sm:pl-[calc(2.5rem-0.04em)] lg:pl-[calc(4rem-0.04em)] -mb-[0.2em] text-[clamp(4.5rem,17vw,14rem)] font-extrabold leading-[0.78] tracking-[-0.045em] text-primary motion-safe:animate-[brand-rise_0.8s_cubic-bezier(0.2,0.7,0.2,1)_both]"
        style={{ fontStretch: "125%" }}
      >
        {b.name}
      </div>
    </div>
  )
}
