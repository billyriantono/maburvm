"use client"

import { createContext, useCallback, useContext, useRef, useState } from "react"
import { AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"

// A confirmation that can actually explain itself.
//
// This replaces window.confirm, which could not: it renders unstyled browser
// chrome, shows a single line of plain text, and offers exactly two answers. In
// practice that meant destructive actions were confirmed with a bare identifier
// and no context — "Put 00:16:3e:07:32:2c back on the network?" — and one call
// site had resorted to overloading Cancel to mean "keep the data", which reads
// as "abort" to every user alive.
//
// The API is deliberately shaped like window.confirm (await it, get a boolean)
// so call sites stay one line and nobody is tempted to skip it.

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
}

export type ConfirmResult = "confirm" | "alternate" | "cancel"

type ConfirmFn = {
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
  // Held in a ref, not state: resolving is a side effect of the user's answer
  // and must not depend on a render having happened first.
  const resolver = useRef<((result: ConfirmResult) => void) | null>(null)

  const choose = useCallback((opts: ConfirmOptions) => {
    setOptions(opts)
    return new Promise<ConfirmResult>((resolve) => {
      resolver.current = resolve
    })
  }, [])

  const answer = useCallback((result: ConfirmResult) => {
    setOptions(null)
    resolver.current?.(result)
    resolver.current = null
  }, [])

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
        // Dismissing by clicking away or pressing Escape means "no". Anything
        // else would let a destructive action through by accident.
        onOpenChange={(open) => !open && answer("cancel")}
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

              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => answer("cancel")}>
                  {options.cancelLabel ?? "Cancel"}
                </Button>
                {options.alternateLabel && (
                  <Button type="button" variant="secondary" onClick={() => answer("alternate")}>
                    {options.alternateLabel}
                  </Button>
                )}
                <Button
                  type="button"
                  variant={options.destructive ? "destructive" : "default"}
                  onClick={() => answer("confirm")}
                >
                  {options.confirmLabel ?? "Confirm"}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </ConfirmContext.Provider>
  )
}
