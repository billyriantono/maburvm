"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useConfirm } from "@/components/confirm-provider"
import { describeRule } from "@/lib/firewall"
import { useAttachFirewallGroup, useDetachFirewallGroup, useFirewallGroups, useVMFirewallGroups } from "@/lib/hooks/use-firewall-groups"
import type { FirewallGroupPosition } from "@/types"

// VMFirewallGroups shows the firewall groups on a VM and attaches or detaches
// them. A customer works with their own groups; a group for everyone is placed
// by staff or the VM's plan and stays.
export function VMFirewallGroups({ vmId, ownerId, isAdmin }: { vmId: string; ownerId: string; isAdmin: boolean }) {
  const confirm = useConfirm()
  const { data: attached, isLoading, error, refetch } = useVMFirewallGroups(vmId)
  const { data: groups } = useFirewallGroups()
  const attach = useAttachFirewallGroup(vmId)
  const detach = useDetachFirewallGroup(vmId)
  const [groupId, setGroupId] = useState("")
  const [position, setPosition] = useState<FirewallGroupPosition>("after")

  const choices = (groups ?? []).filter(
    (g) => !attached?.some((a) => a.id === g.id) && (isAdmin ? g.user_id === null || g.user_id === ownerId : true),
  )
  const groupsHref = isAdmin ? "/firewall-groups" : "/client/firewall-groups"

  return (
    <div className="mb-6 rounded-md border p-4 space-y-3">
      <div>
        <h3 className="text-sm font-semibold">Firewall groups</h3>
        <p className="text-xs text-muted-foreground mt-1">
          Rule sets shared between VMs. A group placed before this VM&apos;s rules cannot be overridden by them; one placed after
          is overridden by them. Group rules count toward the VM&apos;s rule limit. <Link href={groupsHref} className="underline underline-offset-2">Manage groups</Link>
        </p>
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : error ? (
        <div>
          <p role="alert" className="text-sm text-destructive">{(error as Error).message}</p>
          <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : !attached?.length ? (
        <p className="text-sm text-muted-foreground">No groups on this VM.</p>
      ) : (
        <ul className="divide-y border rounded-md">
          {attached.map((g) => (
            <li key={g.id} className="p-3 flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <span className="font-medium text-sm">{g.name}</span>
                <span className="text-xs text-muted-foreground">
                  {" · "}{g.global ? "for everyone" : isAdmin ? "the owner's group" : "your group"}
                  {" · "}{g.position === "before" ? "before this VM's rules" : "after this VM's rules"}
                </span>
                {g.rules.length === 0 ? (
                  <p className="text-xs text-muted-foreground mt-1">No rules yet.</p>
                ) : (
                  <ul className="text-xs text-muted-foreground mt-1 space-y-0.5">
                    {g.rules.map((r) => <li key={r.id}>{describeRule(r)}</li>)}
                  </ul>
                )}
              </div>
              {(isAdmin || !g.global) && (
                <Button size="sm" variant="outline" onClick={() => confirm({
                  title: `Detach "${g.name}"?`,
                  description: "Its rules stop applying to this VM as soon as the node is updated. Traffic they allowed may then be dropped by the VM's default.",
                  confirmLabel: "Detach",
                  destructive: true,
                  action: async () => { await detach.mutateAsync(g.id); toast.success(`"${g.name}" detached`) },
                })}>Detach</Button>
              )}
            </li>
          ))}
        </ul>
      )}

      {choices.length > 0 && (
        <div className="flex flex-wrap items-end gap-2">
          <div>
            <label htmlFor="vm-group" className="text-xs font-medium text-muted-foreground block mb-1">Group</label>
            <select id="vm-group" value={groupId} onChange={(e) => setGroupId(e.target.value)} className="h-9 border rounded-md bg-background px-2 text-sm">
              <option value="">Choose a group</option>
              {choices.map((g) => <option key={g.id} value={g.id}>{g.name} ({g.rule_count} rules)</option>)}
            </select>
          </div>
          <div>
            <label htmlFor="vm-group-pos" className="text-xs font-medium text-muted-foreground block mb-1">Place it</label>
            <select id="vm-group-pos" value={position} onChange={(e) => setPosition(e.target.value as FirewallGroupPosition)} className="h-9 border rounded-md bg-background px-2 text-sm">
              <option value="after">After this VM&apos;s rules</option>
              <option value="before">Before this VM&apos;s rules</option>
            </select>
          </div>
          <Button size="sm" disabled={!groupId || attach.isPending} onClick={async () => {
            try {
              await attach.mutateAsync({ group_id: groupId, position })
              toast.success("Group attached")
              setGroupId("")
            } catch (e) { toast.error((e as Error).message) }
          }}>
            {attach.isPending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}Attach group
          </Button>
        </div>
      )}
    </div>
  )
}
