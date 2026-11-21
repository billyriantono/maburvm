"use client"

import { useMyQuota } from "@/lib/hooks/use-quota"
import { useMemo, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useConfirm } from "@/components/confirm-provider"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useVMs } from "@/lib/hooks/use-vms"
import {
  useDeleteProject,
  useInviteMember,
  useLeaveAccount,
  useMembers,
  useMemberships,
  useProjects,
  useRemoveMember,
  useSaveProject,
  useUpdateMember,
} from "@/lib/hooks/use-team"
import { memberRoles, type AccountMember, type MemberRole, type Project } from "@/types/team"
import { reachLabel } from "@/lib/team"

const field = "h-10 rounded-md border border-input bg-background px-3 text-sm"
type Reach = "all" | "vms" | "projects"

function roleLabel(role: MemberRole) {
  return memberRoles.find((r) => r.value === role)?.label ?? role
}

// Who a member reaches, and with what role: the form shared by inviting and
// changing a member.
function MemberForm({
  listAccountId,
  initial,
  projects,
  submitLabel,
  pending,
  withEmail,
  onSubmit,
  onCancel,
}: {
  /** The account whose servers are offered: the caller's own id, or a team's. */
  listAccountId?: string
  initial?: AccountMember
  projects: Project[]
  submitLabel: string
  pending: boolean
  withEmail: boolean
  onSubmit: (v: { email: string; role: MemberRole; vm_ids: string[]; project_ids: string[] }) => void
  onCancel?: () => void
}) {
  const [email, setEmail] = useState("")
  const [role, setRole] = useState<MemberRole>(initial?.role ?? "technical")
  const [reach, setReach] = useState<Reach>(initial?.vm_ids.length ? "vms" : initial?.project_ids.length ? "projects" : "all")
  const [vmIds, setVmIds] = useState<string[]>(initial?.vm_ids ?? [])
  const [projectIds, setProjectIds] = useState<string[]>(initial?.project_ids ?? [])
  const { data: vms } = useVMs({ pageSize: 100, accountId: listAccountId })
  const toggle = (list: string[], id: string) => (list.includes(id) ? list.filter((x) => x !== id) : [...list, id])
  const chosen = reach === "vms" ? vmIds.length : reach === "projects" ? projectIds.length : 1
  const prefix = initial ? `m-${initial.id}` : "invite"

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        {withEmail && (
          <div className="min-w-0 flex-1 basis-64">
            <Label htmlFor={`${prefix}-email`}>Email address</Label>
            <Input id={`${prefix}-email`} type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="colleague@example.com" autoComplete="off" />
          </div>
        )}
        <div>
          <Label htmlFor={`${prefix}-role`}>Role</Label>
          <select id={`${prefix}-role`} value={role} onChange={(e) => setRole(e.target.value as MemberRole)} className={`${field} block`}>
            {memberRoles.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
          </select>
        </div>
        <div>
          <Label htmlFor={`${prefix}-reach`}>Servers</Label>
          <select id={`${prefix}-reach`} value={reach} onChange={(e) => setReach(e.target.value as Reach)} className={`${field} block`}>
            <option value="all">Every server</option>
            <option value="vms">Chosen servers</option>
            <option value="projects" disabled={!projects.length}>Chosen projects</option>
          </select>
        </div>
      </div>
      <p className="text-sm text-muted-foreground">{memberRoles.find((r) => r.value === role)?.detail}</p>

      {reach === "vms" && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">Servers they reach</legend>
          {!vms?.data?.length ? (
            <p className="text-sm text-muted-foreground">This account has no servers yet.</p>
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              {vms.data.map((vm) => (
                <label key={vm.id} className="flex min-h-10 items-center gap-2 rounded-md border px-3 py-2 text-sm">
                  <input type="checkbox" checked={vmIds.includes(vm.id)} onChange={() => setVmIds((l) => toggle(l, vm.id))} />
                  <span className="truncate font-medium">{vm.hostname}</span>
                </label>
              ))}
            </div>
          )}
        </fieldset>
      )}
      {reach === "projects" && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">Projects they reach</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {projects.map((p) => (
              <label key={p.id} className="flex min-h-10 items-center gap-2 rounded-md border px-3 py-2 text-sm">
                <input type="checkbox" checked={projectIds.includes(p.id)} onChange={() => setProjectIds((l) => toggle(l, p.id))} />
                <span className="truncate font-medium">{p.name}</span>
                <span className="ml-auto text-xs text-muted-foreground tabular-nums">{p.vm_count} {p.vm_count === 1 ? "server" : "servers"}</span>
              </label>
            ))}
          </div>
        </fieldset>
      )}

      <div className="flex gap-2">
        <Button
          disabled={pending || (withEmail && !email.trim()) || chosen === 0}
          onClick={() => onSubmit({
            email: email.trim(),
            role,
            vm_ids: reach === "vms" ? vmIds : [],
            project_ids: reach === "projects" ? projectIds : [],
          })}
        >
          {pending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          {submitLabel}
        </Button>
        {onCancel && <Button variant="ghost" onClick={onCancel}>Cancel</Button>}
      </div>
    </div>
  )
}

