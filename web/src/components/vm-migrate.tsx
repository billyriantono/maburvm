"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { useNodes } from "@/lib/hooks/use-nodes"
import { useStoragePools } from "@/lib/hooks/use-storage"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import { migrationActive, useCancelMigration, useMigrationCheck, useStartMigration, useVMMigration } from "@/lib/hooks/use-migration"
import type { MigrationCheck, VMMigration } from "@/types"

const TONE = {
  busy: { text: "text-amber-700 dark:text-amber-400", dot: "bg-amber-500" },
  ok: { text: "text-emerald-700 dark:text-emerald-400", dot: "bg-emerald-500" },
  bad: { text: "text-red-700 dark:text-red-400", dot: "bg-red-500" },
  off: { text: "text-muted-foreground", dot: "bg-muted-foreground" },
}

function Lamp({ tone, word }: { tone: keyof typeof TONE; word: string }) {
  return (
    <span className={`inline-flex items-center gap-1.5 font-medium ${TONE[tone].text}`}>
      <span className={`h-2 w-2 rounded-full ${TONE[tone].dot}`} aria-hidden="true" />
      {word}
    </span>
  )
}

function percent(m: VMMigration) {
  return m.bytes_total > 0 ? Math.min(100, Math.floor((m.bytes_done / m.bytes_total) * 100)) : null
}

// MoveState is the VM's current or last move, in a line.
function MoveState({ m, nodeName }: { m: VMMigration; nodeName: (id: string | null) => string }) {
  const to = nodeName(m.dest_node_id)
  if (migrationActive(m)) {
    const p = percent(m)
    return (
      <span className="mt-1 block">
        <Lamp tone="busy" word={m.status === "cancelling" ? "Cancelling" : m.status === "queued" ? "Waiting" : "Moving"} />{" "}
        to {to}: {m.phase}
        {p !== null && ` · ${p}% (${formatBytes(m.bytes_done)} of ${formatBytes(m.bytes_total)})`}
      </span>
    )
  }
  if (m.status === "failed") {
    return (
      <span className="mt-1 block">
        <Lamp tone="bad" word="Last move failed" /> ({to}): {m.error} The VM stayed where it was.
      </span>
    )
  }
  if (m.status === "cancelled") {
    return <span className="mt-1 block"><Lamp tone="off" word="Last move cancelled" />; the VM stayed where it was.</span>
  }
  if (m.status === "completed" && m.note) {
    return <span className="mt-1 block"><Lamp tone="busy" word="Moved, with a problem" /> to {to}: {m.note}</span>
  }
  return null
}

