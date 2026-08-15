"use client"

import { useState } from "react"
import { Loader2, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// VMNameEditor lets the name be changed in place.
//
// Machines arrive with names like "v1064" — an identifier from wherever they
// were imported from, meaningful to nobody. Until now neither admins nor
// customers could change it, though the API has always accepted a rename.
//
// It renames the record, not the guest. The operating system inside keeps
// whatever hostname it was configured with: cloud-init only runs at build time,
// so writing a new one would need a rebuild. Saying so on the control is the
// difference between a label and a broken promise.
export function VMNameEditor({
  hostname,
  onRename,
  headingClassName = "text-2xl lg:text-3xl font-semibold text-foreground",
}: {
  hostname: string
  onRename: (name: string) => Promise<void>
  /** Lets each page keep its own heading size without forking the component. */
  headingClassName?: string
}) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(hostname)
  const [saving, setSaving] = useState(false)

  if (!editing) {
    return (
      <button
        type="button"
        onClick={() => {
          setValue(hostname)
          setEditing(true)
        }}
        className="group flex items-center gap-2 text-left"
        title="Rename"
      >
        <h1 className={headingClassName}>{hostname}</h1>
        <Pencil className="w-4 h-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
      </button>
    )
  }

  const commit = async () => {
    const name = value.trim()
    if (!name || name === hostname) {
      setEditing(false)
      return
    }
    setSaving(true)
    try {
      await onRename(name)
      setEditing(false)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex items-center gap-2">
      <Input
        autoFocus
        value={value}
        disabled={saving}
        maxLength={100}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit()
          if (e.key === "Escape") setEditing(false)
        }}
        className="text-xl font-semibold h-auto py-1"
      />
      <Button type="button" size="sm" onClick={commit} disabled={saving}>
        {saving ? <Loader2 className="w-4 h-4 animate-spin" /> : "Save"}
      </Button>
      <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)} disabled={saving}>
        Cancel
      </Button>
    </div>
  )
}
