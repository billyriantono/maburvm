"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Meter } from "@/components/ui/meter"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { DeleteProgressDialog } from "@/components/vm-delete-progress"
import { StatusBadge, useVMTransitions } from "@/components/status-badge"
import { OSLogoTile } from "@/components/os-icon"
import { useConfirm } from "@/components/confirm-provider"
import { useCopy } from "@/lib/use-feedback"
import { useFleetVMs, type FleetVM } from "@/lib/hooks/use-dashboard"
import { useDeleteVM, useVMActions, useVMStatusStream } from "@/lib/hooks/use-vms"
import { useNodes } from "@/lib/hooks/use-nodes"

// The VM list is for finding a machine and acting on it. It can be searched
// by what an operator actually has in hand (a name, a customer's email, an
// address from a ticket), and each row says who owns the VM, where it runs,
// how to reach it and what it is doing now. Searching, filtering, ordering
// and paging are all done by the server, so a VM is found wherever it sits.

const PAGE_SIZE = 25

const STATES: { id: string; label: string }[] = [
  { id: "running", label: "Running" },
  { id: "stopped", label: "Stopped" },
  { id: "suspended", label: "Suspended" },
  { id: "error", label: "Failed" },
  { id: "creating", label: "Creating" },
  { id: "deleting", label: "Deleting" },
]

const SORTS: { id: string; label: string }[] = [
  { id: "name", label: "Name" },
  { id: "cpu", label: "Busiest CPU first" },
  { id: "memory", label: "Most memory first" },
  { id: "newest", label: "Newest first" },
  { id: "oldest", label: "Oldest first" },
  { id: "owner", label: "Owner" },
  { id: "node", label: "Node" },
  { id: "address", label: "Address" },
]

const selectClass =
  "h-10 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"

type Power = "start" | "stop" | "restart"

const ASK: Record<"stop" | "restart", (name: string) => { title: string; description: string; confirmLabel: string }> = {
  restart: (name) => ({ title: `Restart ${name}?`, description: "It shuts down cleanly, then boots again. Expect about a minute offline.", confirmLabel: "Restart" }),
  stop: (name) => ({ title: `Stop ${name}?`, description: "It shuts down cleanly and stays off until you start it. Disks and addresses are kept.", confirmLabel: "Stop" }),
}

