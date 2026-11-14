// A thin bar for "this much of that". The figures it stands for are always
// written beside it, so it is hidden from screen readers.
export function Meter({ percent, over }: { percent: number; over?: boolean }) {
  return (
    <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
      <div className={`h-full ${over ? "bg-destructive" : "bg-foreground"}`} style={{ width: `${Math.min(Math.max(percent, 0), 100)}%` }} />
    </div>
  )
}
