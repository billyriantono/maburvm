"use client"

import { useEffect, useId, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { useQuery } from "@tanstack/react-query"
import { Loader2, Search } from "lucide-react"
import { api } from "@/lib/api-client"

interface SearchResults {
  vms: { id: string; hostname: string; status: string; ip_address: string; owner_email?: string }[]
  users: { id: string; email: string; name: string; role: string }[]
  nodes: { id: string; name: string; ip_address: string; status: string }[]
}

export interface SearchPage {
  href: string
  label: string
}

interface Row {
  key: string
  href: string
  title: string
  detail: string
  group: string
}

/**
 * The header search. One box for the things people find by a fragment they
 * remember: a hostname, an address, an ID, an email, or the name of a page.
 * "/" or Ctrl/Cmd+K jumps to it from anywhere.
 */
export function GlobalSearch({ pages, vmHref, admin }: {
  pages: SearchPage[]
  /** Where a VM result opens, which differs between the admin and customer areas. */
  vmHref: (id: string) => string
  admin: boolean
}) {
  const router = useRouter()
  const listId = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const boxRef = useRef<HTMLDivElement>(null)
  const [text, setText] = useState("")
  const [term, setTerm] = useState("")
  const [open, setOpen] = useState(false)
  const [cursor, setCursor] = useState(0)

  // Wait for a pause in typing before asking the server.
  useEffect(() => {
    const timer = setTimeout(() => setTerm(text.trim()), 200)
    return () => clearTimeout(timer)
  }, [text])

  const { data, isFetching, error } = useQuery<SearchResults>({
    queryKey: ["search", term],
    queryFn: async () => (await api.get<SearchResults>("/api/v1/search", { params: { q: term } })).data.data,
    enabled: term.length >= 2,
    staleTime: 15_000,
  })

  const rows = useMemo<Row[]>(() => {
    const q = text.trim().toLowerCase()
    if (!q) return []
    const out: Row[] = pages
      .filter((p) => p.label.toLowerCase().includes(q))
      .slice(0, 5)
      .map((p) => ({ key: `page:${p.href}`, href: p.href, title: p.label, detail: "", group: "Pages" }))
    if (term.length >= 2 && data) {
      for (const vm of data.vms) {
        out.push({
          key: `vm:${vm.id}`, href: vmHref(vm.id), title: vm.hostname, group: admin ? "Virtual machines" : "Your VMs",
          detail: [vm.ip_address, vm.status, vm.owner_email].filter(Boolean).join(", "),
        })
      }
      for (const u of data.users) {
        out.push({ key: `user:${u.id}`, href: `/users/${u.id}`, title: u.email, detail: [u.name, u.role].filter(Boolean).join(", "), group: "Users" })
      }
      for (const n of data.nodes) {
        out.push({ key: `node:${n.id}`, href: `/nodes/${n.id}`, title: n.name, detail: [n.ip_address, n.status].filter(Boolean).join(", "), group: "Nodes" })
      }
    }
    return out
  }, [text, term, data, pages, vmHref, admin])

  useEffect(() => { setCursor(0) }, [rows.length, text])

  // "/" and Ctrl/Cmd+K reach the box from anywhere, except while typing elsewhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      const typing = !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable)
      if ((e.key === "k" && (e.metaKey || e.ctrlKey)) || (e.key === "/" && !typing)) {
        e.preventDefault()
        inputRef.current?.focus()
        inputRef.current?.select()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => { if (!boxRef.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener("mousedown", close)
    return () => document.removeEventListener("mousedown", close)
  }, [open])

  const go = (row: Row) => {
    setOpen(false)
    setText("")
    inputRef.current?.blur()
    router.push(row.href)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") { setOpen(false); inputRef.current?.blur(); return }
    if (!rows.length) return
    if (e.key === "ArrowDown") { e.preventDefault(); setOpen(true); setCursor((c) => (c + 1) % rows.length) }
    if (e.key === "ArrowUp") { e.preventDefault(); setOpen(true); setCursor((c) => (c + rows.length - 1) % rows.length) }
    if (e.key === "Enter") { e.preventDefault(); go(rows[Math.min(cursor, rows.length - 1)]) }
  }

  const typed = text.trim()
  const waiting = typed.length >= 2 && (isFetching || term !== typed)
  const showPanel = open && typed.length > 0
  const activeId = rows.length ? `${listId}-opt-${Math.min(cursor, rows.length - 1)}` : undefined

  return (
    <div ref={boxRef} className="relative">
      <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
      <input
        ref={inputRef}
        type="search"
        role="combobox"
        aria-label={admin ? "Search VMs, addresses, users, nodes and pages" : "Search your VMs and pages"}
        aria-expanded={showPanel}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={showPanel ? activeId : undefined}
        autoComplete="off"
        spellCheck={false}
        value={text}
        placeholder={admin ? "Find a VM, IP, user or page" : "Find a VM or page"}
        onChange={(e) => { setText(e.target.value); setOpen(true) }}
        onFocus={() => setOpen(true)}
        onKeyDown={onKeyDown}
        className="h-10 w-56 rounded-md border border-input bg-background pl-10 pr-10 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 lg:w-80 [&::-webkit-search-cancel-button]:hidden"
      />
      {waiting ? (
        <Loader2 className="absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 animate-spin text-muted-foreground" aria-hidden="true" />
      ) : (
        <kbd aria-hidden="true" className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 rounded border bg-muted px-1.5 text-[11px] font-medium text-muted-foreground">/</kbd>
      )}

      {showPanel && (
        <div className="absolute right-0 z-50 mt-2 w-[min(28rem,calc(100vw-2rem))] overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-lg">
          <ul id={listId} role="listbox" aria-label="Search results" className="max-h-[60vh] overflow-y-auto py-1">
            {rows.map((row, i) => (
              <li key={row.key} role="presentation">
                {(i === 0 || rows[i - 1].group !== row.group) && (
                  <p className="px-3 pb-1 pt-2 text-xs font-medium text-muted-foreground">{row.group}</p>
                )}
                <div
                  id={`${listId}-opt-${i}`}
                  role="option"
                  aria-selected={i === cursor}
                  onMouseEnter={() => setCursor(i)}
                  onMouseDown={(e) => { e.preventDefault(); go(row) }}
                  className={`mx-1 flex cursor-pointer items-baseline justify-between gap-3 rounded-md px-2 py-1.5 text-sm ${i === cursor ? "bg-accent text-accent-foreground" : ""}`}
                >
                  <span className="truncate font-medium">{row.title}</span>
                  {row.detail && <span className="shrink-0 truncate text-xs text-muted-foreground max-w-[55%]">{row.detail}</span>}
                </div>
              </li>
            ))}
          </ul>
          <p role="status" className={`px-3 py-2 text-xs text-muted-foreground ${rows.length ? "border-t" : ""}`}>
            {error
              ? "Search is not answering. Try again in a moment."
              : typed.length < 2
                ? rows.length ? "Type one more character to search VMs as well." : "Type at least two characters."
                : waiting
                  ? "Searching…"
                  : rows.length
                    ? "Up and down to move, Enter to open."
                    : admin
                      ? `Nothing matches “${typed}”. Try part of a hostname, an IP address, an email or a VM ID.`
                      : `Nothing matches “${typed}”. Try part of a hostname or an IP address.`}
          </p>
        </div>
      )}
    </div>
  )
}
