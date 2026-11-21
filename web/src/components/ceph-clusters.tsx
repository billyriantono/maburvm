"use client"

import { useEffect, useState } from "react"
import { Boxes, Loader2, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { cephHealth } from "@/lib/ceph"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import { useCephClusters, useChangeCephCluster, useCheckCephCluster } from "@/lib/hooks/use-storage"
import type { CephCluster, CephClusterNode } from "@/types/storage"

type NodeRef = { id: string; name: string }

const nodeState = (n: CephClusterNode) =>
  n.status === "ok" ? { word: "Reaches the cluster", cls: "text-emerald-700 dark:text-emerald-300" }
  : n.status === "mismatch" ? { word: "Reaches another cluster", cls: "text-red-700 dark:text-red-300" }
  : n.status === "degraded" ? { word: "Needs attention", cls: "text-amber-700 dark:text-amber-300" }
  : { word: "Cannot reach the cluster", cls: "text-red-700 dark:text-red-300" }

// CephClusters shows each registered Ceph cluster: its health, capacity, RBD
// pools and the nodes it is enabled on. Nothing is shown when none is
// registered.
export function CephClusters({ nodes }: { nodes: NodeRef[] }) {
  const { data: clusters, error } = useCephClusters()
  const [editing, setEditing] = useState<CephCluster | null>(null)
  if (error) {
    return <p className="text-sm text-muted-foreground mb-6">Ceph clusters could not be loaded: {error.message}</p>
  }
  if (!clusters || clusters.length === 0) return null
  return (
    <section className="mb-6" aria-labelledby="ceph-clusters-heading">
      <h2 id="ceph-clusters-heading" className="text-lg font-semibold mb-1">Ceph clusters</h2>
      <p className="text-sm text-muted-foreground mb-3">
        Existing clusters this panel is a client of. Enable a cluster on the nodes that reach it, then add its RBD
        pools with Add shared pool. The panel does not install, grow or upgrade a cluster.
      </p>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {clusters.map((c) => <CephClusterCard key={c.id} cluster={c} nodes={nodes} onEdit={() => setEditing(c)} />)}
      </div>
      <CephClusterDialog open={!!editing} onOpenChange={(open) => { if (!open) setEditing(null) }} cluster={editing} />
    </section>
  )
}

function CephClusterCard({ cluster, nodes, onEdit }: { cluster: CephCluster; nodes: NodeRef[]; onEdit: () => void }) {
  const confirm = useConfirm()
  const change = useChangeCephCluster()
  const check = useCheckCephCluster()
  const [addNode, setAddNode] = useState("")
  const candidates = nodes.filter((n) => !cluster.nodes.some((m) => m.node_id === n.id))
  const health = cephHealth(cluster)
  const enabling = change.isPending && change.variables?.op === "enable-node"

  const enable = () => {
    const node = nodes.find((n) => n.id === addNode)
    if (!node) return
    change.mutate({ op: "enable-node", id: cluster.id, nodeId: node.id }, {
      onSuccess: () => { toast.success(`${cluster.name} is enabled on ${node.name}`); setAddNode("") },
      onError: (err) => toast.error(`${cluster.name} was not enabled on ${node.name}`, { description: err.message }),
    })
  }

  const disable = async (n: CephClusterNode) => {
    const ok = await confirm({
      title: `Disable "${cluster.name}" on ${n.node_name || "this node"}?`,
      description: "The node drops the cluster's settings, keyring and libvirt secret, and leaves the cluster's RBD pools. This is refused while a VM on that node has a disk on the cluster. Nothing on the cluster is removed.",
      confirmLabel: "Disable on node",
      destructive: true,
      action: () => change.mutateAsync({ op: "disable-node", id: cluster.id, nodeId: n.node_id }),
    })
    if (ok) toast.success(`${cluster.name} is disabled on ${n.node_name || "the node"}`)
  }

  const remove = async () => {
    const ok = await confirm({
      title: `Unregister Ceph cluster "${cluster.name}"?`,
      description: "Only the panel's record and its stored key are removed. Nothing on the cluster changes.",
      confirmLabel: "Unregister cluster",
      destructive: true,
      action: () => change.mutateAsync({ op: "delete", id: cluster.id }),
    })
    if (ok) toast.success(`Ceph cluster "${cluster.name}" unregistered`)
  }

  const runCheck = () => check.mutate(cluster.id, {
    onSuccess: (res) => {
      const failed = cluster.nodes.filter((n) => res[n.node_id])
      if (failed.length === 0) toast.success(`Every node reaches ${cluster.name}`)
      else toast.error(`${failed.map((n) => n.node_name).join(", ")} cannot use ${cluster.name}`, { description: res[failed[0].node_id] })
    },
    onError: (err) => toast.error(err.message),
  })

  return (
    <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
      <div className="p-4 border-b bg-muted/50 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-lg font-semibold flex items-center gap-2">
            <Boxes className="w-4 h-4 shrink-0" aria-hidden="true" />
            <span className="truncate">{cluster.name}</span>
          </h3>
          <p className="text-sm font-mono text-muted-foreground break-all">{cluster.monitors.split(",").join(", ")}</p>
          <p className="text-xs text-muted-foreground break-all mt-1">
            Client <span className="font-mono">client.{cluster.client_id}</span>
            {cluster.fsid && <>, fsid <span className="font-mono">{cluster.fsid}</span></>}
          </p>
        </div>
        <span className={`inline-flex items-center px-2 py-0.5 text-xs font-medium border rounded-full shrink-0 ${health.cls}`}
          title={cluster.health || undefined}>
          {health.word}
        </span>
      </div>

      <div className="p-4 space-y-4">
        {cluster.health_summary && cluster.health !== "HEALTH_OK" && (
          <ul className="text-sm space-y-0.5" aria-label={`Health checks of ${cluster.name}`}>
            {cluster.health_summary.split("\n").map((line) => <li key={line} className="font-mono text-xs break-words">{line}</li>)}
          </ul>
        )}
        <p className="text-sm">
          {cluster.total_bytes > 0
            ? <>Raw capacity: {formatBytes(cluster.used_bytes)} used of {formatBytes(cluster.total_bytes)}, <span className="font-medium">{formatBytes(cluster.available_bytes)} free</span></>
            : <span className="text-muted-foreground">{cluster.nodes.length === 0 ? "Enable it on a node to read its health and capacity." : "Raw capacity is not reported. A node reads it within a minute, when the client may."}</span>}
          {cluster.checked_at && <span className="text-xs text-muted-foreground"> (read {new Date(cluster.checked_at).toLocaleString()})</span>}
        </p>

        <div>
          <p className="text-xs font-medium text-muted-foreground mb-1">RBD pools</p>
          {cluster.pools.length === 0 ? (
            <p className="text-sm text-muted-foreground">None yet. Add one with Add shared pool, kind RBD pool.</p>
          ) : (
            <ul className="divide-y border rounded-md">
              {cluster.pools.map((p) => (
                <li key={p.id} className="px-3 py-2 flex flex-wrap items-center justify-between gap-2 text-sm">
                  <span>
                    <span className="font-medium">{p.name}</span>
                    <span className="ml-2 font-mono text-xs text-muted-foreground">{p.rbd_namespace ? `${p.rbd_pool}/${p.rbd_namespace}` : p.rbd_pool}</span>
                  </span>
                  <span className="text-muted-foreground">
                    {p.total_space > 0 ? `${formatBytes(p.used_space)} used, ${formatBytes(p.available_space)} free` : "Not measured yet"}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div>
          <p className="text-xs font-medium text-muted-foreground mb-1">Nodes</p>
          {cluster.nodes.length === 0 ? (
            <p className="text-sm text-muted-foreground">Enable the cluster on the nodes that reach its monitors and OSDs. Each is checked first.</p>
          ) : (
            <ul className="divide-y border rounded-md">
              {cluster.nodes.map((n) => {
                const st = nodeState(n)
                return (
                  <li key={n.node_id} className="px-3 py-2">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <p className="text-sm font-medium">
                        {n.node_name || n.node_id}
                        <span className={`ml-2 text-xs font-medium ${st.cls}`}>{st.word}</span>
                      </p>
                      <Button variant="outline" size="sm" onClick={() => disable(n)} aria-label={`Disable ${cluster.name} on ${n.node_name}`}>
                        Disable
                      </Button>
                    </div>
                    {n.status !== "ok" && n.status_note && (
                      <p className="text-xs text-amber-700 dark:text-amber-300 mt-1 break-words">{n.status_note}</p>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <label htmlFor={`ceph-add-${cluster.id}`} className="sr-only">Node to enable {cluster.name} on</label>
          <select id={`ceph-add-${cluster.id}`} value={addNode} onChange={(e) => setAddNode(e.target.value)}
            className="h-9 px-3 rounded-md border border-input bg-background text-sm min-w-0 flex-1">
            <option value="">{candidates.length === 0 ? "Enabled on every node" : "Enable on a node"}</option>
            {candidates.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </select>
          <Button size="sm" onClick={enable} disabled={!addNode || change.isPending}>
            {enabling && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
            {enabling ? "Checking the node" : "Enable"}
          </Button>
          {cluster.nodes.length > 0 && (
            <Button variant="outline" size="sm" onClick={runCheck} disabled={check.isPending}>
              {check.isPending ? "Checking" : "Check now"}
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={onEdit}>Edit</Button>
          {cluster.nodes.length === 0 && cluster.pools.length === 0 && (
            <Button variant="outline" size="sm" onClick={remove} aria-label={`Unregister Ceph cluster ${cluster.name}`}
              className="text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950">
              <Trash2 className="w-4 h-4" aria-hidden="true" />
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}

const emptyCluster = { name: "", monitors: "", client_id: "", client_key: "", fsid: "" }

// CephClusterDialog registers a cluster, or changes one. The key is only
// ever sent: the panel never shows a stored key.
export function CephClusterDialog({ open, onOpenChange, cluster }: {
  open: boolean; onOpenChange: (open: boolean) => void; cluster?: CephCluster | null
}) {
  const change = useChangeCephCluster()
  const [form, setForm] = useState(emptyCluster)
  const set = (patch: Partial<typeof emptyCluster>) => setForm({ ...form, ...patch })
  useEffect(() => {
    if (open) setForm(cluster ? { name: cluster.name, monitors: cluster.monitors.split(",").join(", "), client_id: cluster.client_id, client_key: "", fsid: cluster.fsid } : emptyCluster)
  }, [open, cluster])
  const ready = !!form.name.trim() && !!form.monitors.trim() && !!form.client_id.trim() && (!!cluster || !!form.client_key.trim())

  const submit = () => {
    if (!ready) return
    const body = { name: form.name.trim(), monitors: form.monitors.trim(), client_id: form.client_id.trim(), client_key: form.client_key.trim() }
    change.mutate(cluster ? { op: "update", id: cluster.id, cluster: body } : { op: "create", cluster: { ...body, fsid: form.fsid.trim() } }, {
      onSuccess: () => {
        toast.success(cluster ? `Ceph cluster "${body.name}" changed` : `Ceph cluster "${body.name}" registered`,
          cluster ? undefined : { description: "Now enable it on the nodes that reach it, on its card." })
        onOpenChange(false)
      },
      onError: (err) => toast.error(cluster ? "The cluster was not changed" : "The cluster was not registered", { description: err.message }),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{cluster ? `Change ${cluster.name}` : "Register Ceph cluster"}</DialogTitle>
          <DialogDescription>
            {cluster
              ? "New monitors, client id or key are sent to the nodes the cluster is enabled on. Each proves them before it uses them; if the first node refuses them, nothing changes."
              : "An existing cluster the panel becomes a client of. Nothing happens on any node until you enable the cluster on it. The panel does not create or manage the cluster itself."}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div>
            <label htmlFor="ceph-name" className="text-xs font-medium text-muted-foreground">Name</label>
            <Input id="ceph-name" value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="ceph-jkt" />
          </div>
          <div>
            <label htmlFor="ceph-monitors" className="text-xs font-medium text-muted-foreground">Monitor addresses</label>
            <Input id="ceph-monitors" value={form.monitors} onChange={(e) => set({ monitors: e.target.value })} placeholder="10.0.0.1, 10.0.0.2, 10.0.0.3" />
            <p className="text-xs text-muted-foreground mt-1">Separated by commas. Add :port only when it is not the default; put an IPv6 address in brackets.</p>
          </div>
          <div>
            <label htmlFor="ceph-client" className="text-xs font-medium text-muted-foreground">Client id</label>
            <Input id="ceph-client" value={form.client_id} onChange={(e) => set({ client_id: e.target.value })} placeholder="maburvm" autoComplete="off" />
            <p className="text-xs text-muted-foreground mt-1">The cephx user, without &quot;client.&quot;. Give it the rbd profile on the pools you will use, not the admin key.</p>
          </div>
          <div>
            <label htmlFor="ceph-key" className="text-xs font-medium text-muted-foreground">Key{cluster ? " (leave empty to keep the stored key)" : ""}</label>
            <Input id="ceph-key" type="password" value={form.client_key} onChange={(e) => set({ client_key: e.target.value })}
              placeholder={cluster ? "Stored. Not shown." : "Output of ceph auth get-key"} autoComplete="new-password" />
            <p className="text-xs text-muted-foreground mt-1">Stored encrypted and never shown again.</p>
          </div>
          {!cluster && (
            <div>
              <label htmlFor="ceph-fsid" className="text-xs font-medium text-muted-foreground">Cluster fsid (optional)</label>
              <Input id="ceph-fsid" value={form.fsid} onChange={(e) => set({ fsid: e.target.value })} placeholder="Recorded from the first node when empty" autoComplete="off" />
              <p className="text-xs text-muted-foreground mt-1">Every node must reach the cluster with this fsid. A node that reaches another is refused.</p>
            </div>
          )}
        </div>
        <DialogFooter className="gap-3 sm:gap-0">
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={submit} disabled={!ready || change.isPending}>
            {change.isPending ? "Saving..." : cluster ? "Save changes" : "Register cluster"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
