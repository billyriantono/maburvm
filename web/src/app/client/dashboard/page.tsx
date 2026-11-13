"use client"

import Link from "next/link"
import { useMyDashboard, type MyVM } from "@/lib/hooks/use-dashboard"
import { useVMStatusStream } from "@/lib/hooks/use-vms"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Sparkline } from "@/components/ui/sparkline"
import { StatusBadge } from "@/components/status-badge"
import { CountryFlag } from "@/components/country-flag"
import { useCopy } from "@/lib/use-feedback"

// The customer's home page answers two questions for every server they rent:
// is it healthy, and is it about to hit a limit. One row per server, and a
// short list on top of anything that needs them to act.

// The panel counts transfer in binary gigabytes, like the limit it is compared with.
const GB = 1024 ** 3

function ago(iso: string): string {
  const min = Math.floor((Date.now() - new Date(iso).getTime()) / 60_000)
  if (min < 60) return min <= 1 ? "just now" : `${min} minutes ago`
  const h = Math.floor(min / 60)
  if (h < 48) return h === 1 ? "1 hour ago" : `${h} hours ago`
  return `${Math.floor(h / 24)} days ago`
}

function transferShare(vm: MyVM): number | null {
  if (vm.transfer_quota_gb <= 0 || vm.transfer_bytes == null) return null
  return (vm.transfer_bytes / (vm.transfer_quota_gb * GB)) * 100
}

/** Things the customer should act on, most serious first. */
function attention(vms: MyVM[]): { key: string; bad: boolean; text: string; href: string }[] {
  const rows: { key: string; bad: boolean; text: string; href: string }[] = []
  for (const vm of vms) {
    const href = `/client/vms/${vm.id}`
    if (vm.suspended || vm.status === "suspended")
      rows.push({ key: `s-${vm.id}`, bad: true, text: `${vm.hostname} is suspended. Contact support to restore it.`, href })
    else if (vm.status === "error")
      rows.push({ key: `e-${vm.id}`, bad: true, text: `${vm.hostname} failed and is not running. Contact support.`, href })
    const share = transferShare(vm)
    if (vm.transfer_exceeded)
      rows.push({ key: `x-${vm.id}`, bad: true, text: `${vm.hostname} is over its monthly transfer limit.`, href: `${href}?tab=network` })
    else if (share !== null && share >= 80)
      rows.push({ key: `t-${vm.id}`, bad: false, text: `${vm.hostname} has used ${Math.round(share)}% of this month's transfer.`, href: `${href}?tab=network` })
    if (vm.last_backup_status === "failed" && vm.last_backup_at)
      rows.push({ key: `b-${vm.id}`, bad: false, text: `The last backup of ${vm.hostname} failed ${ago(vm.last_backup_at)}.`, href: `${href}?tab=backups` })
  }
  return rows.sort((a, b) => Number(b.bad) - Number(a.bad))
}

function Meter({ percent, over }: { percent: number; over?: boolean }) {
  return (
    <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
      <div className={`h-full ${over ? "bg-destructive" : "bg-foreground"}`} style={{ width: `${Math.min(Math.max(percent, 0), 100)}%` }} />
    </div>
  )
}

