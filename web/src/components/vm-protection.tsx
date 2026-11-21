"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { parseTags } from "@/lib/tags"
import { useSetVMLock, useSetVMTags, useStaffNoteActions, useStaffNotes, type StaffNote } from "@/lib/hooks/use-admin-extras"

const lockWords: Record<string, string> = {
  "": "Not locked. It can be deleted and reinstalled.",
  owner: "Locked by its owner: it cannot be deleted or reinstalled until it is unlocked. The billing system can still remove it.",
  staff: "Locked by staff: it cannot be deleted or reinstalled by anyone, the billing system included, until staff unlock it.",
}

/**
 * The VM's tags and its lock against deletion and reinstall. Owners and staff
 * both see and change them; an owner cannot lift a staff lock.
 */
export function VMLabelsAndLock({ vm, isAdmin, disabled }: {
  vm: { id: string; hostname: string; delete_lock?: string; tags?: string[] | null }
  isAdmin: boolean
  disabled?: boolean
}) {
  const confirm = useConfirm()
  const setLock = useSetVMLock()
  const setTags = useSetVMTags()
  const saved = (vm.tags ?? []).join(", ")
  const [text, setText] = useState(saved)
  useEffect(() => setText(saved), [saved])
  const lock = vm.delete_lock ?? ""
  const ownerBlocked = !isAdmin && lock === "staff"

  const saveTags = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      const out = await setTags.mutateAsync({ vmId: vm.id, tags: parseTags(text) })
      setText(out.tags.join(", "))
      toast.success("Tags saved")
    } catch (err) {
      toast.error(`Could not save the tags: ${(err as Error).message}`)
    }
  }

  const toggleLock = async () => {
    const locking = lock === "" || (isAdmin && lock === "owner")
    if (!locking && !(await confirm({
      title: `Unlock ${vm.hostname}?`,
      description: "It can then be deleted or reinstalled again.",
      confirmLabel: "Unlock",
    }))) return
    try {
      await setLock.mutateAsync({ vmId: vm.id, locked: locking })
      toast.success(locking ? `${vm.hostname} is locked` : `${vm.hostname} is unlocked`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <section className="divide-y rounded-lg border bg-card text-card-foreground">
      <h2 className="px-6 py-4 text-base font-semibold">Tags and lock</h2>
      <form onSubmit={saveTags} className="flex flex-wrap items-end gap-3 px-6 py-4">
        <div className="min-w-0 flex-1 basis-64">
          <label htmlFor={`tags-${vm.id}`} className="text-sm font-medium">Tags</label>
          <p className="text-sm text-muted-foreground">Up to 10 short labels, separated by commas, such as web or env:prod. The VM list can be filtered by them.</p>
          <Input id={`tags-${vm.id}`} className="mt-2 font-mono" value={text} disabled={disabled} onChange={(e) => setText(e.target.value)} placeholder="web, env:prod" />
        </div>
        <Button type="submit" variant={text !== saved ? "default" : "outline"} size="sm" disabled={disabled || setTags.isPending || text === saved}>
          {setTags.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          Save tags
        </Button>
      </form>
      <div className="flex flex-wrap items-center justify-between gap-3 px-6 py-4">
        <div className="min-w-0 flex-1 basis-64">
          <p className="text-sm font-medium">Lock against deletion</p>
          <p className="text-sm text-muted-foreground">{lockWords[lock] ?? lockWords[""]}</p>
        </div>
        {!ownerBlocked && (
          <Button variant="outline" size="sm" disabled={disabled || setLock.isPending} onClick={toggleLock}>
            {lock === "" ? "Lock" : isAdmin && lock === "owner" ? "Lock as staff" : "Unlock"}
          </Button>
        )}
        {isAdmin && lock === "owner" && (
          <Button variant="ghost" size="sm" disabled={disabled || setLock.isPending} onClick={async () => {
            if (!(await confirm({ title: `Unlock ${vm.hostname}?`, description: "Its owner locked it. It can then be deleted or reinstalled again.", confirmLabel: "Unlock" }))) return
            try {
              await setLock.mutateAsync({ vmId: vm.id, locked: false })
              toast.success(`${vm.hostname} is unlocked`)
            } catch (err) {
              toast.error((err as Error).message)
            }
          }}>
            Unlock
          </Button>
        )}
      </div>
    </section>
  )
}

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })

