"use client"

import { useState } from "react"
import { Loader2, Network, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { PoolAllocation } from "@/components/pool-allocation"
import { cephHealth } from "@/lib/ceph"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import { useCephClusters, useChangeSharedPool, useCheckSharedPool, useSetPoolTier, useSetPrimaryPool, useSharedPools } from "@/lib/hooks/use-storage"
import type { SharedPool, SharedPoolMember } from "@/types/storage"

type NodeRef = { id: string; name: string }

const memberState = (m: SharedPoolMember) =>
  m.status === "online" ? { word: "Usable", cls: "text-emerald-700 dark:text-emerald-300" }
  : m.status === "degraded" ? { word: "Not usable", cls: "text-amber-700 dark:text-amber-300" }
  : { word: "Not measured yet", cls: "text-muted-foreground" }

// SharedPools shows each shared pool once, with the nodes that mount it. The
// nodes' own rows for it are left out of the pool cards below.
export function SharedPools({ nodes }: { nodes: NodeRef[] }) {
  const { data: pools, error } = useSharedPools()
  if (error) {
    return <p className="text-sm text-muted-foreground mb-6">Shared pools could not be loaded: {error.message}</p>
  }
  if (!pools || pools.length === 0) return null
  return (
    <section className="mb-6" aria-labelledby="shared-pools-heading">
      <h2 id="shared-pools-heading" className="text-lg font-semibold mb-1">Shared pools</h2>
      <p className="text-sm text-muted-foreground mb-3">
        Storage several nodes use: a directory at the same path, or an RBD pool of a Ceph cluster. A VM whose disks
        are here moves between these nodes without its disks being copied.
      </p>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {pools.map((pool) => <SharedPoolCard key={pool.id} pool={pool} nodes={nodes} />)}
      </div>
    </section>
  )
}

function SharedPoolCard({ pool, nodes }: { pool: SharedPool; nodes: NodeRef[] }) {
  const confirm = useConfirm()
  const change = useChangeSharedPool()
  const check = useCheckSharedPool()
  const setPrimary = useSetPrimaryPool()
  const setTier = useSetPoolTier()
  // The tier dialog: one tier for the pool, set on every member's row.
  const [tier, setTierValue] = useState<string | null>(null)
  const [addNode, setAddNode] = useState("")
  const candidates = nodes.filter((n) => !pool.members.some((m) => m.node_id === n.id))
  const measured = pool.total_space > 0
  // An RBD pool's members are the nodes its Ceph cluster is enabled on.
  const rbd = pool.kind === "rbd"
  const { data: clusters } = useCephClusters()
  const cluster = clusters?.find((c) => c.id === pool.ceph_cluster_id)
  const clusterName = cluster?.name
  const health = cluster ? cephHealth(cluster) : null

  const add = () => {
    const node = nodes.find((n) => n.id === addNode)
    if (!node) return
    change.mutate({ op: "add-node", id: pool.id, nodeId: node.id }, {
      onSuccess: () => { toast.success(`${node.name} now uses ${pool.name}`); setAddNode("") },
      onError: (err) => toast.error(`${node.name} was not added`, { description: err.message }),
    })
  }

  const removeNode = async (m: SharedPoolMember) => {
    const ok = await confirm({
      title: `Remove ${m.node_name || "this node"} from "${pool.name}"?`,
      description: pool.kind === "nfs"
        ? "The node unmounts the pool. This is refused while a VM on that node has a disk in it. Files in the pool are not removed."
        : "The node stops using the pool. This is refused while a VM on that node has a disk in it. Files in the pool are not removed, and your own mount is left alone.",
      confirmLabel: "Remove node",
      destructive: true,
      action: () => change.mutateAsync({ op: "remove-node", id: pool.id, nodeId: m.node_id }),
    })
    if (ok) toast.success(`${m.node_name || "The node"} no longer uses ${pool.name}`)
  }

  const removePool = async () => {
    const ok = await confirm({
      title: `Delete shared pool "${pool.name}"?`,
      description: rbd
        ? "Only its record is removed. Its images on the cluster stay. This is refused while a VM or a volume is in the pool."
        : "Only its record is removed. Files on the storage stay.",
      confirmLabel: "Delete shared pool",
      destructive: true,
      action: () => change.mutateAsync({ op: "delete", id: pool.id }),
    })
    if (ok) toast.success(`Shared pool "${pool.name}" deleted`)
  }

  const saveTier = async () => {
    if (tier === null) return
    try {
      for (const m of pool.members) await setTier.mutateAsync({ id: m.pool_id, tier })
      toast.success(tier.trim() ? `${pool.name} is in tier ${tier.trim().toLowerCase()}` : `${pool.name} has no tier`)
      setTierValue(null)
    } catch (err) {
      toast.error("The tier was not saved", { description: err instanceof Error ? err.message : undefined })
    }
  }

  const runCheck = () => check.mutate(pool.id, {
    onSuccess: (res) => {
      const failed = pool.members.filter((m) => res[m.node_id])
      if (failed.length === 0) toast.success(`Every node sees ${pool.name}`)
      else toast.error(`${failed.map((m) => m.node_name).join(", ")} cannot use ${pool.name}`, { description: res[failed[0].node_id] })
    },
    onError: (err) => toast.error(err.message),
  })

  return (
    <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
      <div className="p-4 border-b bg-muted/50 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-lg font-semibold flex items-center gap-2">
            <Network className="w-4 h-4 shrink-0" aria-hidden="true" />
            <span className="truncate">{pool.name}</span>
          </h3>
          <p className="text-sm font-mono text-muted-foreground break-all">
            {pool.kind === "nfs" ? `${pool.nfs_server}:${pool.nfs_export}` : pool.path}
          </p>
          {rbd && <p className="text-xs text-muted-foreground break-all mt-1">On Ceph cluster {clusterName ?? "(no longer registered)"}</p>}
          {pool.kind === "nfs" && <p className="text-xs font-mono text-muted-foreground break-all mt-1">mounted at {pool.path}</p>}
        </div>
        <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-medium border rounded-full bg-muted text-muted-foreground shrink-0">
          {pool.kind === "nfs" ? "NFS, mounted by the nodes" : rbd ? "RBD pool" : "Mounted by you"}
        </span>
      </div>

      <div className="p-4 space-y-4">
        <p className="text-sm">
          {measured
            ? <>{formatBytes(pool.used_space)} used of {formatBytes(pool.total_space)}, <span className="font-medium">{formatBytes(pool.available_space)} free</span></>
            : <span className="text-muted-foreground">{pool.members.length === 0 ? "No node uses it yet." : "Not measured yet. A node reports it within a minute."}</span>}
        </p>
        {measured && <PoolAllocation pool={pool} />}
        {rbd && (
          <div className="space-y-1">
            {health && (
              <p className="text-sm flex flex-wrap items-center gap-2">
                Cluster health
                <span className={`inline-flex items-center px-2 py-0.5 text-xs font-medium border rounded-full ${health.cls}`}>{health.word}</span>
              </p>
            )}
            <p className="text-xs text-muted-foreground">
              VM disks here are RBD images. A VM moves between these nodes without copying its disks, and is recovered
              on them if its node fails. Snapshots hold the disks, not the memory; backups are full copies.
            </p>
          </div>
        )}

        <div>
          <p className="text-xs font-medium text-muted-foreground mb-1">Nodes</p>
          {pool.members.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {rbd ? "Enable the pool's Ceph cluster on a node, on the cluster's card, and the node joins this pool."
                : "Add the nodes that reach this storage. Each is checked before it joins."}
            </p>
          ) : (
            <ul className="divide-y border rounded-md">
              {pool.members.map((m) => {
                const st = memberState(m)
                return (
                  <li key={m.pool_id} className="px-3 py-2">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <p className="text-sm font-medium">
                        {m.node_name || m.node_id}
                        <span className={`ml-2 text-xs font-medium ${st.cls}`}>{st.word}</span>
                        {m.is_primary && <span className="ml-2 text-xs font-normal text-muted-foreground">New VMs go here</span>}
                        {m.tier && <span className="ml-2 text-xs font-normal text-muted-foreground">Tier {m.tier}</span>}
                      </p>
                      <div className="flex items-center gap-2">
                        {!m.is_primary && m.status === "online" && (
                          <Button variant="outline" size="sm" disabled={setPrimary.isPending}
                            title={`Create new VMs on ${m.node_name} in this pool`}
                            onClick={() => setPrimary.mutate(m.pool_id, {
                              onSuccess: () => toast.success(`New VMs on ${m.node_name} will be created in "${pool.name}"`),
                              onError: (err) => toast.error(err.message),
                            })}>
                            Use for new VMs
                          </Button>
                        )}
                        {!rbd && (
                          <Button variant="outline" size="sm" onClick={() => removeNode(m)} aria-label={`Remove ${m.node_name} from ${pool.name}`}>
                            Remove
                          </Button>
                        )}
                      </div>
                    </div>
                    {m.status !== "online" && m.status_note && (
                      <p className="text-xs text-amber-700 dark:text-amber-300 mt-1">{m.status_note}</p>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {!rbd && (
            <>
              <label htmlFor={`shared-add-${pool.id}`} className="sr-only">Node to add to {pool.name}</label>
              <select id={`shared-add-${pool.id}`} value={addNode} onChange={(e) => setAddNode(e.target.value)}
                className="h-9 px-3 rounded-md border border-input bg-background text-sm min-w-0 flex-1">
                <option value="">{candidates.length === 0 ? "Every node is a member" : "Add a node"}</option>
                {candidates.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
              </select>
              <Button size="sm" onClick={add} disabled={!addNode || change.isPending}>
                {change.isPending && change.variables?.op === "add-node" && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                {change.isPending && change.variables?.op === "add-node" ? "Checking the node" : "Add node"}
              </Button>
            </>
          )}
          {pool.members.length > 0 && (
            <Button variant="outline" size="sm" onClick={runCheck} disabled={check.isPending}>
              {check.isPending ? "Checking" : "Check now"}
            </Button>
          )}
          {pool.members.length > 0 && (
            <Button variant="outline" size="sm" onClick={() => setTierValue(pool.members[0].tier ?? "")}>
              Tier
            </Button>
          )}
          {(rbd || pool.members.length === 0) && (
            <Button variant="outline" size="sm" onClick={removePool} aria-label={`Delete shared pool ${pool.name}`}
              className="text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950">
              <Trash2 className="w-4 h-4" aria-hidden="true" />
            </Button>
          )}
        </div>
      </div>

      <Dialog open={tier !== null} onOpenChange={(o) => { if (!o) setTierValue(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Storage tier of {pool.name}</DialogTitle>
            <DialogDescription>
              Plans naming this tier put their VMs&apos; disks in this pool, on every node that uses it now. Leave empty for no tier.
            </DialogDescription>
          </DialogHeader>
          <label htmlFor={`shared-tier-${pool.id}`} className="text-sm font-medium">Tier</label>
          <Input id={`shared-tier-${pool.id}`} value={tier ?? ""} onChange={(e) => setTierValue(e.target.value)} placeholder="ceph" />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setTierValue(null)}>Cancel</Button>
            <Button disabled={setTier.isPending} onClick={saveTier}>{setTier.isPending ? "Saving" : "Save"}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

const emptyShared = {
  name: "", kind: "nfs" as "nfs" | "mounted" | "rbd", path: "", nfs_server: "", nfs_export: "", mount_options: "",
  ceph_cluster_id: "", rbd_pool: "", rbd_namespace: "",
}
const rbdNameOk = (s: string) => /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(s)

// AddSharedPoolDialog records a shared pool. No node is touched until one is
// added on the pool's card.
export function AddSharedPoolDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const change = useChangeSharedPool()
  const [form, setForm] = useState(emptyShared)
  const set = (patch: Partial<typeof emptyShared>) => setForm({ ...form, ...patch })
  const exportOk = /^\/[A-Za-z0-9._/-]*$/.test(form.nfs_export.trim())
  const { data: clusters } = useCephClusters()
  const rbdPool = form.rbd_pool.trim()
  const rbdNamespace = form.rbd_namespace.trim()
  const ready = !!form.name.trim() && (form.kind === "nfs"
    ? !!form.nfs_server.trim() && exportOk
    : form.kind === "rbd"
      ? !!form.ceph_cluster_id && rbdNameOk(rbdPool) && (!rbdNamespace || rbdNameOk(rbdNamespace))
      : form.path.trim().startsWith("/") && form.path.trim().length > 1)

  const submit = () => {
    if (!ready) return
    change.mutate({ op: "create", pool: form.kind === "nfs"
      ? { name: form.name.trim(), kind: "nfs", nfs_server: form.nfs_server.trim(), nfs_export: form.nfs_export.trim(), mount_options: form.mount_options.trim() }
      : form.kind === "rbd"
        ? { name: form.name.trim(), kind: "rbd", ceph_cluster_id: form.ceph_cluster_id, rbd_pool: rbdPool, rbd_namespace: rbdNamespace }
        : { name: form.name.trim(), kind: "mounted", path: form.path.trim() } }, {
      onSuccess: () => {
        toast.success(`Shared pool "${form.name.trim()}" added`, {
          description: form.kind === "rbd" ? "The nodes its Ceph cluster is enabled on are its members." : "Now add the nodes that reach it, on its card.",
        })
        setForm(emptyShared)
        onOpenChange(false)
      },
      onError: (err) => toast.error("The shared pool was not added", { description: err.message }),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Add shared pool</DialogTitle>
          <DialogDescription>
            Storage that several nodes reach: an NFS export the nodes mount for you, or a directory you already mount
            at the same path on each node (NFS, CephFS, GlusterFS or another cluster file system), or an RBD pool of a
            registered Ceph cluster. Nothing happens on any node until you add it to the pool; an RBD pool is used by
            the nodes its cluster is enabled on.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div>
            <label htmlFor="shared-name" className="text-xs font-medium text-muted-foreground">Name</label>
            <Input id="shared-name" value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="vmstore" />
          </div>
          <div>
            <label htmlFor="shared-kind" className="text-xs font-medium text-muted-foreground">Kind</label>
            <select id="shared-kind" value={form.kind} onChange={(e) => set({ kind: e.target.value as typeof form.kind })}
              className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
              <option value="nfs">NFS export, mounted by the nodes</option>
              <option value="mounted">A directory I mount myself</option>
              <option value="rbd">RBD pool of a Ceph cluster</option>
            </select>
          </div>
          {form.kind === "rbd" ? (
            <>
              <div>
                <label htmlFor="shared-cluster" className="text-xs font-medium text-muted-foreground">Ceph cluster</label>
                <select id="shared-cluster" value={form.ceph_cluster_id} onChange={(e) => set({ ceph_cluster_id: e.target.value })}
                  className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
                  <option value="">{clusters && clusters.length > 0 ? "Choose a cluster" : "No Ceph cluster is registered"}</option>
                  {(clusters ?? []).map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
                {clusters && clusters.length === 0 && (
                  <p className="text-xs text-muted-foreground mt-1">Register the cluster first, with Add Ceph cluster on the Storage page.</p>
                )}
              </div>
              <div>
                <label htmlFor="shared-rbd-pool" className="text-xs font-medium text-muted-foreground">RBD pool</label>
                <Input id="shared-rbd-pool" value={form.rbd_pool} onChange={(e) => set({ rbd_pool: e.target.value })} placeholder="vms"
                  aria-invalid={!!rbdPool && !rbdNameOk(rbdPool)} />
                <p className="text-xs text-muted-foreground mt-1">
                  A pool that already exists on the cluster, with the rbd application enabled. It is checked on the
                  cluster&apos;s nodes before it is added.
                </p>
              </div>
              <div>
                <label htmlFor="shared-rbd-namespace" className="text-xs font-medium text-muted-foreground">Namespace (optional)</label>
                <Input id="shared-rbd-namespace" value={form.rbd_namespace} onChange={(e) => set({ rbd_namespace: e.target.value })} placeholder="tenant-a"
                  aria-invalid={!!rbdNamespace && !rbdNameOk(rbdNamespace)} />
                {((!!rbdPool && !rbdNameOk(rbdPool)) || (!!rbdNamespace && !rbdNameOk(rbdNamespace))) && (
                  <p className="text-xs text-destructive mt-1">Use letters, digits, dots, dashes and underscores, starting with a letter or digit.</p>
                )}
              </div>
            </>
          ) : form.kind === "nfs" ? (
            <>
              <div>
                <label htmlFor="shared-server" className="text-xs font-medium text-muted-foreground">NFS server</label>
                <Input id="shared-server" value={form.nfs_server} onChange={(e) => set({ nfs_server: e.target.value })} placeholder="nas.example.net" />
              </div>
              <div>
                <label htmlFor="shared-export" className="text-xs font-medium text-muted-foreground">Export</label>
                <Input id="shared-export" value={form.nfs_export} onChange={(e) => set({ nfs_export: e.target.value })} placeholder="/export/vms"
                  aria-invalid={!!form.nfs_export && !exportOk} />
                {form.nfs_export && !exportOk && <p className="text-xs text-destructive mt-1">Enter the export&apos;s full path, starting with /.</p>}
              </div>
              <div>
                <label htmlFor="shared-options" className="text-xs font-medium text-muted-foreground">Mount options (optional)</label>
                <Input id="shared-options" value={form.mount_options} onChange={(e) => set({ mount_options: e.target.value })} placeholder="vers=4.2,noatime" />
                <p className="text-xs text-muted-foreground mt-1">
                  Mounted hard. The options nolock, local_lock=all, local_lock=posix and soft are refused: they put disk images at risk.
                </p>
              </div>
            </>
          ) : (
            <div>
              <label htmlFor="shared-path" className="text-xs font-medium text-muted-foreground">Directory, the same on every node</label>
              <Input id="shared-path" value={form.path} onChange={(e) => set({ path: e.target.value })} placeholder="/mnt/vmstore" />
              <p className="text-xs text-muted-foreground mt-1">
                Mount it on each node before adding that node, and make it mount at boot. A directory pool already
                registered at this path on a node becomes that node&apos;s part of the shared pool.
              </p>
            </div>
          )}
        </div>
        <DialogFooter className="gap-3 sm:gap-0">
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={submit} disabled={!ready || change.isPending}>{change.isPending ? "Adding..." : "Add shared pool"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
