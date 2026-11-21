"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Meter } from "@/components/ui/meter"
import { Sparkline } from "@/components/ui/sparkline"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { type CapacitySeries, downloadCSV, useCapacityReport, useUsageReport } from "@/lib/hooks/use-admin-extras"

// Capacity over time, for deciding when to buy the next node, and what each
// customer holds and moved, for a conversation with them.

const selectClass =
  "h-9 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"

const gb = (mb: number) => `${Number((mb / 1024).toFixed(1))} GB`
const tb = (g: number) => (g >= 1024 ? `${Number((g / 1024).toFixed(1))} TB` : `${g} GB`)
const bytes = (n: number) => {
  const units = ["B", "KB", "MB", "GB", "TB", "PB"]
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${Number(n.toFixed(1))} ${units[i]}`
}

function Share({ label, used, total, text }: { label: string; used: number; total: number; text: string }) {
  return (
    <div className="min-w-0">
      <div className="flex justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular-nums">{text}</span>
      </div>
      {total > 0 ? <Meter percent={(used / total) * 100} over={used > total} /> : <p className="text-xs text-muted-foreground">Not measured</p>}
    </div>
  )
}

function Strip({ label, values, top }: { label: string; values: number[]; top: string }) {
  return (
    <div className="min-w-0">
      <div className="flex justify-between gap-2 text-xs text-muted-foreground">
        <span>{label}</span>
        <span>top {top}</span>
      </div>
      <Sparkline data={values} max={Math.max(...values, 1)} height={28} colorClass="text-[color:var(--chart-1)]" />
    </div>
  )
}

function SeriesRow({ s, scope }: { s: CapacitySeries; scope: string }) {
  const last = s.points[s.points.length - 1]
  const pool = scope === "pool"
  return (
    <li className="px-5 py-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <p className="font-semibold [font-stretch:112%]">{s.name}</p>
        <p className="text-xs text-muted-foreground">
          {last.day}{!pool && `, ${last.vms} ${last.vms === 1 ? "VM" : "VMs"}`}
        </p>
      </div>
      <div className={`mt-3 grid gap-4 ${pool ? "sm:grid-cols-2" : "sm:grid-cols-3"}`}>
        {!pool && (
          <>
            <Share label="vCPU allocated" used={last.vcpu_allocated} total={last.vcpu_total}
              text={`${last.vcpu_allocated} of ${last.vcpu_total || "?"}${last.vcpu_total ? ` (${Number((last.vcpu_allocated / last.vcpu_total).toFixed(2))}x)` : ""}`} />
            <Share label="Memory allocated" used={last.ram_allocated_mb} total={last.ram_total_mb}
              text={`${gb(last.ram_allocated_mb)} of ${last.ram_total_mb ? gb(last.ram_total_mb) : "?"}`} />
          </>
        )}
        <Share label="Disk allocated" used={last.disk_allocated_gb} total={last.disk_total_gb}
          text={`${tb(last.disk_allocated_gb)} of ${last.disk_total_gb ? tb(last.disk_total_gb) : "?"}, ${tb(last.disk_used_gb)} used`} />
        {pool && <Share label="Disk used" used={last.disk_used_gb} total={last.disk_total_gb} text={`${tb(last.disk_used_gb)}`} />}
      </div>
      {s.points.length > 1 && (
        <div className={`mt-3 grid gap-4 ${pool ? "sm:grid-cols-2" : "sm:grid-cols-3"}`}>
          {!pool && <Strip label="Memory allocated" values={s.points.map((p) => p.ram_allocated_mb)} top={gb(Math.max(...s.points.map((p) => p.ram_allocated_mb)))} />}
          {!pool && <Strip label="Average memory use, %" values={s.points.map((p) => p.memory_usage)} top={`${Math.round(Math.max(...s.points.map((p) => p.memory_usage)))}%`} />}
          <Strip label="Disk used" values={s.points.map((p) => p.disk_used_gb)} top={tb(Math.max(...s.points.map((p) => p.disk_used_gb)))} />
          {pool && <Strip label="Disk allocated" values={s.points.map((p) => p.disk_allocated_gb)} top={tb(Math.max(...s.points.map((p) => p.disk_allocated_gb)))} />}
        </div>
      )}
      <details className="mt-3 text-sm">
        <summary className="cursor-pointer text-xs font-medium text-muted-foreground">Day by day</summary>
        <div className="mt-2 max-h-64 overflow-auto rounded-md border">
          <table className="w-full text-xs tabular-nums">
            <thead className="sticky top-0 bg-muted text-muted-foreground">
              <tr>
                <th className="p-2 text-left font-medium">Day</th>
                {!pool && <th className="p-2 text-right font-medium">VMs</th>}
                {!pool && <th className="p-2 text-right font-medium">vCPU</th>}
                {!pool && <th className="p-2 text-right font-medium">Memory</th>}
                <th className="p-2 text-right font-medium">Disk allocated</th>
                <th className="p-2 text-right font-medium">Disk used</th>
                {!pool && <th className="p-2 text-right font-medium">CPU use</th>}
              </tr>
            </thead>
            <tbody>
              {[...s.points].reverse().map((p) => (
                <tr key={p.day} className="border-t">
                  <td className="p-2">{p.day}</td>
                  {!pool && <td className="p-2 text-right">{p.vms}</td>}
                  {!pool && <td className="p-2 text-right">{p.vcpu_allocated}/{p.vcpu_total}</td>}
                  {!pool && <td className="p-2 text-right">{gb(p.ram_allocated_mb)}/{gb(p.ram_total_mb)}</td>}
                  <td className="p-2 text-right">{tb(p.disk_allocated_gb)}/{tb(p.disk_total_gb)}</td>
                  <td className="p-2 text-right">{tb(p.disk_used_gb)}</td>
                  {!pool && <td className="p-2 text-right">{Math.round(p.cpu_usage)}%</td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </li>
  )
}

function Capacity() {
  const [scope, setScope] = useState("node")
  const [days, setDays] = useState(90)
  const { data, isLoading, error, refetch } = useCapacityReport(scope, days)
  const [busy, setBusy] = useState(false)
  const csv = async () => {
    setBusy(true)
    try {
      await downloadCSV("/api/v1/reports/capacity", { scope, days })
    } catch (err) {
      toast.error(`The file could not be made: ${(err as Error).message}`)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section aria-label="Capacity">
      <p className="mb-3 max-w-prose text-sm text-muted-foreground">
        One reading a day: what was promised to VMs against what there is, and the day&apos;s average use. The panel records it every
        hour and keeps two years. Regions add up their nodes as they are assigned now.
      </p>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <label htmlFor="cap-scope" className="sr-only">By</label>
        <select id="cap-scope" className={selectClass} value={scope} onChange={(e) => setScope(e.target.value)}>
          <option value="node">By node</option>
          <option value="pool">By storage pool</option>
          <option value="region">By region</option>
        </select>
        <label htmlFor="cap-days" className="sr-only">Period</label>
        <select id="cap-days" className={selectClass} value={days} onChange={(e) => setDays(Number(e.target.value))}>
          <option value={30}>Last 30 days</option>
          <option value={90}>Last 90 days</option>
          <option value={365}>Last year</option>
          <option value={730}>Last two years</option>
        </select>
        <Button size="sm" variant="outline" disabled={busy} onClick={csv}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}Download CSV
        </Button>
      </div>
      <div className="rounded-lg border bg-card">
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading capacity</p>
        ) : error ? (
          <div className="p-5" role="alert">
            <p className="text-sm font-medium">The report could not be loaded: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : !data?.series.length ? (
          <p className="p-5 text-sm text-muted-foreground">Nothing recorded yet. The first reading is taken a minute after the panel starts, then every hour.</p>
        ) : (
          <ul className="divide-y">
            {data.series.filter((s) => s.points.length > 0).map((s) => <SeriesRow key={s.id || s.name} s={s} scope={scope} />)}
          </ul>
        )}
      </div>
    </section>
  )
}

function Usage() {
  const [month, setMonth] = useState(new Date().toISOString().slice(0, 7))
  const { data, isLoading, error, refetch } = useUsageReport(month)
  const [busy, setBusy] = useState(false)
  const rows = data?.customers ?? []
  const sum = (f: (r: (typeof rows)[number]) => number) => rows.reduce((n, r) => n + f(r), 0)
  const csv = async () => {
    setBusy(true)
    try {
      await downloadCSV("/api/v1/reports/usage", { month })
    } catch (err) {
      toast.error(`The file could not be made: ${(err as Error).message}`)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section aria-label="Customer usage">
      <p className="mb-3 max-w-prose text-sm text-muted-foreground">
        Each customer with VMs or transfer in the month: what they hold now, and the month&apos;s transfer (in and out), largest first.
      </p>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <label htmlFor="usage-month" className="text-sm">Month</label>
        <Input id="usage-month" type="month" className="h-9 w-44" value={month} onChange={(e) => e.target.value && setMonth(e.target.value)} />
        <Button size="sm" variant="outline" disabled={busy} onClick={csv}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}Download CSV
        </Button>
      </div>
      <div className="overflow-x-auto rounded-lg border bg-card">
        {isLoading ? (
          <p className="flex items-center gap-2 p-5 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading usage</p>
        ) : error ? (
          <div className="p-5" role="alert">
            <p className="text-sm font-medium">The report could not be loaded: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : rows.length === 0 ? (
          <p className="p-5 text-sm text-muted-foreground">No customer had VMs or transfer that month.</p>
        ) : (
          <table className="w-full text-sm tabular-nums">
            <thead className="bg-muted text-xs text-muted-foreground">
              <tr>
                <th className="p-3 text-left font-medium">Customer</th>
                <th className="p-3 text-right font-medium">VMs</th>
                <th className="p-3 text-right font-medium">vCPU</th>
                <th className="p-3 text-right font-medium">Memory</th>
                <th className="p-3 text-right font-medium">Disk</th>
                <th className="p-3 text-right font-medium">Transfer</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.user_id} className="border-t">
                  <td className="p-3">
                    <Link href={`/users/${r.user_id}`} className="break-all underline-offset-4 hover:underline">{r.email}</Link>
                    {r.name && <span className="block text-xs text-muted-foreground">{r.name}</span>}
                  </td>
                  <td className="p-3 text-right">{r.vms}</td>
                  <td className="p-3 text-right">{r.vcpu}</td>
                  <td className="p-3 text-right">{gb(r.ram_mb)}</td>
                  <td className="p-3 text-right">{tb(r.disk_gb)}</td>
                  <td className="p-3 text-right">{bytes(r.transfer_bytes)}</td>
                </tr>
              ))}
              <tr className="border-t bg-muted/50 font-medium">
                <td className="p-3">{rows.length} customers</td>
                <td className="p-3 text-right">{sum((r) => r.vms)}</td>
                <td className="p-3 text-right">{sum((r) => r.vcpu)}</td>
                <td className="p-3 text-right">{gb(sum((r) => r.ram_mb))}</td>
                <td className="p-3 text-right">{tb(sum((r) => r.disk_gb))}</td>
                <td className="p-3 text-right">{bytes(sum((r) => r.transfer_bytes))}</td>
              </tr>
            </tbody>
          </table>
        )}
      </div>
    </section>
  )
}

export default function ReportsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <h1 className="text-2xl font-semibold">Reports</h1>
      <p className="mt-1 mb-6 max-w-2xl text-sm text-muted-foreground">Capacity over time and what each customer uses, each as a CSV for a spreadsheet.</p>
      <Tabs defaultValue="capacity" className="space-y-4">
        <TabsList>
          <TabsTrigger value="capacity">Capacity</TabsTrigger>
          <TabsTrigger value="usage">Customer usage</TabsTrigger>
        </TabsList>
        <TabsContent value="capacity"><Capacity /></TabsContent>
        <TabsContent value="usage"><Usage /></TabsContent>
      </Tabs>
    </div>
  )
}
