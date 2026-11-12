"use client"

import { useEffect, useState } from "react"
import { Check, Loader2, X } from "lucide-react"
import { cn } from "@/lib/utils"

export type StepState = "done" | "active" | "pending" | "failed"

export interface ProgressStep {
  label: string
  state: StepState
  /** Optional second line, e.g. the server's own step label or an error. */
  detail?: string
}

function formatElapsed(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000))
  const m = Math.floor(s / 60)
  return `${m}:${String(s % 60).padStart(2, "0")}`
}

// Ticks once a second while `running`; freezes on the last value afterwards.
// ponytail: measured against the browser clock, so a skewed client clock shifts
// the number. Have the API return elapsed seconds if that ever matters.
function useElapsed(startedAt: string | undefined, running: boolean) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!running) return
    setNow(Date.now())
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [running])
  if (!startedAt) return null
  const start = Date.parse(startedAt)
  return Number.isNaN(start) ? null : formatElapsed(now - start)
}

// OperationProgress shows a long-running job as the steps it really goes
// through. It never invents a percentage: a step is done, in progress, or not
// started, and the only number is the time actually elapsed.
export function OperationProgress({
  title,
  steps,
  startedAt,
  className,
  children,
}: {
  title: string
  steps: ProgressStep[]
  /** ISO timestamp the job started; omit to hide the timer. */
  startedAt?: string
  className?: string
  children?: React.ReactNode
}) {
  const failed = steps.some((s) => s.state === "failed")
  const activeIndex = steps.findIndex((s) => s.state === "active")
  const running = activeIndex !== -1 && !failed
  const allDone = steps.length > 0 && steps.every((s) => s.state === "done")
  const elapsed = useElapsed(startedAt, running)

  const position = failed
    ? "Stopped"
    : allDone
      ? "Finished"
      : `Step ${Math.max(activeIndex, 0) + 1} of ${steps.length}`

  return (
    <section className={cn("rounded-lg border bg-card p-6 text-card-foreground shadow-sm", className)}>
      <div className="flex items-start justify-between gap-4">
        <div role="status" aria-live="polite">
          <p className="text-xs font-medium text-muted-foreground">{position}</p>
          <h2 className="mt-1 text-lg font-semibold leading-tight">{title}</h2>
        </div>
        {elapsed && (
          <p className="shrink-0 text-right text-sm tabular-nums text-muted-foreground">
            <span className="sr-only">Elapsed </span>
            {elapsed}
          </p>
        )}
      </div>

      {/* One segment per step. The active one slides to say "working" without
          claiming a percentage we do not have. */}
      <div className="mt-4 flex gap-1" aria-hidden="true">
        {steps.map((s, i) => (
          <div key={i} className="relative h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
            <div
              className={cn(
                "absolute inset-0 origin-left rounded-full transition-transform duration-500 ease-out",
                s.state === "done" && "scale-x-100 bg-primary",
                s.state === "failed" && "scale-x-100 bg-destructive",
                (s.state === "pending" || s.state === "active") && "scale-x-0 bg-primary"
              )}
            />
            {s.state === "active" && (
              <div className="absolute inset-y-0 left-0 w-2/5 animate-progress-slide rounded-full bg-primary/70" />
            )}
          </div>
        ))}
      </div>

      <ol className="mt-6">
        {steps.map((s, i) => {
          const last = i === steps.length - 1
          return (
            <li
              key={i}
              aria-current={s.state === "active" ? "step" : undefined}
              className="relative flex gap-3 pb-5 last:pb-0"
            >
              {!last && (
                <span
                  aria-hidden="true"
                  className={cn(
                    "absolute left-3 top-6 -ml-px h-[calc(100%-1.5rem)] w-px transition-colors duration-500",
                    s.state === "done" ? "bg-primary" : "bg-border"
                  )}
                />
              )}
              <span
                className={cn(
                  "relative flex h-6 w-6 shrink-0 items-center justify-center rounded-full border transition-colors duration-300",
                  s.state === "done" && "border-primary bg-primary text-primary-foreground",
                  s.state === "active" && "border-primary bg-background text-primary",
                  s.state === "pending" && "border-border bg-background text-muted-foreground",
                  s.state === "failed" && "border-destructive bg-destructive text-destructive-foreground"
                )}
              >
                {s.state === "done" && <Check className="h-3.5 w-3.5 animate-in zoom-in-50 duration-300" aria-hidden="true" />}
                {s.state === "active" && <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />}
                {s.state === "failed" && <X className="h-3.5 w-3.5" aria-hidden="true" />}
              </span>
              <div className="min-w-0 pt-0.5">
                <p
                  className={cn(
                    "text-sm transition-colors duration-300",
                    s.state === "pending" ? "text-muted-foreground" : "font-medium text-foreground"
                  )}
                >
                  {s.label}
                  <span className="sr-only">
                    {s.state === "done" ? " (done)" : s.state === "active" ? " (in progress)" : s.state === "failed" ? " (failed)" : " (not started)"}
                  </span>
                </p>
                {s.detail && (
                  <p className={cn("mt-0.5 break-words text-xs", s.state === "failed" ? "text-destructive" : "text-muted-foreground")}>
                    {s.detail}
                  </p>
                )}
              </div>
            </li>
          )
        })}
      </ol>

      {children && <div className="mt-6">{children}</div>}
    </section>
  )
}

// The steps a VM build goes through, derived from the only signal the panel
// has: the VM's status. The node does disk + OS install in one call, so that is
// one honest step rather than several guessed ones.
export function vmProvisionSteps(status: string, osName?: string): ProgressStep[] {
  const built = status === "running" || status === "stopped" || status === "suspended"
  const failed = status === "error"
  return [
    { label: "Request accepted", state: "done" },
    {
      label: osName ? `Building the disk and installing ${osName}` : "Building the disk and installing the OS",
      state: failed ? "failed" : built ? "done" : "active",
      detail: failed ? "The node reported an error while building this VM." : undefined,
    },
    {
      label: status === "stopped" ? "Built (currently stopped)" : "Booted and running",
      state: built ? "done" : "pending",
    },
  ]
}