/** Notes staff keep on a VM or an account. Customers never see them. */
export function StaffNotes({ subject, id, canWrite }: { subject: "vms" | "users"; id: string; canWrite: boolean }) {
  const confirm = useConfirm()
  const { data, isLoading, error } = useStaffNotes(subject, id)
  const { add, update, remove } = useStaffNoteActions(subject, id)
  const [draft, setDraft] = useState("")
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await add.mutateAsync(draft)
      setDraft("")
    } catch (err) {
      toast.error(`Could not save the note: ${(err as Error).message}`)
    }
  }
  const saveEdit = async (n: StaffNote) => {
    if (!editing) return
    try {
      await update.mutateAsync({ noteId: n.id, body: editing.body })
      setEditing(null)
    } catch (err) {
      toast.error(`Could not save the note: ${(err as Error).message}`)
    }
  }

  return (
    <section aria-labelledby={`notes-${id}`} className="rounded-lg border bg-card text-card-foreground">
      <div className="border-b px-6 py-4">
        <h2 id={`notes-${id}`} className="text-base font-semibold">Staff notes</h2>
        <p className="text-sm text-muted-foreground">Only staff see these. Every change is in the audit log.</p>
      </div>
      <div className="divide-y">
        {isLoading ? (
          <p className="px-6 py-4 text-sm text-muted-foreground">Loading notes</p>
        ) : error ? (
          <p className="px-6 py-4 text-sm" role="alert">The notes could not be loaded: {(error as Error).message}</p>
        ) : !data?.length ? (
          <p className="px-6 py-4 text-sm text-muted-foreground">No notes yet.</p>
        ) : (
          data.map((n) => (
            <div key={n.id} className="px-6 py-4">
              {editing?.id === n.id ? (
                <div className="space-y-2">
                  <label htmlFor={`edit-${n.id}`} className="sr-only">Note</label>
                  <textarea id={`edit-${n.id}`} rows={3} value={editing.body} onChange={(e) => setEditing({ id: n.id, body: e.target.value })}
                    className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" />
                  <div className="flex gap-2">
                    <Button size="sm" disabled={update.isPending || !editing.body.trim()} onClick={() => saveEdit(n)}>Save</Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
                  </div>
                </div>
              ) : (
                <>
                  <p className="whitespace-pre-wrap break-words text-sm">{n.body}</p>
                  <div className="mt-1 flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground">
                    <span>{n.author_email || "Staff"}, {when(n.created_at)}{n.updated_at !== n.created_at ? `, changed ${when(n.updated_at)}` : ""}</span>
                    {canWrite && (
                      <>
                        <button type="button" className="font-medium text-foreground underline underline-offset-2" onClick={() => setEditing({ id: n.id, body: n.body })}>Edit</button>
                        <button type="button" className="font-medium text-destructive underline underline-offset-2" onClick={async () => {
                          if (await confirm({ title: "Delete this note?", description: "It cannot be brought back.", confirmLabel: "Delete note", destructive: true })) {
                            remove.mutate(n.id, { onError: (err) => toast.error(`Could not delete the note: ${err.message}`) })
                          }
                        }}>Delete</button>
                      </>
                    )}
                  </div>
                </>
              )}
            </div>
          ))
        )}
      </div>
      {canWrite && (
        <form onSubmit={submit} className="space-y-2 border-t px-6 py-4">
          <label htmlFor={`new-note-${id}`} className="text-sm font-medium">Add a note</label>
          <textarea id={`new-note-${id}`} rows={3} value={draft} maxLength={4000} onChange={(e) => setDraft(e.target.value)}
            className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" />
          <Button type="submit" size="sm" variant={draft.trim() ? "default" : "outline"} disabled={add.isPending || !draft.trim()}>
            {add.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
            Add note
          </Button>
        </form>
      )}
    </section>
  )
}
