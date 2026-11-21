"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { usePortal, useCancelIPRequest, useRequestIP, useVMIPRequests } from "@/lib/hooks/use-portal"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useMemberships, useProjects, useSetVMProject } from "@/lib/hooks/use-team"
import type { IPRequest } from "@/types/team"

const statusWords: Record<IPRequest["status"], string> = {
  pending: "Waiting for staff",
  granting: "Being assigned",
  granted: "Granted",
  rejected: "Declined",
  canceled: "Withdrawn",
}

// A customer asking for another address on their server: granted at once or
// by staff, as the operator chose, and only within the plan's allowance.
export function AddressRequestsCard({ vmId, hasIPv6, disabled }: { vmId: string; hasIPv6: boolean; disabled?: boolean }) {
  const { data: portal } = usePortal()
  const off = portal?.ip_requests === "off"
  const { data: requests } = useVMIPRequests(vmId, !!portal && !off)
  const request = useRequestIP(vmId)
  const cancel = useCancelIPRequest(vmId)
  const [family, setFamily] = useState<"ipv4" | "ipv6">("ipv4")
  const [reason, setReason] = useState("")
  if (!portal || off) return null
  const auto = portal.ip_requests === "auto"

  return (
    <section className="rounded-lg border bg-card p-6 text-card-foreground">
      <h2 className="text-lg font-semibold">More addresses</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {auto ? "Ask for another address and it is assigned straight away, within what your plan includes." : "Ask for another address. Staff look at each request; you are notified of the answer."}
        {" "}The server is not restarted; a guest without its agent running may need the new address set by hand.
      </p>
      <div className="mt-4 flex flex-wrap items-end gap-3">
        <div>
          <Label htmlFor="ipr-family">Kind</Label>
          <select id="ipr-family" value={family} onChange={(e) => setFamily(e.target.value as "ipv4" | "ipv6")}
            className="block h-10 rounded-md border border-input bg-background px-3 text-sm">
            <option value="ipv4">IPv4 address</option>
            <option value="ipv6" disabled={hasIPv6}>IPv6{hasIPv6 ? " (already has one)" : ""}</option>
          </select>
        </div>
        <div className="min-w-0 flex-1 basis-64">
          <Label htmlFor="ipr-reason">What it is for{auto ? " (optional)" : ""}</Label>
          <Input id="ipr-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} placeholder="A second website with its own certificate" />
        </div>
        <Button disabled={disabled || request.isPending} onClick={async () => {
          try {
            const res = await request.mutateAsync({ family, reason })
            setReason("")
            if (res.data.status === "granted") toast.success(`Assigned ${res.data.address}`)
            else if (res.warning) toast.warning(`It could not be assigned at once, so staff will look at it: ${res.warning}`)
            else toast.success("Request sent to staff")
          } catch (e) {
            toast.error((e as Error).message)
          }
        }}>
          {request.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          Request address
        </Button>
      </div>
      {!!requests?.length && (
        <ul className="mt-5 divide-y rounded-md border">
          {requests.map((r) => (
            <li key={r.id} className="flex flex-wrap items-baseline gap-x-4 gap-y-1 px-3 py-2 text-sm">
              <span className="font-medium">{r.family === "ipv6" ? "IPv6" : "IPv4"}</span>
              <span>{statusWords[r.status]}</span>
              {r.address && <span className="font-mono">{r.address}</span>}
              {r.note && <span className="text-muted-foreground">{r.note}</span>}
              <time className="ml-auto text-xs text-muted-foreground tabular-nums" dateTime={r.created_at}>{new Date(r.created_at).toLocaleDateString()}</time>
              {r.status === "pending" && (
                <Button size="sm" variant="ghost" disabled={cancel.isPending} onClick={() => cancel.mutate(r.id, { onError: (e) => toast.error(e.message) })}>Withdraw</Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// Which project of its account a server is in.
export function VMProjectCard({ vm, disabled }: { vm: { id: string; user_id: string; project_id?: string }; disabled?: boolean }) {
  const { data: me } = useCurrentUser()
  const { data: teams } = useMemberships()
  const own = me?.id === vm.user_id
  const member = teams?.memberships.find((m) => m.account_id === vm.user_id)
  const { data: projects } = useProjects(own ? undefined : vm.user_id, own || !!member)
  const set = useSetVMProject(vm.id)
  if (!projects?.length) return null
  const mayChange = own || !!member?.can.includes("operate")
  return (
    <section className="rounded-lg border bg-card p-6 text-card-foreground">
      <h2 className="text-base font-semibold">Project</h2>
      <p className="mt-1 text-sm text-muted-foreground">Groups the server inside {own ? "your" : "its"} account. Team members limited to a project reach the servers in it.</p>
      <div className="mt-3 max-w-sm">
        <Label htmlFor="vm-project" className="sr-only">Project</Label>
        <select id="vm-project" value={vm.project_id ?? ""} disabled={disabled || !mayChange || set.isPending}
          onChange={(e) => set.mutate(e.target.value, {
            onSuccess: () => toast.success(e.target.value ? "Moved into the project" : "Taken out of its project"),
            onError: (err) => toast.error(err.message),
          })}
          className="h-10 w-full rounded-md border border-input bg-background px-3 text-sm">
          <option value="">No project</option>
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
      </div>
    </section>
  )
}