function ProjectsPanel({ accountId, projects, canManage }: { accountId?: string; projects: Project[]; canManage: boolean }) {
  const confirm = useConfirm()
  const save = useSaveProject()
  const remove = useDeleteProject()
  const [name, setName] = useState("")
  const [editing, setEditing] = useState<string | null>(null)
  const [editName, setEditName] = useState("")

  return (
    <section className="rounded-lg border bg-card text-card-foreground">
      <div className="border-b px-5 py-4">
        <h2 className="text-lg font-semibold">Projects</h2>
        <p className="text-sm text-muted-foreground">Group servers, and give members a project instead of a list of servers. Move a server into a project from its page.</p>
      </div>
      <div className="space-y-3 p-5">
        {projects.length === 0 && <p className="text-sm text-muted-foreground">No projects yet.</p>}
        {projects.map((p) => (
          <div key={p.id} className="flex flex-wrap items-center gap-3 rounded-md border px-3 py-2">
            {editing === p.id ? (
              <>
                <Label htmlFor={`p-${p.id}`} className="sr-only">Project name</Label>
                <Input id={`p-${p.id}`} value={editName} onChange={(e) => setEditName(e.target.value)} className="w-56" />
                <Button size="sm" disabled={save.isPending || !editName.trim()} onClick={async () => {
                  try { await save.mutateAsync({ id: p.id, account_id: accountId, name: editName, description: p.description }); setEditing(null); toast.success("Project renamed") } catch (e) { toast.error((e as Error).message) }
                }}>Save</Button>
                <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
              </>
            ) : (
              <>
                <span className="font-medium">{p.name}</span>
                <span className="text-sm text-muted-foreground tabular-nums">{p.vm_count} {p.vm_count === 1 ? "server" : "servers"}</span>
                {canManage && (
                  <span className="ml-auto flex gap-2">
                    <Button size="sm" variant="outline" onClick={() => { setEditing(p.id); setEditName(p.name) }}>Rename</Button>
                    <Button size="sm" variant="outline" onClick={() => confirm({
                      title: `Delete the project "${p.name}"?`,
                      description: "Its servers stay, in no project. A member who works only in this project loses access.",
                      confirmLabel: "Delete project",
                      destructive: true,
                      action: async () => { await remove.mutateAsync({ id: p.id, accountId }); toast.success("Project deleted") },
                    })}>Delete</Button>
                  </span>
                )}
              </>
            )}
          </div>
        ))}
        {canManage && (
          <div className="flex flex-wrap items-end gap-2 pt-2">
            <div>
              <Label htmlFor="new-project">New project</Label>
              <Input id="new-project" value={name} onChange={(e) => setName(e.target.value)} placeholder="Shop" className="w-56" />
            </div>
            <Button variant="outline" disabled={save.isPending || !name.trim()} onClick={async () => {
              try { await save.mutateAsync({ account_id: accountId, name, description: "" }); setName(""); toast.success("Project added") } catch (e) { toast.error((e as Error).message) }
            }}>Add project</Button>
          </div>
        )}
      </div>
    </section>
  )
}

