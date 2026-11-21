"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ListPager } from "@/components/list-pager"
import { useCan } from "@/lib/hooks/use-auth"
import { useDecideIPRequest, useIPRequestQueue } from "@/lib/hooks/use-portal"

const PAGE = 25
const STATUSES = [
  { value: "pending", label: "Waiting" },
  { value: "granted", label: "Granted" },
  { value: "rejected", label: "Declined" },
  { value: "", label: "All" },
]

// Customers' requests for another address, oldest waiting first. Granting
// assigns one from a pool on the VM's node, as Assign IP address does.
export default function IPRequestsPage() {
  const can = useCan()
  const [status, setStatus] = useState("pending")
  const [page, setPage] = useState(1)
  const { data, isLoading, isError, refetch } = useIPRequestQueue(status, page, PAGE)
  const decide = useDecideIPRequest()
  const [declining, setDeclining] = useState<string | null>(null)
  const [note, setNote] = useState("")
  const mayDecide = can("network:update")

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Address requests</h1>
        <p className="text-sm text-muted-foreground">
          Customers asking for another IPv4 address or an IPv6 prefix on a VM. Whether they wait for you or get one at once is set under Settings, Customer area.
        </p>
      </div>

      <div role="group" aria-label="Show" className="flex flex-wrap gap-1.5">
        {STATUSES.map((s) => (
          <Button key={s.value || "all"} size="sm" variant={status === s.value ? "default" : "outline"} aria-pressed={status === s.value}
            onClick={() => { setStatus(s.value); setPage(1) }}>{s.label}</Button>
        ))}
      </div>

      <section className="rounded-lg border bg-card text-card-foreground">
        {isLoading ? (
          <p className="p-5 text-sm text-muted-foreground">Loading…</p>
        ) : isError ? (
          <div className="p-5">
            <p role="alert" className="text-sm text-destructive">The requests could not be loaded.</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !data?.data.length ? (
          <p className="p-5 text-sm text-muted-foreground">{status === "pending" ? "Nobody is waiting." : "No requests."}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-xs text-muted-foreground">
                  <th className="p-3 font-medium">VM</th>
                  <th className="p-3 font-medium">Owner</th>
                  <th className="p-3 font-medium">Kind</th>
                  <th className="p-3 font-medium">Reason</th>
                  <th className="p-3 font-medium">Asked</th>
                  <th className="p-3 font-medium">Outcome</th>
                  <th className="p-3" />
                </tr>
              </thead>
              <tbody>
                {data.data.map((r) => (
                  <tr key={r.id} className="border-b border-hair align-top last:border-0">
                    <td className="p-3"><Link href={`/vms/${r.vm_id}`} className="font-medium hover:underline">{r.hostname || r.vm_id}</Link></td>
                    <td className="p-3">{r.email}</td>
                    <td className="p-3">{r.family === "ipv6" ? "IPv6" : "IPv4"}</td>
                    <td className="max-w-xs p-3 text-muted-foreground">{r.reason || "None given"}</td>
                    <td className="p-3 tabular-nums text-muted-foreground">{new Date(r.created_at).toLocaleString()}</td>
                    <td className="p-3">
                      {r.status === "granted" ? <span className="font-mono">{r.address}</span> : r.status === "rejected" ? "Declined" : r.status === "canceled" ? "Withdrawn" : r.note ? <span className="text-muted-foreground">{r.note}</span> : "Waiting"}
                    </td>
                    <td className="p-3">
                      {r.status === "pending" && mayDecide && (
                        declining === r.id ? (
                          <div className="flex flex-wrap items-center gap-2">
                            <label htmlFor={`note-${r.id}`} className="sr-only">Why it is declined</label>
                            <Input id={`note-${r.id}`} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Told to the customer" className="h-9 w-56" maxLength={500} />
                            <Button size="sm" variant="destructive" disabled={decide.isPending} onClick={() => decide.mutate({ id: r.id, approve: false, note }, {
                              onSuccess: () => { setDeclining(null); setNote(""); toast.success("Request declined") },
                              onError: (e) => toast.error(e.message),
                            })}>Decline</Button>
                            <Button size="sm" variant="ghost" onClick={() => setDeclining(null)}>Cancel</Button>
                          </div>
                        ) : (
                          <div className="flex gap-2">
                            <Button size="sm" disabled={decide.isPending} onClick={() => decide.mutate({ id: r.id, approve: true }, {
                              onSuccess: (res) => res.warning ? toast.warning(`Assigned ${res.data.address}, but: ${res.warning}`) : toast.success(`Assigned ${res.data.address}`),
                              onError: (e) => toast.error(e.message),
                            })}>
                              {decide.isPending && decide.variables?.id === r.id && decide.variables.approve && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                              Grant
                            </Button>
                            <Button size="sm" variant="outline" onClick={() => { setDeclining(r.id); setNote("") }}>Decline</Button>
                          </div>
                        )
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <ListPager page={page} pageSize={PAGE} total={data?.total ?? 0} onPage={setPage} noun="requests" />
    </div>
  )
}
