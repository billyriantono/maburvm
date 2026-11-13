"use client"

import { useCallback, useEffect, useState } from "react"
import { type Branding, accentOf, hsl, markOf, onAccent } from "@/lib/branding/core"
import { SignalArt } from "@/components/signal-art"

// The sign-in screen: a picture side and a form side. The real pages and the
// preview in the brand studio both render this, so what an operator sees while
// choosing is what their customers get.
//
// The picture side is the operator's choice (Branding, Sign-in page):
//   traces  the product's reading traces drawn in their colour (the default)
//   logo    their logo, large, on their colour
//   image   a picture of their own

export type PanelStatus = { state: "checking" } | { state: "up"; ms: number } | { state: "down" }

// Asks the panel a question it answers without a session, and times it. The
// lamp on the sign-in screen is therefore a measurement, not a decoration: if
// the panel is down, this is where someone finds out, before typing a password.
export function usePanelStatus(): [PanelStatus, () => void] {
  const [status, setStatus] = useState<PanelStatus>({ state: "checking" })
  const check = useCallback(async () => {
    setStatus({ state: "checking" })
    const started = performance.now()
    try {
      const res = await fetch("/api/v1/branding", { cache: "no-store", signal: AbortSignal.timeout(8000) })
      setStatus(res.ok ? { state: "up", ms: Math.max(1, Math.round(performance.now() - started)) } : { state: "down" })
    } catch {
      setStatus({ state: "down" })
    }
  }, [])
  useEffect(() => {
    check()
  }, [check])
  return [status, check]
}

export function SignInFrame({
  b,
  status,
  onRecheck,
  children,
  className = "min-h-screen",
}: {
  b: Branding
  status: PanelStatus
  onRecheck?: () => void
  children: React.ReactNode
  /** Height of the frame. The real page fills the screen; the studio preview is a fixed box. */
  className?: string
}) {
  const name = b.name.trim() || "Your panel"
  const footer = b.footer_text || `© ${new Date().getFullYear()} ${name}`
  const picture = b.login_image && b.login_image !== "none" ? b.login_image : b.login_image_url
  const art = b.login_art === "image" && picture ? "image" : b.login_art === "logo" ? "logo" : "traces"

  const accent = accentOf(b)
  // The traces take only their hue from the operator's colour, so a pale or a
  // very dark brand colour still gives a deep ground the lines can glow on.
  const [hue, sat] = hsl(accent).split(" ").map((v) => parseFloat(v))
  const depth = Math.min(Math.max(sat, 62), 82)
  const ground =
    art === "logo"
      ? { background: accent, color: onAccent(accent) }
      : art === "image"
        ? { background: "#0b0f14", color: "#f1f4f7" }
        : {
            background: `radial-gradient(110% 80% at 82% 50%, hsl(${hue} ${depth}% 30%) 0%, hsl(${hue} ${depth}% 17%) 38%, hsl(${hue} ${depth * 0.8}% 7%) 100%)`,
            color: "#f1f4f7",
          }

  return (
    <div className={`bg-background text-foreground lg:grid lg:grid-cols-[minmax(0,1.15fr)_minmax(28rem,1fr)] ${className}`}>
      <section
        // Dark-ground lamp colours for the two dark pictures; on a logo ground the theme's own apply.
        className={`${art === "logo" ? "" : "faceplate"} relative isolate flex min-h-[12rem] flex-col justify-between gap-8 overflow-hidden px-6 py-6 sm:px-10 lg:min-h-full lg:px-12 lg:py-10`}
        style={ground}
      >
        {art === "traces" && <SignalArt seed={name} hue={hue} saturation={depth} className="absolute inset-0 -z-10 h-full w-full" />}
        {art === "image" && (
          <>
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src={picture} alt="" className="absolute inset-0 -z-20 h-full w-full object-cover" />
            {/* Shade under the two lines of text, so they read on any picture. */}
            <div aria-hidden="true" className="absolute inset-0 -z-10 bg-[linear-gradient(to_bottom,rgb(0_0_0/0.6),transparent_28%,transparent_70%,rgb(0_0_0/0.65))]" />
          </>
        )}

        {art === "logo" ? (
          <div className="flex flex-1 items-center justify-center py-6">
            {b.logo ? (
              // eslint-disable-next-line @next/next/no-img-element
              <img src={b.logo} alt={name} className="max-h-[42%] max-w-[58%] object-contain lg:max-h-[18rem]" />
            ) : (
              <p className="max-w-full text-center text-[clamp(2.25rem,5.5vw,5rem)] font-extrabold leading-none tracking-[-0.03em] [font-stretch:125%] [overflow-wrap:anywhere]">
                {name}
              </p>
            )}
          </div>
        ) : (
          <div className="flex min-w-0 items-center gap-3">
            {/* An uploaded logo may be dark on a clear ground, so it sits on a light tile here. */}
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src={markOf(b)} alt="" className={`h-10 shrink-0 rounded-md object-contain ${b.logo ? "w-auto max-w-[11rem] bg-white px-2 py-1" : "w-10"}`} />
            <span className="truncate text-lg font-semibold [font-stretch:112%]">{name}</span>
          </div>
        )}

        <p role="status" className="flex items-center gap-2 text-sm">
          <span
            aria-hidden="true"
            className={`h-2.5 w-2.5 shrink-0 rounded-full ring-2 ring-black/20 ${status.state === "up" ? "bg-success" : status.state === "down" ? "bg-destructive" : "bg-current opacity-50"}`}
          />
          {status.state === "up" ? (
            <span>
              Panel online<span className="opacity-75">, answered in {status.ms} ms</span>
            </span>
          ) : status.state === "down" ? (
            <span>
              The panel is not answering.{" "}
              <button type="button" onClick={onRecheck} className="font-semibold underline underline-offset-4">
                Check again
              </button>
            </span>
          ) : (
            <span className="opacity-75">Checking the panel</span>
          )}
        </p>
      </section>

      <main className="flex flex-col px-6 py-10 sm:px-10 lg:px-16 lg:py-12">
        <div className="mx-auto my-auto w-full max-w-[26rem] [&_input]:h-12 [&_input]:text-base">
          {b.login_message && (
            <aside aria-label={`Notice from ${name}`} className="mb-8 rounded-md border border-border px-4 py-3 text-sm">
              <p className="font-semibold">Notice from {name}</p>
              <p className="mt-1 leading-relaxed text-muted-foreground">{b.login_message}</p>
            </aside>
          )}
          {children}
        </div>
        <footer className="mx-auto mt-12 flex w-full max-w-[26rem] flex-wrap items-center justify-between gap-x-6 gap-y-2 text-sm text-muted-foreground">
          <p>{footer}</p>
          {(b.support_url || b.support_email) && (
            <nav aria-label="Help" className="flex flex-wrap items-center gap-x-5 gap-y-1">
              {b.support_url && (
                <a href={b.support_url} target="_blank" rel="noreferrer" className="text-foreground underline decoration-foreground/40 underline-offset-4 hover:decoration-foreground">
                  Get help
                </a>
              )}
              {b.support_email && (
                <a href={`mailto:${b.support_email}`} className="text-foreground underline decoration-foreground/40 underline-offset-4 hover:decoration-foreground">
                  {b.support_email}
                </a>
              )}
            </nav>
          )}
        </footer>
      </main>
    </div>
  )
}