// AccountUsage is what a team account holds against its limits, for members
// whose role covers its usage and billing.
function AccountUsage({ accountId }: { accountId: string }) {
  const { data, error } = useMyQuota(accountId)
  if (error) return <span className="text-sm text-muted-foreground">Usage could not be loaded</span>
  if (!data) return null
  const of = (used: number, max: number, unit = "") => `${used}${unit}${max ? ` of ${max}${unit}` : ""}`
  return (
    <span className="text-sm text-muted-foreground">
      {of(data.usage.vms, data.quota.max_vms)} servers, {of(data.usage.vcpu, data.quota.max_vcpu)} vCPU,{" "}
      {of(Math.round(data.usage.ram_mb / 1024), Math.round(data.quota.max_ram_mb / 1024), " GB")} RAM, {of(data.usage.disk_gb, data.quota.max_disk_gb, " GB")} disk
    </span>
  )
}

export default function ClientTeamPage() {
  const confirm = useConfirm()
  const { data: user } = useCurrentUser()
  const { data: mine } = useMemberships()
  // The account whose team is shown: the caller's own, or one they are an admin of.
  const managed = useMemo(() => (mine?.memberships ?? []).filter((m) => m.can.includes("manage")), [mine])
  const [accountId, setAccountId] = useState<string>("")
  const viewing = accountId || undefined
  const { data: members, isLoading } = useMembers(viewing)
  const { data: projects } = useProjects(viewing)
  const invite = useInviteMember()
  const update = useUpdateMember()
  const remove = useRemoveMember()
  const leave = useLeaveAccount()
  const [editing, setEditing] = useState<string | null>(null)
  const projectList = projects ?? []

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Team</h1>
        <p className="text-sm text-muted-foreground">
          Let colleagues into this account with their own sign-in and two-factor. The servers, quota and billing stay the account&apos;s.
        </p>
      </div>

      {managed.length > 0 && (
        <div className="max-w-sm">
          <Label htmlFor="team-account">Account</Label>
          <select id="team-account" value={accountId} onChange={(e) => { setAccountId(e.target.value); setEditing(null) }} className={`${field} block w-full`}>
            <option value="">Your account ({user?.email})</option>
            {managed.map((m) => <option key={m.account_id} value={m.account_id}>{m.account_name || m.account_email}</option>)}
          </select>
        </div>
      )}

      {mine?.member_only && !viewing && (
        <p className="rounded-md border p-4 text-sm text-muted-foreground">
          This account was made when you accepted an invitation, so it holds no servers of its own. Work in the team&apos;s account from My VMs.
        </p>
      )}

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4">
          <h2 className="text-lg font-semibold">Invite someone</h2>
          <p className="text-sm text-muted-foreground">They get an email with a link that works for 7 days. Someone without an account chooses a password when they accept.</p>
        </div>
        <div className="p-5">
          <MemberForm
            key={viewing ?? "own"}
            listAccountId={viewing ?? user?.id}
            projects={projectList}
            withEmail
            submitLabel="Send invitation"
            pending={invite.isPending}
            onSubmit={async (v) => {
              try {
                const res = await invite.mutateAsync({ ...v, account_id: viewing })
                if (res.warning) toast.warning(res.warning)
                else toast.success(`Invitation sent to ${v.email}`)
              } catch (e) {
                toast.error((e as Error).message)
              }
            }}
          />
        </div>
      </section>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4">
          <h2 className="text-lg font-semibold">Members</h2>
        </div>
        <div className="divide-y">
          {isLoading ? (
            <p className="p-5 text-sm text-muted-foreground">Loading…</p>
          ) : !members?.length ? (
            <p className="p-5 text-sm text-muted-foreground">Nobody else works in this account yet.</p>
          ) : (
            members.map((m) => (
              <div key={m.id} className="space-y-3 px-5 py-4">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
                  <div className="min-w-0">
                    <p className="truncate font-medium">{m.name || m.email}</p>
                    {m.name && <p className="truncate text-sm text-muted-foreground">{m.email}</p>}
                  </div>
                  <span className="text-sm">{roleLabel(m.role)}</span>
                  <span className="text-sm text-muted-foreground">{reachLabel(m, projectList)}</span>
                  <span className="text-sm text-muted-foreground">
                    {m.accepted_at ? "Member" : m.invite_expires_at && new Date(m.invite_expires_at) < new Date() ? "Invitation expired" : "Invited, not accepted yet"}
                  </span>
                  <span className="ml-auto flex gap-2">
                    <Button size="sm" variant="outline" onClick={() => setEditing(editing === m.id ? null : m.id)}>Change</Button>
                    <Button size="sm" variant="outline" onClick={() => confirm({
                      title: m.accepted_at ? `Remove ${m.email}?` : `Revoke the invitation to ${m.email}?`,
                      description: m.accepted_at ? "They lose access to this account's servers at once. Their own account stays." : "The link in the email stops working.",
                      confirmLabel: m.accepted_at ? "Remove member" : "Revoke invitation",
                      destructive: true,
                      action: async () => { await remove.mutateAsync({ id: m.id, accountId: viewing }); toast.success(m.accepted_at ? "Member removed" : "Invitation revoked") },
                    })}>{m.accepted_at ? "Remove" : "Revoke"}</Button>
                  </span>
                </div>
                {editing === m.id && (
                  <div className="rounded-md border p-4">
                    <MemberForm
                      listAccountId={viewing ?? user?.id}
                      initial={m}
                      projects={projectList}
                      withEmail={false}
                      submitLabel="Save"
                      pending={update.isPending}
                      onCancel={() => setEditing(null)}
                      onSubmit={async (v) => {
                        try {
                          await update.mutateAsync({ id: m.id, account_id: viewing, role: v.role, vm_ids: v.vm_ids, project_ids: v.project_ids })
                          setEditing(null)
                          toast.success("Member changed")
                        } catch (e) {
                          toast.error((e as Error).message)
                        }
                      }}
                    />
                  </div>
                )}
              </div>
            ))
          )}
        </div>
      </section>

      <ProjectsPanel accountId={viewing} projects={projectList} canManage />

      {!!mine?.memberships.length && (
        <section className="rounded-lg border bg-card text-card-foreground">
          <div className="border-b px-5 py-4">
            <h2 className="text-lg font-semibold">Accounts you work in</h2>
            <p className="text-sm text-muted-foreground">Their servers are in My VMs, next to your own.</p>
          </div>
          <div className="divide-y">
            {mine.memberships.map((m) => (
              <div key={m.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-4">
                <div className="min-w-0">
                  <p className="truncate font-medium">{m.account_name || m.account_email}</p>
                  {m.account_name && <p className="truncate text-sm text-muted-foreground">{m.account_email}</p>}
                </div>
                <span className="text-sm">{roleLabel(m.role)}</span>
                <span className="text-sm text-muted-foreground">{m.role === "billing" ? "Usage and billing" : reachLabel(m, [])}</span>
                {m.can.includes("billing") && <AccountUsage accountId={m.account_id} />}
                <Button size="sm" variant="outline" className="ml-auto" onClick={() => confirm({
                  title: `Leave ${m.account_name || m.account_email}?`,
                  description: "You lose access to its servers. Someone there has to invite you again to come back.",
                  confirmLabel: "Leave account",
                  destructive: true,
                  action: async () => { await leave.mutateAsync(m.id); toast.success("You left the account") },
                })}>Leave</Button>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}
