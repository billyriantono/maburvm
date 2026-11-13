"use client"

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react"
import { BrandMark, useBranding } from "@/lib/branding/provider"

// The sign-in screen is the front of the machine room: a dark faceplate with
// the operator's name on it and a lamp that says whether the panel is
// answering, and beside it the form. The faceplate is the same one a VM's page
// opens with, so the product looks like itself before anyone has signed in.

type Probe = { state: "checking" } | { state: "up"; ms: number } | { state: "down" }

// Asks the panel a question it answers without a session, and times it. The
// lamp is therefore a measurement, not a decoration: if the panel is down,
// this is where someone finds out, before typing a password.
function usePanelProbe(): [Probe, () => void] {
  const [probe, setProbe] = useState<Probe>({ state: "checking" })
  const check = useCallback(async () => {
    setProbe({ state: "checking" })
    const started = performance.now()
    try {
      const res = await fetch("/api/v1/branding", { cache: "no-store", signal: AbortSignal.timeout(8000) })
      setProbe(res.ok ? { state: "up", ms: Math.max(1, Math.round(performance.now() - started)) } : { state: "down" })
    } catch {
      setProbe({ state: "down" })
    }
  }, [])
  useEffect(() => {
    check()
  }, [check])
  return [probe, check]
}

// The operator's name set as large as the faceplate allows, whatever its
// length. Sized by measuring, because no CSS unit knows how wide a word is.
function Nameplate({ name }: { name: string }) {
  const box = useRef<HTMLDivElement>(null)
  const text = useRef<HTMLSpanElement>(null)
  useLayoutEffect(() => {
    const fit = () => {
      if (!box.current || !text.current) return
      text.current.style.fontSize = "100px"
      const perPx = text.current.scrollWidth / 100
      if (perPx > 0) text.current.style.fontSize = `${Math.min(box.current.clientWidth / perPx, 220)}px`
    }
    fit()
    const ro = new ResizeObserver(fit)
    if (box.current) ro.observe(box.current)
    document.fonts?.ready.then(fit)
    return () => ro.disconnect()
  }, [name])
  return (
    <div ref={box} aria-hidden="true" className="select-none overflow-x-clip">
      <span
        ref={text}
        className="inline-block whitespace-nowrap pb-[0.08em] font-extrabold leading-[0.95] tracking-[-0.04em] text-secondary [font-stretch:125%]"
      >
        {name}
      </span>
    </div>
  )
}

export default function AuthLayout({ children }: { children: React.ReactNode }) {
  const { branding: b } = useBranding()
  const [probe, recheck] = usePanelProbe()
  const footer = b.footer_text || `© ${new Date().getFullYear()} ${b.name}`
  const headline = b.login_headline || b.tagline || "Sign in to manage your servers"

  return (
    <div className="min-h-screen bg-background text-foreground lg:grid lg:grid-cols-[minmax(0,1.25fr)_minmax(26rem,1fr)]">
      {/* Faceplate. Dark in both themes; its colours come from .faceplate. */}
      <section className="faceplate flex flex-col gap-10 bg-card px-6 py-7 sm:px-10 lg:min-h-screen lg:px-14 lg:py-10">
        <div className="flex flex-wrap items-center justify-between gap-x-8 gap-y-3">
          <div className="flex min-w-0 items-center gap-3">
            {/* An uploaded logo may be dark on a clear ground, so it sits on a light tile here. */}
            <BrandMark className={`h-9 w-9 shrink-0 rounded-md object-contain ${b.logo ? "bg-white p-0.5" : ""}`} />
            <span className="truncate text-lg font-semibold [font-stretch:112%]">{b.name}</span>
          </div>
          <p role="status" className="flex items-center gap-2 text-sm">
            <span
              aria-hidden="true"
              className={`h-2.5 w-2.5 shrink-0 rounded-full ${probe.state === "up" ? "bg-success" : probe.state === "down" ? "bg-destructive" : "bg-muted-foreground"}`}
            />
            {probe.state === "up" ? (
              <span>
                Panel online<span className="text-muted-foreground">, answered in {probe.ms} ms</span>
              </span>
            ) : probe.state === "down" ? (
              <span>
                The panel is not answering.{" "}
                <button type="button" onClick={recheck} className="font-semibold underline underline-offset-4">
                  Check again
                </button>
              </span>
            ) : (
              <span className="text-muted-foreground">Checking the panel</span>
            )}
          </p>
        </div>

        <div className="lg:my-auto">
          <h1 className="max-w-[16ch] text-[clamp(2rem,4.6vw,4.25rem)] font-semibold leading-[1.02] tracking-[-0.02em] text-balance">
            {headline}
          </h1>
          {b.tagline && b.tagline !== headline && <p className="mt-4 max-w-[46ch] text-lg text-muted-foreground">{b.tagline}</p>}
          {b.login_message && (
            <aside aria-label={`Notice from ${b.name}`} className="mt-8 max-w-[52ch] rounded-md border border-border px-4 py-3">
              <p className="text-sm font-semibold">Notice from {b.name}</p>
              <p className="mt-1 leading-relaxed text-muted-foreground">{b.login_message}</p>
            </aside>
          )}
        </div>

        <div className="hidden lg:block">
          <Nameplate name={b.name} />
        </div>

        <div className="flex flex-wrap items-center justify-between gap-x-8 gap-y-2 text-sm text-muted-foreground">
          <p>{footer}</p>
          {(b.support_url || b.support_email) && (
            <nav aria-label="Help" className="flex flex-wrap items-center gap-x-6 gap-y-1">
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
        </div>
      </section>

      <main className="flex items-center justify-center px-6 py-10 sm:px-10">
        <div className="w-full max-w-sm">{children}</div>
      </main>
    </div>
  )
}
