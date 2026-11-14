"use client"

import Link from "next/link"
import { Check, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useDismissSetup, useSetup } from "@/lib/hooks/use-ops"

// The steps are a real sequence: each one is needed before the next is useful
// (no VM without a node, no address without a range), so they are numbered.
export default function SetupPage() {
  const { data, isLoading, error, refetch } = useSetup()
  const dismiss = useDismissSetup()

  if (isLoading) {
    return <p className="text-sm text-muted-foreground flex items-center gap-2"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Checking what is set up…</p>
  }
  if (error || !data) {
    return (
      <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6">
        <p className="font-semibold">The setup list could not be loaded</p>
        <p className="text-sm text-muted-foreground mt-1">{(error as Error | null)?.message}</p>
        <Button variant="outline" size="sm" className="mt-4" onClick={() => refetch()}>Try again</Button>
      </div>
    )
  }

  const done = data.steps.filter((s) => s.done).length
  const next = data.steps.find((s) => !s.done)

  return (
    <div className="max-w-3xl">
      <h1 className="text-3xl font-bold tracking-tight">Getting set up</h1>
      <p className="text-muted-foreground text-sm mt-1">
        {next
          ? `${done} of ${data.steps.length} done. Each line is checked against the panel, so it ticks itself off when you finish it.`
          : "Everything on this list is done. The panel is ready to take orders."}
      </p>

      <div
        role="progressbar"
        aria-label="Setup progress"
        aria-valuemin={0}
        aria-valuemax={data.steps.length}
        aria-valuenow={done}
        className="mt-5 h-2 rounded-full bg-muted overflow-hidden"
      >
        <div className="h-full bg-primary transition-[width] duration-300" style={{ width: `${(done / data.steps.length) * 100}%` }} />
      </div>

      <ol className="mt-6 rounded-lg border bg-card shadow-sm divide-y">
        {data.steps.map((step, i) => {
          const isNext = step.id === next?.id
          return (
            <li key={step.id} className={`flex flex-wrap items-center gap-x-4 gap-y-2 p-4 sm:p-5 ${isNext ? "bg-muted/50" : ""}`}>
              <span
                className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-full border text-sm font-semibold ${step.done ? "border-primary bg-primary text-primary-foreground" : "border-input text-muted-foreground"}`}
              >
                {step.done ? <Check className="h-4 w-4" aria-hidden="true" /> : i + 1}
                <span className="sr-only">{step.done ? "Done:" : "To do:"}</span>
              </span>
              <div className="min-w-0 flex-1 basis-56">
                <p className={`font-medium ${step.done ? "text-muted-foreground" : ""}`}>{step.title}</p>
                <p className="text-sm text-muted-foreground mt-0.5">{step.why}</p>
              </div>
              <Link href={step.href}>
                <Button variant={isNext ? "default" : "outline"} size="sm">{step.done ? "Review" : isNext ? "Do this next" : "Open"}</Button>
              </Link>
            </li>
          )
        })}
      </ol>

      <div className="mt-5 flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
        {data.dismissed ? (
          <>
            The reminder on the dashboard is hidden.
            <Button variant="ghost" size="sm" onClick={() => dismiss.mutate(false)} disabled={dismiss.isPending}>Show it again</Button>
          </>
        ) : next ? (
          <>
            The dashboard reminds you of this list until it is finished.
            <Button variant="ghost" size="sm" onClick={() => dismiss.mutate(true)} disabled={dismiss.isPending}>Hide the reminder</Button>
          </>
        ) : null}
      </div>
    </div>
  )
}
