"use client"

import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useSetMemberOnly, useUserTeam } from "@/lib/hooks/use-team"
import { memberRoles } from "@/types/team"
import { reachLabel } from "@/lib/team"

const role = (r: string) => memberRoles.find((x) => x.value === r)?.label ?? r

// A customer's team as staff see it: who is let into the account, the teams
// they work in, and whether an account made by an invitation may order.
export function UserTeamCard({ userId, canUpdate }: { userId: string; canUpdate: boolean }) {
  const { data } = useUserTeam(userId)
  const setMemberOnly = useSetMemberOnly(userId)
  if (!data || (!data.members.length && !data.memberships.length && !data.projects.length && !data.member_only)) return null

  return (
    <section className="rounded-lg border bg-card p-6 text-card-foreground">
      <h2 className="text-base font-semibold">Team</h2>
      {data.member_only && (
        <div className="mt-3 flex flex-wrap items-center gap-3 rounded-md border p-3 text-sm">
          <span className="min-w-0 flex-1">Made by accepting a team invitation: it cannot order servers or floating IPs of its own.</span>
          {canUpdate && (
            <Button size="sm" variant="outline" disabled={setMemberOnly.isPending} onClick={() => setMemberOnly.mutate(false, {
              onSuccess: () => toast.success("The account may now order on its own"),
              onError: (e) => toast.error(e.message),
            })}>Let it order</Button>
          )}
        </div>
      )}
      {data.members.length > 0 && (
        <div className="mt-4">
          <h3 className="text-sm font-medium">Members of this account</h3>
          <ul className="mt-2 divide-y rounded-md border text-sm">
            {data.members.map((m) => (
              <li key={m.id} className="flex flex-wrap gap-x-4 px-3 py-2">
                <span className="font-medium">{m.email}</span>
                <span>{role(m.role)}</span>
                <span className="text-muted-foreground">{reachLabel(m, data.projects)}</span>
                <span className="ml-auto text-muted-foreground">{m.accepted_at ? "Member" : "Invited"}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {data.memberships.length > 0 && (
        <div className="mt-4">
          <h3 className="text-sm font-medium">Works in</h3>
          <ul className="mt-2 divide-y rounded-md border text-sm">
            {data.memberships.map((m) => (
              <li key={m.id} className="flex flex-wrap gap-x-4 px-3 py-2">
                <span className="font-medium">{m.account_name || m.account_email}</span>
                <span>{role(m.role)}</span>
                <span className="text-muted-foreground">{reachLabel(m, [])}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {data.projects.length > 0 && (
        <p className="mt-4 text-sm text-muted-foreground">Projects: {data.projects.map((p) => `${p.name} (${p.vm_count})`).join(", ")}</p>
      )}
    </section>
  )
}
