"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Loader2 } from "lucide-react"
import { cn } from "@/lib/utils"

// One status vocabulary for the whole panel. Motion is reserved for states
// where something is actually happening: a steady state (running, active) gets
// a solid dot, a transition gets a spinner.
export type VMTransition = "starting" | "stopping" | "restarting"

type Tone = "ok" | "idle" | "warn" | "info" | "bad"

const STATUSES: Record<string, { tone: Tone; label: string; busy?: boolean }> = {
  // VM
  running: { tone: "ok", label: "Running" },
  stopped: { tone: "idle", label: "Stopped" },
  suspended: { tone: "warn", label: "Suspended" },
  creating: { tone: "info", label: "Creating", busy: true },
  deleting: { tone: "warn", label: "Deleting", busy: true },
  error: { tone: "bad", label: "Error" },
  starting: { tone: "info", label: "Starting", busy: true },
  stopping: { tone: "info", label: "Stopping", busy: true },
  restarting: { tone: "info", label: "Restarting", busy: true },
  // Backups, snapshots and other jobs
  completed: { tone: "ok", label: "Completed" },
  failed: { tone: "bad", label: "Failed" },
  pending: { tone: "info", label: "Waiting", busy: true },
  in_progress: { tone: "info", label: "In progress", busy: true },
  // Firewall rules
  allow: { tone: "ok", label: "Allow" },
  deny: { tone: "bad", label: "Deny" },
  // Node
  active: { tone: "ok", label: "Active" },
  maintenance: { tone: "warn", label: "Maintenance" },
  offline: { tone: "bad", label: "Offline" },
}

// Lamp colours come from the theme tokens, so they stay readable on the dark
// faceplate as well as on a normal panel.
const LAMPS: Record<Tone, string> = {
  ok: "bg-success",
  idle: "bg-muted-foreground",
  warn: "bg-warning",
  info: "bg-muted-foreground",
  bad: "bg-destructive",
}

// A state is always a lamp and a word, never a coloured pill (see DESIGN.md).
// "lamp" is the larger size, for use beside a title; the default suits a table row.
export function StatusBadge({
  status,
  transition,
  className,
  variant = "badge",
}: {
  status: string
  /** A requested power action the server has not reflected yet. */
  transition?: VMTransition | null
  className?: string
  variant?: "badge" | "lamp"
}) {
  const shown = transition || status
  const cfg = STATUSES[shown] ?? { tone: "idle" as Tone, label: shown.replace(/_/g, " ") || "Unknown" }
  const large = variant === "lamp"
  return (
    <span className={cn("inline-flex items-center gap-2 whitespace-nowrap capitalize text-foreground", large ? "text-sm font-semibold" : "text-xs font-medium", className)}>
      {cfg.busy ? (
        <Loader2 className={cn("animate-spin", large ? "h-3.5 w-3.5" : "h-3 w-3")} aria-hidden="true" />
      ) : (
        <span className={cn("rounded-full", large ? "h-2.5 w-2.5" : "h-2 w-2", LAMPS[cfg.tone])} aria-hidden="true" />
      )}
      {cfg.label}
    </span>
  )
}

const TRANSITION_FOR: Record<string, VMTransition> = {
  start: "starting",
  stop: "stopping",
  "force-stop": "stopping",
  restart: "restarting",
}

// useVMTransitions remembers "the operator asked for X" between the click and
// the moment the server's status actually changes, so the badge can say
// "Stopping" instead of sitting on "Running" and then flipping.
//
// ponytail: cleared by a status change or a timeout, whichever comes first. A
// restart goes running -> running, so only the timeout ends it. If the panel
// starts reporting transitional statuses itself, delete this hook.
export function useVMTransitions(vms: { id: string; status: string }[], timeoutMs = 45_000) {
  const [pending, setPending] = useState<Record<string, { t: VMTransition; from: string }>>({})
  const timers = useRef<Record<string, ReturnType<typeof setTimeout>>>({})

  const clear = useCallback((id: string) => {
    clearTimeout(timers.current[id])
    delete timers.current[id]
    setPending((p) => {
      if (!(id in p)) return p
      const next = { ...p }
      delete next[id]
      return next
    })
  }, [])

  const begin = useCallback(
    (id: string, action: string, from: string) => {
      const t = TRANSITION_FOR[action]
      if (!t) return
      clearTimeout(timers.current[id])
      timers.current[id] = setTimeout(() => clear(id), t === "restarting" ? 20_000 : timeoutMs)
      setPending((p) => ({ ...p, [id]: { t, from } }))
    },
    [clear, timeoutMs]
  )

  // Server caught up: the status is no longer what it was at click time.
  const key = vms.map((v) => `${v.id}:${v.status}`).join(",")
  useEffect(() => {
    for (const v of vms) {
      const p = pending[v.id]
      if (p && v.status !== p.from) clear(v.id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, pending, clear])

  useEffect(() => {
    const t = timers.current
    return () => Object.values(t).forEach(clearTimeout)
  }, [])

  // While something is in transition, re-read VM state every few seconds. The
  // live event stream normally delivers the change first; this keeps the badge
  // honest when that stream is down, instead of waiting out the timeout.
  const queryClient = useQueryClient()
  const anyPending = Object.keys(pending).length > 0
  useEffect(() => {
    if (!anyPending) return
    const t = setInterval(() => queryClient.invalidateQueries({ queryKey: ["vms"] }), 3000)
    return () => clearInterval(t)
  }, [anyPending, queryClient])

  return {
    transitionOf: (id: string): VMTransition | null => pending[id]?.t ?? null,
    begin,
    /** Call when the action request itself failed. */
    cancel: clear,
  }
}