// VMNodeRow is the "Node" row of a VM's settings: where it runs, its move in
// progress, and the way to move it.
export function VMNodeRow({ vmId, hostname, nodeId, nodeName, mayMove }: {
  vmId: string; hostname: string; nodeId: string; nodeName: string; mayMove: boolean
}) {
  const { data: nodes } = useNodes(mayMove)
  const { data: move } = useVMMigration(vmId, mayMove)
  const cancel = useCancelMigration(vmId)
  const confirm = useConfirm()
  const [open, setOpen] = useState(false)
  const nameOf = (id: string | null) => nodes?.find((n) => n.id === id)?.name ?? "another node"
  const active = migrationActive(move)

  const cancelMove = async () => {
    const ok = await confirm({
      title: `Cancel moving ${hostname}?`,
      description: "The copy is stopped and what was copied to the other node is removed. The VM stays where it is, running or stopped as it was.",
      confirmLabel: "Cancel move",
      cancelLabel: "Keep moving",
    })
    if (!ok) return
    try {
      await cancel.mutateAsync()
      toast.success("Cancelling the move")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 px-6 py-4">
      <div className="min-w-0 flex-1 basis-64">
        <p className="text-sm font-medium text-foreground">Node</p>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Runs on <span className="font-medium text-foreground">{nodeName}</span>.
          {move && <MoveState m={move} nodeName={nameOf} />}
        </p>
      </div>
      {mayMove && (
        <div className="flex shrink-0 flex-wrap gap-2">
          {active ? (
            <Button variant="outline" size="sm" onClick={cancelMove} disabled={cancel.isPending || move?.status === "cancelling"}>
              {cancel.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              Cancel move
            </Button>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setOpen(true)}>Move to another node</Button>
          )}
        </div>
      )}
      {open && <MoveDialog vmId={vmId} hostname={hostname} nodeId={nodeId} onClose={() => setOpen(false)} />}
    </div>
  )
}

function Summary({ chk }: { chk: MigrationCheck }) {
  const size = chk.copy_bytes > 0 ? formatBytes(chk.copy_bytes) : `${chk.disk_gb} GB`
  return (
    <div className="space-y-2 text-sm">
      <p>
        To <span className="font-medium">{chk.dest_node_name || "—"}</span>
        {chk.dest_pool_name && <>, storage pool <span className="font-medium">{chk.dest_pool_name}</span> <span className="font-mono text-xs">{chk.dest_pool_path}</span></>}.
      </p>
      {chk.mode === "live" && !chk.copy_free && (
        <p className="text-muted-foreground">
          Moves live: the VM keeps running while its memory and about {size} of disk are copied, then pauses for a moment
          as it switches to the new node. Its addresses stay the same.
        </p>
      )}
      {chk.mode === "offline" && !chk.copy_free && (
        <p className="text-muted-foreground">
          Moves offline: the VM is stopped, so its disks (about {size}) are copied and it stays stopped on the new node
          until it is started.
        </p>
      )}
      {chk.copy_free && (
        <p className="text-muted-foreground">
          {chk.mode === "live"
            ? "Moves live without copying a disk: only its memory is copied, then it pauses for a moment as it switches to the new node."
            : "Moves offline without copying a disk: it is defined on the new node and stays stopped until it is started."}{" "}
          Its disks are on shared storage both nodes use, and stay where they are.
        </p>
      )}
      {!chk.copy_free && (chk.shared_disks?.length ?? 0) > 0 && (
        <p className="text-muted-foreground">
          {chk.shared_disks?.join(", ")} stay on shared storage both nodes use; only the other disks are copied.
        </p>
      )}
      {chk.cpu_mode === "host-passthrough" && chk.mode === "live" && chk.can_move && (
        <p className="text-muted-foreground">The VM uses its host&apos;s own CPU; the new node has the same CPU, so it can move live.</p>
      )}
      <p className="text-muted-foreground">
        Nothing is removed from {chk.source_node_name} until the VM is running on the new node. If anything fails, the VM stays where it is.
      </p>
    </div>
  )
}

// MoveDialog checks a move as the operator picks the destination, and starts it.
function MoveDialog({ vmId, hostname, nodeId, onClose }: { vmId: string; hostname: string; nodeId: string; onClose: () => void }) {
  const { data: nodes } = useNodes()
  const { data: pools } = useStoragePools()
  const [dest, setDest] = useState("")
  const [pool, setPool] = useState("")
  const [bandwidth, setBandwidth] = useState("")
  const check = useMigrationCheck(vmId, { dest_node_id: dest, dest_pool_id: pool }, true)
  const start = useStartMigration(vmId)
  const chk = check.data
  const others = (nodes ?? []).filter((n) => n.id !== nodeId)
  // VMs move only into directory pools.
  const destPools = (pools ?? []).filter((p) => p.node_id === dest && (!p.type || p.type === "dir" || p.type === "file"))
  const mbps = bandwidth.trim() === "" ? 0 : Number(bandwidth)
  const bandwidthBad = !Number.isInteger(mbps) || mbps < 0 || mbps > 100000

  const go = async () => {
    try {
      await start.mutateAsync({ dest_node_id: chk?.dest_node_id, dest_pool_id: pool || undefined, bandwidth_mbps: mbps || undefined })
      toast.success(`Moving ${hostname}; progress shows on this page`)
      onClose()
    } catch (err) {
      toast.error((err as Error).message)
      check.refetch()
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Move {hostname} to another node</DialogTitle>
          <DialogDescription>
            The VM&apos;s disks are copied to the new node, and its network rules, console and limits are set up there.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1">
            <label htmlFor="move-node" className="text-sm font-medium">Destination node</label>
            <select id="move-node" className="w-full rounded-md border bg-background px-3 py-2 text-sm" value={dest}
              onChange={(e) => { setDest(e.target.value); setPool("") }}>
              <option value="">The node it fits best</option>
              {others.map((n) => (
                <option key={n.id} value={n.id}>{n.name}{n.status !== "active" ? ` (${n.status})` : n.creation_paused ? " (paused for new VMs)" : ""}</option>
              ))}
            </select>
          </div>
          {dest && (
            <div className="space-y-1">
              <label htmlFor="move-pool" className="text-sm font-medium">Storage pool</label>
              <select id="move-pool" className="w-full rounded-md border bg-background px-3 py-2 text-sm" value={pool} onChange={(e) => setPool(e.target.value)}>
                <option value="">A directory pool the panel picks</option>
                {destPools.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.path})</option>)}
              </select>
            </div>
          )}
          {chk?.mode === "live" && (
            <div className="space-y-1">
              <label htmlFor="move-bw" className="text-sm font-medium">Bandwidth cap (Mbps)</label>
              <Input id="move-bw" inputMode="numeric" placeholder="No cap" value={bandwidth} onChange={(e) => setBandwidth(e.target.value)} />
              <p className="text-xs text-muted-foreground">Limits the copy so the nodes&apos; other traffic keeps its share. Blank copies as fast as the link allows.</p>
              {bandwidthBad && <p className="text-xs text-red-700 dark:text-red-400">Enter a whole number of Mbps, up to 100000.</p>}
            </div>
          )}

          <div className="rounded-md border p-3">
            {check.isFetching ? (
              <p className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Checking with both nodes…</p>
            ) : check.error ? (
              <p className="text-sm text-red-700 dark:text-red-400">The move could not be checked: {(check.error as Error).message}</p>
            ) : chk ? (
              <div className="space-y-3">
                {chk.blockers.length > 0 ? (
                  <div className="text-sm">
                    <Lamp tone="bad" word="Cannot move" />
                    <ul className="mt-1 list-disc space-y-1 pl-5">{chk.blockers.map((b) => <li key={b}>{b}</li>)}</ul>
                  </div>
                ) : (
                  <p className="text-sm"><Lamp tone="ok" word="Ready to move" /></p>
                )}
                {chk.can_move && <Summary chk={chk} />}
                {chk.warnings.length > 0 && (
                  <div className="text-sm">
                    <Lamp tone="busy" word="Note" />
                    <ul className="mt-1 list-disc space-y-1 pl-5 text-muted-foreground">{chk.warnings.map((w) => <li key={w}>{w}</li>)}</ul>
                  </div>
                )}
              </div>
            ) : null}
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Close</Button>
          <Button onClick={go} disabled={!chk?.can_move || check.isFetching || start.isPending || bandwidthBad}>
            {start.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
            Move
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
