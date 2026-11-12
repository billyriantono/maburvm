"use client"

import * as React from "react"
import { Loader2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { useUser, useUsers } from "@/lib/hooks/use-users"
import type { User } from "@/types"

interface UserPickerProps {
  value: string
  onChange: (id: string, user?: User) => void
  id?: string
  allowNone?: boolean
  noneLabel?: string
  disabled?: boolean
  role?: string
}

const PAGE = 8

export function UserPicker({ value, onChange, id, allowNone, noneLabel = "None", disabled, role }: UserPickerProps) {
  const [choosing, setChoosing] = React.useState(false)
  const [known, setKnown] = React.useState<User | undefined>()
  const knownMatches = known && known.id === value ? known : undefined
  const { data: fetched } = useUser(value, { enabled: !!value && !knownMatches } as never)
  const current = knownMatches ?? fetched

  const pick = (u?: User) => {
    setKnown(u)
    setChoosing(false)
    onChange(u?.id ?? "", u)
  }

  if (!choosing) {
    const label = value
      ? current
        ? `${current.email}${current.name ? ` (${current.name})` : ""}`
        : "Loading user"
      : allowNone ? noneLabel : "No user selected"
    return (
      <div className="flex items-center justify-between gap-2 rounded-md border border-input bg-background px-3 h-12 text-sm">
        <span className="truncate">{label}</span>
        <Button type="button" variant="outline" size="sm" id={id} disabled={disabled} onClick={() => setChoosing(true)}>
          Change
        </Button>
      </div>
    )
  }

  return (
    <Chooser id={id} role={role} allowNone={allowNone} noneLabel={noneLabel} onPick={pick} onCancel={() => setChoosing(false)} />
  )
}

interface ChooserProps {
  id?: string
  role?: string
  allowNone?: boolean
  noneLabel: string
  onPick: (u?: User) => void
  onCancel: () => void
}

function Chooser({ id, role, allowNone, noneLabel, onPick: pick, onCancel: cancel }: ChooserProps) {
  const [text, setText] = React.useState("")
  const [search, setSearch] = React.useState("")
  const listRef = React.useRef<HTMLDivElement>(null)
  const inputRef = React.useRef<HTMLInputElement>(null)

  React.useEffect(() => {
    const t = setTimeout(() => setSearch(text.trim()), 250)
    return () => clearTimeout(t)
  }, [text])

  const { data, isLoading, isError, refetch } = useUsers({ search, pageSize: PAGE, role })

  const buttons = () => Array.from(listRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? [])
  const onListKey = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return
    e.preventDefault()
    const b = buttons()
    const i = b.indexOf(document.activeElement as HTMLButtonElement)
    if (e.key === "ArrowDown") b[Math.min(i + 1, b.length - 1)]?.focus()
    else if (i <= 0) inputRef.current?.focus()
    else b[i - 1]?.focus()
  }

  const users = data?.data ?? []
  const showSpinner = isLoading
  return (
    <div className="space-y-2" onKeyDown={(e) => { if (e.key === "Escape") { e.preventDefault(); cancel() } }}>
      <Input
        ref={inputRef}
        id={id}
        type="search"
        autoComplete="off"
        autoFocus
        placeholder="Search by email or name"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") { e.preventDefault(); buttons()[0]?.focus() }
        }}
      />
      <div ref={listRef} onKeyDown={onListKey} className="max-h-64 overflow-y-auto rounded-md border border-input bg-background text-sm">
        {allowNone && !search && (
          <button type="button" onClick={() => pick(undefined)} className="block w-full px-3 py-2 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none">
            {noneLabel}
          </button>
        )}
        {showSpinner ? (
          <div className="flex items-center gap-2 px-3 py-2 text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Searching
          </div>
        ) : isError ? (
          <div className="flex items-center justify-between gap-2 px-3 py-2">
            <span className="text-destructive">Could not load users</span>
            <Button type="button" variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </div>
        ) : users.length === 0 ? (
          <div className="px-3 py-2 text-muted-foreground">No user matches &quot;{search}&quot;</div>
        ) : (
          users.map((u) => (
            <button key={u.id} type="button" onClick={() => pick(u)} className="flex w-full flex-col px-3 py-2 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none">
              <span>{u.email}</span>
              {u.name && <span className="text-xs text-muted-foreground">{u.name}</span>}
            </button>
          ))
        )}
        {!showSpinner && !isError && data && data.total > users.length && (
          <div className="border-t px-3 py-2 text-xs text-muted-foreground">
            Showing {users.length} of {data.total}. Type to narrow down.
          </div>
        )}
      </div>
      <Button type="button" variant="ghost" size="sm" onClick={cancel}>Cancel</Button>
    </div>
  )
}
