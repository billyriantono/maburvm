"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { EmailLog } from "@/components/email-log"
import { useCan } from "@/lib/hooks/use-auth"
import { type RequestFilter, useIPAllocations, useRequestLog } from "@/lib/hooks/use-admin-extras"
import { cn } from "@/lib/utils"

// One place to answer "what happened": the API requests the panel answered,
// who held which address when, and what was emailed to customers.

const selectClass =
  "h-9 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" })

/** Waits until typing pauses before a filter applies. */
function useSettled(value: string, ms = 300) {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

function Pager({ page, pageSize, total, onPage }: { page: number; pageSize: number; total: number; onPage: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  if (pages <= 1) return null
  return (
    <nav aria-label="Pages" className="mt-3 flex flex-wrap items-center justify-between gap-3 text-sm">
      <p className="text-muted-foreground">Page {page} of {pages}, {total.toLocaleString()} in all</p>
      <div className="flex gap-2">
        <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => onPage(page - 1)}>Previous</Button>
        <Button size="sm" variant="outline" disabled={page >= pages} onClick={() => onPage(page + 1)}>Next</Button>
      </div>
    </nav>
  )
}

function statusTone(code: number) {
  if (code >= 500) return "text-destructive font-semibold"
  if (code >= 400) return "text-warning font-semibold"
  return ""
}

function Requests() {
  const [typed, setTyped] = useState("")
  const q = useSettled(typed)
  const [f, setF] = useState<Omit<RequestFilter, "q">>({ method: "", status: "", min_ms: "", page: 1 })
  const { data, isLoading, error, refetch, isPlaceholderData } = useRequestLog({ ...f, q })
  const set = (patch: Partial<RequestFilter>) => setF((x) => ({ ...x, page: 1, ...patch }))

  return (
    <section aria-label="API requests">
      <p className="mb-3 max-w-prose text-sm text-muted-foreground">
        The newest {data ? data.kept.toLocaleString() : "10,000"} requests to the API and the billing webhook, kept in the panel&apos;s memory:
        a restart empties this list. Changes are also in the <Link href="/audit-logs" className="underline underline-offset-4">audit log</Link>, which keeps them.
        Query strings are never recorded.
      </p>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <label htmlFor="req-q" className="sr-only">Search path, account, key, address or request id</label>
        <Input id="req-q" type="search" className="h-9 w-72" placeholder="Path, account, key, address, request id" value={typed}
          onChange={(e) => { setTyped(e.target.value); set({}) }} />
        <label htmlFor="req-method" className="sr-only">Method</label>
        <select id="req-method" className={selectClass} value={f.method} onChange={(e) => set({ method: e.target.value })}>
          <option value="">Every method</option>
          {["GET", "POST", "PUT", "PATCH", "DELETE"].map((m) => <option key={m} value={m}>{m}</option>)}
        </select>
        <label htmlFor="req-status" className="sr-only">Answer</label>
        <select id="req-status" className={selectClass} value={f.status} onChange={(e) => set({ status: e.target.value })}>
          <option value="">Every answer</option>
          <option value="2xx">Succeeded (2xx)</option>
          <option value="4xx">Refused (4xx)</option>
          <option value="5xx">Failed (5xx)</option>
        </select>
        <label htmlFor="req-slow" className="sr-only">Slower than</label>
        <select id="req-slow" className={selectClass} value={f.min_ms} onChange={(e) => set({ min_ms: e.target.value })}>
          <option value="">Any time taken</option>
          <option value="500">Slower than 0.5 s</option>
          <option value="2000">Slower than 2 s</option>
          <option value="10000">Slower than 10 s</option>
        </select>
      </div>
      <div className={cn("overflow-x-auto rounded-lg border bg-card", isPlaceholderData && "opacity-60")}>
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading requests</p>
        ) : error && !data ? (
          <div className="p-5" role="alert">
            <p className="text-sm font-medium">The requests could not be loaded: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !data?.requests.length ? (
          <p className="p-5 text-sm text-muted-foreground">No request matches.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-muted text-xs text-muted-foreground">
              <tr>
                <th className="p-3 text-left font-medium">When</th>
                <th className="p-3 text-left font-medium">Request</th>
                <th className="p-3 text-left font-medium">Answer</th>
                <th className="p-3 text-right font-medium">Took</th>
                <th className="p-3 text-left font-medium">Who</th>
                <th className="p-3 text-left font-medium">From</th>
              </tr>
            </thead>
            <tbody>
              {data.requests.map((r, i) => (
                <tr key={`${r.time}-${i}`} className="border-t align-top">
                  <td className="whitespace-nowrap p-3 text-muted-foreground tabular-nums">{when(r.time)}</td>
                  <td className="p-3"><span className="font-medium">{r.method}</span> <span className="break-all font-mono text-xs">{r.path}</span></td>
                  <td className={cn("p-3 tabular-nums", statusTone(r.status))}>{r.status}</td>
                  <td className="whitespace-nowrap p-3 text-right tabular-nums">{r.latency_ms < 1000 ? `${Math.round(r.latency_ms)} ms` : `${(r.latency_ms / 1000).toFixed(1)} s`}</td>
                  <td className="p-3">
                    {r.email ? (
                      r.user_id ? <Link href={`/users/${r.user_id}`} className="break-all underline-offset-4 hover:underline">{r.email}</Link> : r.email
                    ) : <span className="text-muted-foreground">Not signed in</span>}
                    {r.api_key_id && <span className="block font-mono text-xs text-muted-foreground">key {r.api_key_id.slice(0, 8)}</span>}
                  </td>
                  <td className="p-3 font-mono text-xs">{r.ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {data && <Pager page={f.page} pageSize={data.page_size} total={data.total} onPage={(page) => setF((x) => ({ ...x, page }))} />}
    </section>
  )
}

function Addresses() {
  const [typed, setTyped] = useState("")
  const q = useSettled(typed)
  const [state, setState] = useState("")
  const [page, setPage] = useState(1)
  const { data, isLoading, error, refetch, isPlaceholderData } = useIPAllocations({ q, state, page })

  return (
    <section aria-label="Address assignments">
      <p className="mb-3 max-w-prose text-sm text-muted-foreground">
        Every time an address went to a VM and came back, newest change first. For an abuse report about a moment in time, the
        address lookup on the Abuse page answers who held it then.
      </p>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <label htmlFor="ip-q" className="sr-only">Search address, VM or owner</label>
        <Input id="ip-q" type="search" className="h-9 w-72" placeholder="Address, VM name or owner's email" value={typed}
          onChange={(e) => { setTyped(e.target.value); setPage(1) }} />
        <label htmlFor="ip-state" className="sr-only">State</label>
        <select id="ip-state" className={selectClass} value={state} onChange={(e) => { setState(e.target.value); setPage(1) }}>
          <option value="">Held and released</option>
          <option value="held">Held now</option>
          <option value="released">Released</option>
        </select>
      </div>
      <div className={cn("overflow-x-auto rounded-lg border bg-card", isPlaceholderData && "opacity-60")}>
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading assignments</p>
        ) : error && !data ? (
          <div className="p-5" role="alert">
            <p className="text-sm font-medium">The assignments could not be loaded: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !data?.events.length ? (
          <p className="p-5 text-sm text-muted-foreground">No assignment matches.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-muted text-xs text-muted-foreground">
              <tr>
                <th className="p-3 text-left font-medium">Address</th>
                <th className="p-3 text-left font-medium">VM and owner</th>
                <th className="p-3 text-left font-medium">Node</th>
                <th className="p-3 text-left font-medium">Assigned</th>
                <th className="p-3 text-left font-medium">Released</th>
              </tr>
            </thead>
            <tbody>
              {data.events.map((ev) => (
                <tr key={ev.id} className="border-t align-top">
                  <td className="p-3 font-mono">{ev.address}{ev.kind !== "direct" && <span className="block font-sans text-xs text-muted-foreground">{ev.kind}</span>}</td>
                  <td className="p-3">
                    {ev.vm_id ? <Link href={`/vms/${ev.vm_id}`} className="font-medium underline-offset-4 hover:underline">{ev.vm_hostname || ev.vm_id}</Link> : ev.vm_hostname}
                    <span className="block text-xs text-muted-foreground">{ev.user_email || "No owner recorded"}</span>
                  </td>
                  <td className="p-3">{ev.node_name || <span className="text-muted-foreground">Unknown</span>}</td>
                  <td className="whitespace-nowrap p-3 tabular-nums">{when(ev.assigned_at)}{ev.estimated && <span className="block text-xs text-muted-foreground">at the latest; held before recording began</span>}</td>
                  <td className="whitespace-nowrap p-3 tabular-nums">{ev.released_at ? when(ev.released_at) : <span className="font-medium">Held now</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {data && <Pager page={page} pageSize={data.page_size} total={data.total} onPage={setPage} />}
    </section>
  )
}

export default function LogsPage() {
  const allow = useCan()
  const tabs = [
    { id: "requests", label: "API requests", show: allow("audit:read") },
    { id: "addresses", label: "Addresses", show: allow("network:read") },
    { id: "emails", label: "Emails", show: allow("settings:read") },
  ].filter((t) => t.show)
  const [tab, setTab] = useState("")
  const current = tab || tabs[0]?.id || ""

  return (
    <div className="mx-auto max-w-7xl">
      <h1 className="text-2xl font-semibold">Logs</h1>
      <p className="mt-1 mb-6 max-w-2xl text-sm text-muted-foreground">
        What the panel was asked, which address went where, and what customers were emailed.
      </p>
      {tabs.length === 0 ? (
        <p className="text-sm text-muted-foreground">Your role does not include any of the logs.</p>
      ) : (
        <Tabs value={current} onValueChange={setTab} className="space-y-4">
          <TabsList>
            {tabs.map((t) => <TabsTrigger key={t.id} value={t.id}>{t.label}</TabsTrigger>)}
          </TabsList>
          {allow("audit:read") && <TabsContent value="requests"><Requests /></TabsContent>}
          {allow("network:read") && <TabsContent value="addresses"><Addresses /></TabsContent>}
          {allow("settings:read") && <TabsContent value="emails"><EmailLog /></TabsContent>}
        </Tabs>
      )}
    </div>
  )
}