export default function VMsListPage() {
  const confirm = useConfirm()
  const { copy, copied } = useCopy()
  const [typed, setTyped] = useState("")
  const [search, setSearch] = useState("")
  const [status, setStatus] = useState("")
  const [node, setNode] = useState("")
  const [sort, setSort] = useState("name")
  const [page, setPage] = useState(1)
  const [busy, setBusy] = useState<Record<string, Power>>({})
  const [deletingVM, setDeletingVM] = useState<{ id: string; hostname: string } | null>(null)

  // Search as they type, but not on every keystroke.
  useEffect(() => {
    const t = setTimeout(() => {
      setSearch(typed.trim())
      setPage(1)
    }, 300)
    return () => clearTimeout(t)
  }, [typed])

  useVMStatusStream()
  const { data, isLoading, error, refetch, isPlaceholderData } = useFleetVMs({ search, status, node, sort, page, pageSize: PAGE_SIZE })
  const { data: nodes } = useNodes()
  const vmActions = useVMActions()
  const deleteVM = useDeleteVM()

  const rows = data?.vms ?? []
  const total = data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const { transitionOf, begin, cancel } = useVMTransitions(rows)
  const filtered = Boolean(search || status || node)

  const power = useCallback(
    async (vm: FleetVM, action: Power) => {
      if (action !== "start" && !(await confirm(ASK[action](vm.hostname)))) return
      setBusy((b) => ({ ...b, [vm.id]: action }))
      try {
        await vmActions.mutateAsync({ vmId: vm.id, action })
        begin(vm.id, action, vm.status)
        toast.success(`${action[0].toUpperCase()}${action.slice(1)} requested for ${vm.hostname}`)
        refetch()
      } catch (err) {
        cancel(vm.id)
        toast.error(`Could not ${action} ${vm.hostname}: ${(err as Error).message}`)
      } finally {
        setBusy((b) => {
          const next = { ...b }
          delete next[vm.id]
          return next
        })
      }
    },
    [confirm, vmActions, begin, cancel, refetch]
  )

  const remove = useCallback(
    async (vm: FleetVM) => {
      const ok = await confirm({
        title: `Delete "${vm.hostname}"?`,
        description: "The machine and its disks are destroyed and its addresses go back to the pool. This cannot be undone.",
        confirmLabel: "Delete VM",
        destructive: true,
        details: [
          { label: "Owner", value: vm.owner_email || "Unknown" },
          { label: "Node", value: vm.node_name || "Unknown" },
          { label: "Address", value: vm.ip || "None" },
        ],
        action: () => deleteVM.mutateAsync(vm.id),
      })
      // Deleting takes several steps; the progress dialog follows the rest.
      if (ok) setDeletingVM({ id: vm.id, hostname: vm.hostname })
    },
    [confirm, deleteVM]
  )

  const by = data?.by_status ?? {}
  const summary = !data
    ? ""
    : data.all === 0
      ? filtered
        ? "No VM matches."
        : "No VMs yet."
      : `${data.all} ${data.all === 1 ? "VM" : "VMs"}${search || node ? " match" : ""}: ` +
        STATES.filter((s) => by[s.id])
          .map((s) => `${by[s.id]} ${s.label.toLowerCase()}`)
          .join(", ") +
        "."

  return (
    <div className="mx-auto max-w-7xl">
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Virtual machines</h1>
          <p className="mt-1 text-sm text-muted-foreground">{isLoading ? "Loading the VMs" : error && !data ? "The VMs could not be loaded." : summary}</p>
        </div>
        <Button asChild variant={data && data.all === 0 && !filtered ? "default" : "outline"}>
          <Link href="/vms/new">Create a VM</Link>
        </Button>
      </div>

      <div className="mb-4 flex flex-wrap items-center gap-3">
        <div className="min-w-[16rem] flex-1">
          <label htmlFor="vm-search" className="sr-only">
            Search VMs
          </label>
          <Input id="vm-search" type="search" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Search by name, owner, address or ID" />
        </div>
        <label htmlFor="vm-node" className="sr-only">
          Node
        </label>
        <select
          id="vm-node"
          value={node}
          onChange={(e) => {
            setNode(e.target.value)
            setPage(1)
          }}
          className={selectClass}
        >
          <option value="">Every node</option>
          {(nodes ?? []).map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </select>
        <label htmlFor="vm-sort" className="sr-only">
          Order
        </label>
        <select
          id="vm-sort"
          value={sort}
          onChange={(e) => {
            setSort(e.target.value)
            setPage(1)
          }}
          className={selectClass}
        >
          {SORTS.map((s) => (
            <option key={s.id} value={s.id}>
              Order: {s.label}
            </option>
          ))}
        </select>
      </div>

      {/* The states that exist, with how many VMs are in each. */}
      {data && data.all > 0 && (
        <div role="group" aria-label="Show VMs in this state" className="mb-4 flex flex-wrap gap-1.5">
          {[{ id: "", label: "All", n: data.all }, ...STATES.filter((s) => by[s.id]).map((s) => ({ ...s, n: by[s.id] }))].map((s) => (
            <Button
              key={s.id || "all"}
              size="sm"
              variant={status === s.id ? "default" : "outline"}
              aria-pressed={status === s.id}
              onClick={() => {
                setStatus(s.id)
                setPage(1)
              }}
            >
              {s.label} {s.n}
            </Button>
          ))}
        </div>
      )}

      <section aria-label="Virtual machines" className="rounded-lg border bg-card text-card-foreground">
        {isLoading ? (
          <div className="space-y-3 p-5" aria-busy="true">
            {[0, 1, 2, 3, 4].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : error && !data ? (
          <div className="p-8" role="alert">
            <p className="font-medium">The VMs could not be loaded.</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error).message}</p>
            <Button className="mt-4" variant="outline" onClick={() => refetch()}>
              Try again
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="p-8">
            {filtered ? (
              <>
                <p className="font-medium">No VM matches.</p>
                <p className="mt-1 text-sm text-muted-foreground">Nothing has that name, owner, address or ID{status || node ? " in this state and node" : ""}.</p>
                <Button
                  className="mt-4"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setTyped("")
                    setStatus("")
                    setNode("")
                    setPage(1)
                  }}
                >
                  Show every VM
                </Button>
              </>
            ) : (
              <>
                <p className="font-medium">No VMs yet.</p>
                <p className="mt-1 text-sm text-muted-foreground">Create one and it appears here with its owner, address and load.</p>
              </>
            )}
          </div>
        ) : (
          <ul className={`divide-y divide-hair ${isPlaceholderData ? "opacity-60" : ""}`}>
            {rows.map((vm) => {
              const moving = !!transitionOf(vm.id) || !!busy[vm.id] || vm.status === "creating" || vm.status === "deleting"
              const running = vm.status === "running" && !vm.suspended
              const nodeDown = vm.node_status === "offline"
              return (
                <li
                  key={vm.id}
                  className="grid grid-cols-2 gap-x-6 gap-y-3 px-5 py-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,1.2fr)_minmax(0,1.5fr)_minmax(0,0.7fr)_9.5rem] lg:items-center"
                >
                  <div className="col-span-2 min-w-0 lg:col-span-1">
                    <Link href={`/vms/${vm.id}`} className="break-all text-base font-semibold underline-offset-4 hover:underline [font-stretch:112%]">
                      {vm.hostname}
                    </Link>
                    <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
                      <StatusBadge status={vm.suspended ? "suspended" : vm.status} transition={transitionOf(vm.id)} />
                      {vm.rescue_mode && <span className="text-xs font-medium text-warning">Rescue mode</span>}
                    </div>
                  </div>

                  <div className="min-w-0">
                    <p className="text-xs text-muted-foreground lg:sr-only">Address</p>
                    {vm.ip ? (
                      <p className="text-sm">
                        <span className="font-mono">{vm.ip}</span>
                        <button
                          type="button"
                          onClick={() => copy(vm.ip, vm.id)}
                          aria-label={copied(vm.id) ? "Copied" : `Copy address ${vm.ip}`}
                          className="ml-2 border-b border-border pb-px text-xs font-semibold hover:border-foreground active:translate-y-px"
                        >
                          {copied(vm.id) ? "Copied" : "Copy"}
                        </button>
                      </p>
                    ) : (
                      <p className="text-sm text-muted-foreground">No address</p>
                    )}
                  </div>

                  <div className="min-w-0">
                    <p className="text-xs text-muted-foreground lg:sr-only">Owner and node</p>
                    {vm.owner_email ? (
                      <Link href={`/users/${vm.owner_id}`} className="block truncate text-sm underline-offset-4 hover:underline" title={vm.owner_email}>
                        {vm.owner_email}
                      </Link>
                    ) : (
                      <p className="text-sm text-muted-foreground">No owner</p>
                    )}
                    <p className={`truncate text-xs ${nodeDown ? "font-medium text-destructive" : "text-muted-foreground"}`}>
                      {vm.node_name ? `on ${vm.node_name}${nodeDown ? ", which is offline" : ""}` : "on an unknown node"}
                    </p>
                  </div>

                  <div className="flex min-w-0 items-center gap-3">
                    {/* The system's own mark, so a row is recognised before it is read. */}
                    <OSLogoTile name={vm.system} className="h-8 w-8" />
                    <div className="min-w-0">
                      <p className="truncate text-sm" title={vm.system}>
                        {vm.system || "Unknown system"}
                      </p>
                      <p className="truncate text-xs text-muted-foreground">
                        {vm.cpu} vCPU, {Number((vm.ram_mb / 1024).toFixed(1))} GB, {vm.disk_gb} GB disk
                      </p>
                    </div>
                  </div>

                  <div className="min-w-0">
                    <p className="text-xs text-muted-foreground lg:sr-only">Load now</p>
                    {running && vm.cpu_now !== null ? (
                      <>
                        <p className="whitespace-nowrap text-sm">
                          CPU <span className="font-semibold">{Math.round(vm.cpu_now)}%</span>
                        </p>
                        <Meter percent={vm.cpu_now} over={vm.cpu_now >= 90} />
                        {vm.memory_now ? <p className="mt-1 whitespace-nowrap text-xs text-muted-foreground">Memory {Math.round(vm.memory_now)}%</p> : null}
                      </>
                    ) : (
                      <p className="text-sm text-muted-foreground">{running ? "No reading" : "Off"}</p>
                    )}
                  </div>

                  <div className="col-span-2 flex flex-wrap items-center gap-2 lg:col-span-1 lg:justify-end">
                    {running ? (
                      <Button size="sm" variant="outline" disabled={moving} onClick={() => power(vm, "restart")}>
                        Restart
                      </Button>
                    ) : vm.status === "stopped" && !vm.suspended ? (
                      <Button size="sm" variant="outline" disabled={moving} onClick={() => power(vm, "start")}>
                        Start
                      </Button>
                    ) : null}
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button size="sm" variant="ghost" aria-label={`More actions for ${vm.hostname}`}>
                          More
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-48">
                        <DropdownMenuItem asChild className="cursor-pointer">
                          <Link href={`/vms/${vm.id}`}>Open</Link>
                        </DropdownMenuItem>
                        {running && (
                          <>
                            <DropdownMenuItem asChild className="cursor-pointer">
                              <a href={`/vms/${vm.id}/console`} target="_blank" rel="noreferrer">
                                Console in a new window
                              </a>
                            </DropdownMenuItem>
                            <DropdownMenuItem className="cursor-pointer" disabled={moving} onSelect={() => power(vm, "stop")}>
                              Stop
                            </DropdownMenuItem>
                          </>
                        )}
                        <DropdownMenuSeparator />
                        <DropdownMenuItem className="cursor-pointer text-destructive focus:text-destructive" disabled={vm.status === "deleting"} onSelect={() => remove(vm)}>
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </section>

      {total > PAGE_SIZE && (
        <nav aria-label="Pages" className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm">
          <p className="text-muted-foreground">
            {(page - 1) * PAGE_SIZE + 1} to {Math.min(page * PAGE_SIZE, total)} of {total}
          </p>
          <div className="flex gap-2">
            <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
              Previous
            </Button>
            <Button size="sm" variant="outline" disabled={page >= pages} onClick={() => setPage((p) => p + 1)}>
              Next
            </Button>
          </div>
        </nav>
      )}

      <DeleteProgressDialog
        vm={deletingVM}
        onClose={() => {
          setDeletingVM(null)
          refetch()
        }}
      />
    </div>
  )
}
