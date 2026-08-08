"use client"

import { createContext, useCallback, useContext, useRef, useState } from "react"
import { AlertTriangle, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"

// The one confirmation in the panel.
//
// It replaces window.confirm, which could not explain itself — unstyled browser
// chrome, one line of text, two answers — and eight hand-rolled dialogs that had
// each drifted apart, so the same action felt different depending on the page it
// was on.
//
// Pass `action` and the dialog owns the whole operation: it stays open with a
// spinner while the work runs, closes on success, and shows the failure in place
// if it fails. That last part is the reason to do it here rather than at each
// call site — a dialog that closes and fires a toast asks the person to catch a
// message they were not looking at, for an action they just chose to take.

export interface ConfirmOptions {
  title: string
  /** What will happen, and anything the person should weigh before saying yes. */
  description?: string
  confirmLabel?: string
  cancelLabel?: string
  /** Styles the confirm button as destructive. Use for anything irreversible. */
  destructive?: boolean
  /**
   * A third answer, for choices that are genuinely three-way. Returned as
   * "alternate". Only use it when the alternative is a real option rather than
   * a variation of cancelling — otherwise the dialog becomes a quiz.
   */
  alternateLabel?: string
  /** Extra detail rendered above the buttons: identifiers, counts, warnings. */
  details?: { label: string; value: React.ReactNode }[]
  /**
   * The work to perform once confirmed. The dialog holds open until it settles,
   * so the person sees the outcome of the thing they just asked for. Omit it
   * only when there is no async work to wait on.
   *
   * Receives which answer was given, for three-way choices.
   */
  action?: (answer: "confirm" | "alternate") => Promise<unknown>
}

export type ConfirmResult = "confirm" | "alternate" | "cancel"

type ConfirmFn = {
  /** Resolves true only if the action ran and succeeded (or there was none). */
  (options: ConfirmOptions): Promise<boolean>
  /** For three-way choices; returns which answer was given. */
  choose(options: ConfirmOptions): Promise<ConfirmResult>
}

const ConfirmContext = createContext<ConfirmFn | null>(null)

export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext)
  if (!ctx) {
    throw new Error("useConfirm must be used inside <ConfirmProvider>")
  }
  return ctx
}

export function ConfirmProvider({ children }: { children: React.ReactNode }) {
  const [options, setOptions] = useState<ConfirmOptions | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Held in a ref, not state: resolving is a side effect of the user's answer
  // and must not wait on a render.
  const resolver = useRef<((result: ConfirmResult) => void) | null>(null)

  const choose = useCallback((opts: ConfirmOptions) => {
    setOptions(opts)
    setError(null)
    setBusy(false)
    return new Promise<ConfirmResult>((resolve) => {
      resolver.current = resolve
    })
  }, [])

  const settle = useCallback((result: ConfirmResult) => {
    setOptions(null)
    setBusy(false)
    setError(null)
    resolver.current?.(result)
    resolver.current = null
  }, [])

  const answer = useCallback(
    async (result: ConfirmResult) => {
      if (result === "cancel" || !options?.action) {
        settle(result)
        return
      }
      setBusy(true)
      setError(null)
      try {
        await options.action(result)
        settle(result)
      } catch (err) {
        // Stay open. The person is still looking at this dialog, and closing it
        // would hide the only explanation of why nothing happened.
        setError((err as Error).message || "Something went wrong")
        setBusy(false)
      }
    },
    [options, settle]
  )

  const confirm = useCallback(
    (opts: ConfirmOptions) => choose(opts).then((r) => r === "confirm"),
    [choose]
  ) as ConfirmFn
  confirm.choose = choose

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog
        open={!!options}
        onOpenChange={(open) => {
          // Dismissing by clicking away or pressing Escape means "no". Ignored
          // while the action is running: the work is already underway and
          // pretending it was cancelled would be a lie.
          if (!open && !busy) settle("cancel")
        }}
      >
        <DialogContent>
          {options && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2">
                  {options.destructive && (
                    <AlertTriangle className="w-5 h-5 text-destructive shrink-0" />
                  )}
                  {options.title}
                </DialogTitle>
                {options.description && (
                  <DialogDescription>{options.description}</DialogDescription>
                )}
              </DialogHeader>

              {options.details && options.details.length > 0 && (
                <div className="rounded-md border bg-muted/40 p-3 space-y-1 text-sm">
                  {options.details.map((d) => (
                    <div key={d.label} className="flex justify-between gap-3">
                      <span className="text-muted-foreground">{d.label}</span>
                      <span className="font-medium text-right">{d.value}</span>
                    </div>
                  ))}
                </div>
              )}

              {error && (
                <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
                  {error}
                </div>
              )}

              <DialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => settle("cancel")}
                >
                  {options.cancelLabel ?? "Cancel"}
                </Button>
                {options.alternateLabel && (
                  <Button
                    type="button"
                    variant="secondary"
                    disabled={busy}
                    onClick={() => answer("alternate")}
                  >
                    {options.alternateLabel}
                  </Button>
                )}
                <Button
                  type="button"
                  variant={options.destructive ? "destructive" : "default"}
                  disabled={busy}
                  onClick={() => answer("confirm")}
                >
                  {busy && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
                  {error ? "Try again" : (options.confirmLabel ?? "Confirm")}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </ConfirmContext.Provider>
  )
}
