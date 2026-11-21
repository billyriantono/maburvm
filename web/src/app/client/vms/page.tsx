"use client"

import Link from "next/link"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Monitor, PlusCircle, Play, Square, RotateCw, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusBadge, useVMTransitions, vmStatus } from "@/components/status-badge"
import { useVMs, useVMStatusStream, useVMActions } from "@/lib/hooks/use-vms"
import { CountryFlag } from "@/components/country-flag"
import { ListPager } from "@/components/list-pager"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useMemberships, useProjects } from "@/lib/hooks/use-team"
import type { VM } from "@/types"

const PAGE = 25

export default function ClientVMsPage() {
  useVMStatusStream()
  const [tag, setTag] = useState("")
  const [page, setPage] = useState(1)
  // Whose servers: everything the caller can see, or one account's (their
  // own, or a team they work in), narrowed to one of its projects.
  const [accountId, setAccountId] = useState("")
  const [projectId, setProjectId] = useState("")
  const { data: me } = useCurrentUser()
  const { data: teams } = useMemberships()
  const memberships = teams?.memberships ?? []
  const { data: projects } = useProjects(accountId && accountId !== me?.id ? accountId : undefined, !!accountId)
  const accountName = (id: string) => {
    const m = memberships.find((x) => x.account_id === id)
    return m ? m.account_name || m.account_email : ""
  }
  const { data, isLoading, error, refetch } = useVMs({ page, pageSize: PAGE, tag, accountId, projectId })
  const action = useVMActions()
  const vms: VM[] = data?.data ?? []
  // The tags in use, remembered while one is filtered on so the choice stays.
  const inUse = useMemo(() => [...new Set((data?.data ?? []).flatMap((v) => v.tags ?? []))].sort(), [data])
  const [known, setKnown] = useState<string[]>([])
  useEffect(() => {
    if (!tag && data) setKnown(inUse)
  }, [tag, data, inUse])
  const { transitionOf, begin } = useVMTransitions(vms)
  // Which VM is waiting on which request, so only that row is disabled.
  const [inFlight, setInFlight] = useState<Record<string, string>>({})

  const busy = (status: string) => status === "creating" || status === "deleting"

  const run = (vm: VM, act: string, label: string) => {
    setInFlight((p) => ({ ...p, [vm.id]: act }))
    const done = () =>
      setInFlight((p) => {
        const n = { ...p }
        delete n[vm.id]
        return n
      })
    action.mutate(
      { vmId: vm.id, action: act },
      {
        onSuccess: () => {
          done()
          begin(vm.id, act, vm.status)
          toast.success(`${label} requested for ${vm.hostname}`)
        },
        onError: (err) => {
          done()
          toast.error(err.message || `Could not complete the request for ${vm.hostname}`)
        },
      }
    )
  }

  const powerButton = ({ vm, act, label, doing, icon: Icon, variant, className }: {
    vm: VM; act: string; label: string; doing: string; icon: React.ElementType
    variant: "outline" | "success"; className?: string
  }) => {
    const mine = inFlight[vm.id] === act
    return (
      <Button
        key={act}
        size="sm"
        variant={variant}
        className={className}
        onClick={() => run(vm, act, label)}
        disabled={!!inFlight[vm.id] || !!transitionOf(vm.id) || busy(vm.status)}
      >
        {mine ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Icon aria-hidden="true" />}
        {mine ? doing : label}
      </Button>
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">My VMs</h1>
          <p className="text-sm text-muted-foreground mt-1">Manage your virtual machines</p>
        </div>
        <Link
          href="/client/order"
          className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors"
        >
          <PlusCircle className="w-4 h-4" /> Order VM
        </Link>
      </div>

      <div className="flex flex-wrap items-center gap-4">
        {memberships.length > 0 && (
          <div className="flex items-center gap-2">
            <label htmlFor="vm-account" className="text-sm text-muted-foreground">Account</label>
            <select id="vm-account" value={accountId} onChange={(e) => { setAccountId(e.target.value); setProjectId(""); setPage(1) }}
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <option value="">Every account</option>
              <option value={me?.id ?? ""}>Your own</option>
              {memberships.map((m) => <option key={m.account_id} value={m.account_id}>{m.account_name || m.account_email}</option>)}
            </select>
          </div>
        )}
        {!!accountId && !!projects?.length && (
          <div className="flex items-center gap-2">
            <label htmlFor="vm-project" className="text-sm text-muted-foreground">Project</label>
            <select id="vm-project" value={projectId} onChange={(e) => { setProjectId(e.target.value); setPage(1) }}
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <option value="">Every project</option>
              {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </div>
        )}
        {known.length > 0 && (
          <div className="flex items-center gap-2">
            <label htmlFor="vm-tag" className="text-sm text-muted-foreground">Tag</label>
            <select id="vm-tag" value={tag} onChange={(e) => { setTag(e.target.value); setPage(1) }}
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <option value="">Every server</option>
              {known.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          </div>
        )}
      </div>

      <div className="rounded-lg border bg-card text-card-foreground shadow-sm">
        {isLoading ? (
          <div className="p-8 text-center text-muted-foreground">Loading…</div>
        ) : error && !data ? (
          <div className="p-10 text-center">
            <p className="font-medium text-muted-foreground">Could not load your VMs.</p>
            <p className="text-sm text-muted-foreground mt-1">{(error as Error).message}</p>
            <Button className="mt-4" variant="outline" onClick={() => refetch()}>Retry</Button>
          </div>
        ) : vms.length === 0 && tag ? (
          <div className="p-10 text-center">
            <p className="font-medium text-muted-foreground">No server has the tag {tag}.</p>
            <Button className="mt-4" variant="outline" onClick={() => setTag("")}>Show every server</Button>
          </div>
        ) : vms.length === 0 ? (
          <div className="p-10 text-center">
            <p className="font-medium text-muted-foreground">You don&apos;t have any VMs yet.</p>
            <Link href="/client/order" className="inline-flex items-center gap-2 mt-4 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors">
              <PlusCircle className="w-4 h-4" /> Order your first VM
            </Link>
          </div>
        ) : (
          <ul className="divide-y">
            {vms.map((vm) => (
              <li key={vm.id} className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 px-5 py-4 hover:bg-muted/50 transition-colors">
                <Link href={`/client/vms/${vm.id}`} className="flex items-center gap-3 min-w-0 flex-1">
                  <Monitor className="w-5 h-5 shrink-0 text-muted-foreground" />
                  <div className="min-w-0">
                    <p className="font-medium truncate">{vm.hostname}</p>
                    {me && vm.user_id !== me.id && accountName(vm.user_id) && (
                      <p className="text-xs text-muted-foreground truncate">In {accountName(vm.user_id)}&apos;s account</p>
                    )}
                    {vm.tags && vm.tags.length > 0 && (
                      <span className="mt-0.5 flex flex-wrap gap-1">
                        {vm.tags.map((t) => (
                          <span key={t} className="rounded border px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">{t}</span>
                        ))}
                      </span>
                    )}
                    <p className="text-xs text-muted-foreground flex items-center gap-1.5">
                      {vm.region_name && (
                        <>
                          <CountryFlag country={vm.region_country} />
                          {vm.region_name} ·{" "}
                        </>
                      )}
                      {vm.resources.cpu} vCPU · {vm.resources.ram} MB RAM · {vm.resources.disk} GB disk
                    </p>
                  </div>
                </Link>
                <div className="flex items-center gap-2">
                  <StatusBadge status={vmStatus(vm)} transition={transitionOf(vm.id)} />
                  {vm.status === "stopped" && !vm.state_unknown &&
                    powerButton({ vm, act: "start", label: "Start", doing: "Starting", icon: Play, variant: "success" })}
                  {vm.status === "running" && !vm.state_unknown && (
                    <>
                      {powerButton({ vm, act: "restart", label: "Reboot", doing: "Rebooting", icon: RotateCw, variant: "outline" })}
                      {powerButton({ vm, act: "stop", label: "Stop", doing: "Stopping", icon: Square, variant: "outline", className: "text-destructive hover:bg-destructive/10 hover:text-destructive" })}
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
      <ListPager page={page} pageSize={PAGE} total={data?.total ?? 0} onPage={setPage} noun="servers" />
    </div>
  )
}
