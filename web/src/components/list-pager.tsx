"use client"

import { Button } from "@/components/ui/button"

/** Previous and Next under a list, with where you are; nothing for one page. */
export function ListPager({ page, pageSize, total, onPage, noun = "in all" }: {
  page: number
  pageSize: number
  total: number
  onPage: (p: number) => void
  noun?: string
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  if (pages <= 1) return null
  return (
    <nav aria-label="Pages" className="flex flex-wrap items-center justify-between gap-3 text-sm">
      <p className="text-muted-foreground tabular-nums">Page {page} of {pages}, {total.toLocaleString()} {noun}</p>
      <div className="flex gap-2">
        <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => onPage(page - 1)}>Previous</Button>
        <Button size="sm" variant="outline" disabled={page >= pages} onClick={() => onPage(page + 1)}>Next</Button>
      </div>
    </nav>
  )
}