function ServerRow({ vm }: { vm: MyVM }) {
  const { copy, copied } = useCopy()
  const running = vm.status === "running" && !vm.suspended
  const cpuNow = vm.cpu_series.length ? vm.cpu_series[vm.cpu_series.length - 1] : null
  const share = transferShare(vm)
  const usedGB = vm.transfer_bytes == null ? null : vm.transfer_bytes / GB
  return (
    <li className="grid grid-cols-2 gap-x-6 gap-y-4 px-5 py-4 lg:grid-cols-[minmax(0,1.5fr)_minmax(0,1.1fr)_minmax(0,1fr)_minmax(0,1.1fr)_minmax(0,1fr)] lg:items-center">
      <div className="col-span-2 min-w-0 lg:col-span-1">
        <Link href={`/client/vms/${vm.id}`} className="break-all text-base font-semibold underline-offset-4 hover:underline [font-stretch:112%]">
          {vm.hostname}
        </Link>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
          <StatusBadge variant="lamp" className="text-xs" status={vm.suspended ? "suspended" : vm.status} />
          {vm.region_name && (
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              {vm.region_country && <CountryFlag country={vm.region_country} />}
              {vm.region_name}
            </span>
          )}
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {vm.cpu} vCPU, {Number((vm.ram_mb / 1024).toFixed(1))} GB memory, {vm.disk_gb} GB disk
        </p>
      </div>

      <div className="col-span-2 min-w-0 lg:col-span-1">
        <p className="text-xs text-muted-foreground">Address</p>
        {vm.ip ? (
          <p className="text-sm font-medium">
            <span className="font-mono">{vm.ip}</span>
            <button
              type="button"
              onClick={() => copy(vm.ip!, vm.id)}
              aria-label={copied(vm.id) ? "Copied" : `Copy address ${vm.ip}`}
              className="ml-2 border-b border-border pb-px text-xs font-semibold hover:border-foreground active:translate-y-px"
            >
              {copied(vm.id) ? "Copied" : "Copy"}
            </button>
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">None assigned</p>
        )}
      </div>

      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">CPU, last hour</p>
        {running && cpuNow !== null ? (
          <div className="flex items-center gap-3" title={`CPU over the last hour, now ${Math.round(cpuNow)}%`}>
            <span className="w-10 text-sm font-semibold">{Math.round(cpuNow)}%</span>
            <Sparkline data={vm.cpu_series} height={28} className="min-w-0 flex-1" colorClass="text-[color:var(--chart-1)]" />
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{running ? "No reading yet" : "Off"}</p>
        )}
      </div>

      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">Transfer this month</p>
        {usedGB === null ? (
          <p className="text-sm text-muted-foreground">Nothing recorded</p>
        ) : (
          <>
            <p className="text-sm">
              <span className="font-semibold">{usedGB.toFixed(usedGB < 10 ? 1 : 0)} GB</span>
              {vm.transfer_quota_gb > 0 ? ` of ${vm.transfer_quota_gb.toLocaleString()} GB` : ", no limit"}
            </p>
            {share !== null && <Meter percent={share} over={!!vm.transfer_exceeded} />}
          </>
        )}
      </div>

      <div className="col-span-2 min-w-0 lg:col-span-1">
        <p className="text-xs text-muted-foreground">Last backup</p>
        {vm.last_backup_at ? (
          <p className={`text-sm font-medium ${vm.last_backup_status === "failed" ? "text-destructive" : ""}`} title={new Date(vm.last_backup_at).toLocaleString()}>
            {vm.last_backup_status === "failed" ? "Failed " : vm.last_backup_status === "completed" ? "" : "Started "}
            {ago(vm.last_backup_at)}
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">None yet</p>
        )}
      </div>
    </li>
  )
}

export default function ClientDashboardPage() {
  useVMStatusStream()
  const { data, isLoading, error, refetch } = useMyDashboard()
  const vms = data?.vms ?? []
  const failed = !!error && !data
  const needs = attention(vms)
  const running = vms.filter((v) => v.status === "running" && !v.suspended).length

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Your servers</h1>
          {!isLoading && !failed && vms.length > 0 && (
            <p className="mt-1 text-sm text-muted-foreground">
              {vms.length === 1 ? "1 server" : `${vms.length} servers`}, {running} running
              {needs.length > 0 ? `, ${needs.length === 1 ? "1 thing needs" : `${needs.length} things need`} your attention` : ", nothing needs your attention"}.
            </p>
          )}
        </div>
        {vms.length > 0 && (
          <Button asChild variant="outline">
            <Link href="/client/order">Order a server</Link>
          </Button>
        )}
      </div>

      {needs.length > 0 && (
        <section aria-labelledby="needs-attention" className="rounded-lg border bg-card text-card-foreground">
          <h2 id="needs-attention" className="border-b border-hair px-5 py-3 text-sm font-semibold">
            Needs your attention
          </h2>
          <ul className="divide-y divide-hair">
            {needs.map((n) => (
              <li key={n.key} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3 text-sm">
                <span className={`h-2.5 w-2.5 shrink-0 rounded-full ${n.bad ? "bg-destructive" : "bg-warning"}`} aria-hidden="true" />
                <span className="min-w-0 flex-1 basis-56">{n.text}</span>
                <Link href={n.href} className="font-semibold underline underline-offset-4">
                  Open
                </Link>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section aria-label="Servers" className="rounded-lg border bg-card text-card-foreground">
        {isLoading ? (
          <div className="space-y-4 p-5" aria-busy="true" aria-label="Loading your servers">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-16 w-full" />
            ))}
          </div>
        ) : failed ? (
          <div className="p-8" role="alert">
            <p className="font-medium">Could not load your servers.</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error).message}</p>
            <Button className="mt-4" variant="outline" onClick={() => refetch()}>
              Try again
            </Button>
          </div>
        ) : vms.length === 0 ? (
          <div className="p-8">
            <p className="font-medium">You have no servers yet.</p>
            <p className="mt-1 text-sm text-muted-foreground">Order one and it appears here with its address, usage and backups.</p>
            <Button asChild className="mt-4">
              <Link href="/client/order">Order your first server</Link>
            </Button>
          </div>
        ) : (
          <ul className="divide-y divide-hair">
            {vms.map((vm) => (
              <ServerRow key={vm.id} vm={vm} />
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
