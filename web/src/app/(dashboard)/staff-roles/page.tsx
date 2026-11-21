"use client"

import { FormEvent, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { can, useCurrentUser } from "@/lib/hooks/use-auth"
import { useDeleteStaffRole, useSaveStaffRole, useStaffRoles, type StaffRoleInput } from "@/lib/hooks/use-staff"
import type { PermissionArea, StaffRole } from "@/types"

const empty: StaffRoleInput = { name: "", description: "", permissions: [] }

// What a role holds, in words. The wildcard is the Administrator's.
function holds(role: StaffRole, areas: PermissionArea[]): string[] {
  if (role.permissions.includes("admin:access")) {
    return role.permissions.includes("staff:manage")
      ? ["Every permission, and creating and deleting staff accounts"]
      : ["Every permission except managing staff"]
  }
  const labels = new Map(areas.flatMap((a) => a.permissions.map((p) => [p.key, p.label] as const)))
  return role.permissions.map((p) => labels.get(p) ?? p)
}

export default function StaffRolesPage() {
  const confirm = useConfirm()
  const { data: me } = useCurrentUser()
  const { data, isLoading, isError, error, refetch } = useStaffRoles()
  const save = useSaveStaffRole()
  const remove = useDeleteStaffRole()
  const [form, setForm] = useState<StaffRoleInput | null>(null)

  const toggle = (key: string) =>
    setForm((f) => f && {
      ...f,
      permissions: f.permissions.includes(key) ? f.permissions.filter((p) => p !== key) : [...f.permissions, key],
    })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!form) return
    try {
      await save.mutateAsync(form)
      toast.success(form.id ? "Role saved" : "Role created")
      setForm(null)
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  const handleDelete = async (role: StaffRole) => {
    const ok = await confirm({
      title: `Delete the role "${role.name}"?`,
      description: "It can be deleted only while nobody holds it.",
      confirmLabel: "Delete role",
      destructive: true,
      action: () => remove.mutateAsync(role.id),
    })
    if (ok) toast.success("Role deleted")
  }

  return (
    <div className="max-w-5xl mx-auto space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Staff roles</h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
            What each staff account may do. The founding administrator is always the Owner. Staff accounts from before
            roles existed are Administrators. A role is changed on the staff member&apos;s user page.
          </p>
        </div>
        {data?.can_manage && (
          <Button onClick={() => setForm({ ...empty })}>New role</Button>
        )}
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading roles…</p>
      ) : isError ? (
        <div className="rounded-lg border bg-card p-5 flex flex-wrap items-center gap-3 text-sm">
          <span className="text-destructive">Roles could not be loaded: {(error as Error).message}</span>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : (
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm divide-y">
          {data!.roles.map((role) => (
            <div key={role.id} className="p-5 flex flex-wrap items-start justify-between gap-4">
              <div className="min-w-0 flex-1">
                <p className="font-semibold">
                  {role.name}
                  <span className="ml-2 text-xs font-normal text-muted-foreground">
                    {role.built_in ? "Built in" : "Custom"} · {role.members} {role.members === 1 ? "account" : "accounts"}
                  </span>
                </p>
                {role.description && <p className="text-sm text-muted-foreground mt-1">{role.description}</p>}
                <details className="mt-2">
                  <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
                    What it allows
                  </summary>
                  <ul className="mt-2 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1 text-sm">
                    {holds(role, data!.areas).map((l) => <li key={l}>{l}</li>)}
                    {holds(role, data!.areas).length === 0 && <li className="text-muted-foreground">Only the dashboard</li>}
                  </ul>
                </details>
              </div>
              {data!.can_manage && !role.built_in && (
                <div className="flex gap-2">
                  <Button variant="outline" size="sm" onClick={() => setForm({ id: role.id, name: role.name, description: role.description, permissions: role.permissions })}>
                    Edit
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => handleDelete(role)} disabled={role.members > 0} title={role.members > 0 ? "Give its staff another role first" : undefined}>
                    Delete
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      <Dialog open={!!form} onOpenChange={(open) => !open && setForm(null)}>
        <DialogContent className="max-w-3xl max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{form?.id ? `Edit ${form.name}` : "New role"}</DialogTitle>
            <DialogDescription>
              Tick what the role may do. You can only give permissions you hold yourself.
            </DialogDescription>
          </DialogHeader>
          {form && data && (
            <form id="role-form" onSubmit={submit} className="space-y-5">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>
                  <label htmlFor="role-name" className="block text-sm font-medium text-muted-foreground mb-1">Name</label>
                  <Input id="role-name" value={form.name} maxLength={100} required
                    onChange={(e) => setForm({ ...form, name: e.target.value })} />
                </div>
                <div>
                  <label htmlFor="role-description" className="block text-sm font-medium text-muted-foreground mb-1">Description (optional)</label>
                  <Input id="role-description" value={form.description} maxLength={500}
                    onChange={(e) => setForm({ ...form, description: e.target.value })} />
                </div>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
                {data.areas.map((area) => (
                  <fieldset key={area.area} className="rounded-md border p-3">
                    <legend className="px-1 text-sm font-semibold">{area.area}</legend>
                    <div className="space-y-2">
                      {area.permissions.map((p) => {
                        const held = can(me, p.key)
                        return (
                          <label key={p.key} className={`flex items-start gap-2 text-sm ${held ? "cursor-pointer" : "text-muted-foreground"}`}>
                            <input type="checkbox" className="mt-0.5 w-4 h-4 accent-primary"
                              checked={form.permissions.includes(p.key)} disabled={!held} onChange={() => toggle(p.key)} />
                            <span>
                              {p.label}
                              {!held && <span className="block text-xs">You do not hold this</span>}
                            </span>
                          </label>
                        )
                      })}
                    </div>
                  </fieldset>
                ))}
              </div>
            </form>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setForm(null)}>Cancel</Button>
            <Button type="submit" form="role-form" disabled={save.isPending || !form?.name.trim()}>
              {save.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}{form?.id ? "Save role" : "Create role"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
