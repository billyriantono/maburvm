"use client"

import { useState } from "react"
import { Check, Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { useVMDisks, useAttachDisk, useDetachDisk, useResizeDisk, useDiskMoves, useMoveDisk, useCancelDiskMove } from "@/lib/hooks/use-disks"
import { useStoragePools } from "@/lib/hooks/use-storage"
import type { VMDiskMove } from "@/types/storage"

interface DiskRow {
  device: string
  sizeGB: number
  path?: string
  system: boolean
}

function moveLabel(m: VMDiskMove): string {
  if (m.status === "running" || m.status === "queued") {
    const pct = m.total_bytes > 0 ? ` ${Math.floor((m.done_bytes / m.total_bytes) * 100)}%` : ""
    return `${m.phase || "Moving"}${pct}`
  }
  if (m.status === "completed") return "Moved"
  if (m.status === "cancelled") return "Cancelled"
  return "Failed"
}

// VMDisksCard lists a VM's system disk and its extra disks, grows them (while
// the VM runs too), moves them between the pools of its node, and adds and
// deletes extra disks. Staff only.
export function VMDisksCard({ vmId, nodeId, systemDiskGB, running, canMove }: {
  vmId: string
  nodeId: string
  systemDiskGB: number
  running: boolean
  canMove: boolean
}) {
  const confirm = useConfirm()
  const { data: disks, isLoading, error, refetch } = useVMDisks(vmId)
  const attach = useAttachDisk(vmId)
  const detach = useDetachDisk(vmId)
  const resize = useResizeDisk(vmId)
  const { data: moves } = useDiskMoves(vmId, canMove)
  const move = useMoveDisk(vmId)
  const cancelMove = useCancelDiskMove(vmId)
  const { data: pools } = useStoragePools()
  const [adding, setAdding] = useState(false)
  const [sizeGB, setSizeGB] = useState("10")
  const [err, setErr] = useState("")
  const [growing, setGrowing] = useState<DiskRow | null>(null)
  const [growTo, setGrowTo] = useState("")
  const [moving, setMoving] = useState<DiskRow | null>(null)
  const [destPool, setDestPool] = useState("")

  // Where a disk can be moved to: a disk is not moved into or out of an RBD pool.
  const nodePools = (pools ?? []).filter((p) => p.node_id === nodeId && p.status === "online" && p.type !== "rbd")
  const active = (moves ?? []).find((m) => m.status === "queued" || m.status === "running")
  const latest = (moves ?? [])[0]
  const rows: DiskRow[] = [
    { device: "vda", sizeGB: systemDiskGB, system: true },
    ...(disks ?? []).map((d) => ({ device: d.device, sizeGB: d.size_gb, path: d.path, system: false })),
  ]

  const handleAttach = async () => {
    const gb = parseInt(sizeGB, 10)
    if (Number.isNaN(gb) || gb <= 0) { setErr("Enter a size in GB"); return }
    setErr("")
    try {
      await attach.mutateAsync(gb)
      setAdding(false)
      setSizeGB("10")
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  const handleDetach = async (device: string) => {
    await confirm({
      title: `Detach and delete ${device}?`,
      description: "The disk is removed from this VM and deleted, with everything on it. This cannot be undone. Copy anything you need off it first.",
      confirmLabel: "Detach and delete",
      destructive: true,
      action: () => detach.mutateAsync({ device, deleteVolume: true }),
    })
  }

  const handleGrow = async () => {
    if (!growing) return
    const gb = parseInt(growTo, 10)
    if (Number.isNaN(gb) || gb <= growing.sizeGB) {
      toast.error(`Enter more than ${growing.sizeGB} GB: disks only grow`)
      return
    }
    try {
      const res = await resize.mutateAsync({ device: growing.device, sizeGB: gb })
      toast.success(res.queued ? `${growing.device} will grow to ${gb} GB` : `${growing.device} is now ${gb} GB${res.live ? "; the running VM sees it already" : ""}`)
      toast.info(res.note, { duration: 12000 })
      setGrowing(null)
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  const handleMove = async () => {
    if (!moving || !destPool) return
    try {
      await move.mutateAsync({ device: moving.device, destPoolId: destPool })
      toast.success(running
        ? `${moving.device} is being copied while the VM runs; it switches over when the copy is done`
        : `${moving.device} is being copied; the VM cannot start until the copy is done`)
      setMoving(null)
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6">
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h2 className="text-lg font-semibold text-foreground">Disks</h2>
        <Button size="sm" variant="outline" onClick={() => setAdding((v) => !v)}><Plus className="w-4 h-4" />Add Disk</Button>
      </div>

      {adding && (
        <div className="flex flex-wrap items-end gap-2 mb-4 p-3 border rounded-md bg-muted">
          <div>
            <label htmlFor="disk-size" className="text-xs font-medium text-muted-foreground block mb-1">Size (GB)</label>
            <Input id="disk-size" type="number" min={1} value={sizeGB} onChange={(e) => setSizeGB(e.target.value)} className="border w-32 font-mono" />
          </div>
          <Button size="sm" onClick={handleAttach} disabled={attach.isPending}>
            {attach.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Check className="w-4 h-4" />}Attach
          </Button>
          <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setErr("") }}>Cancel</Button>
        </div>
      )}
      {err && <p className="text-xs text-destructive font-medium mb-3">{err}</p>}

      {active && (
        <div className="flex flex-wrap items-center justify-between gap-2 mb-4 p-3 border rounded-md" aria-live="polite">
          <span className="text-sm">
            Moving {active.target}{active.live ? " while the VM runs" : ""}: <span className="tabular-nums">{moveLabel(active)}</span>
          </span>
          <Button size="sm" variant="outline" onClick={() => cancelMove.mutate()} disabled={cancelMove.isPending}>Cancel move</Button>
        </div>
      )}
      {!active && latest && (latest.status === "failed" || latest.note) && (
        <p className="text-xs text-muted-foreground mb-4">
          Last move of {latest.target}: {moveLabel(latest)}. {latest.error} {latest.note}
        </p>
      )}

      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading disks</p>
      ) : error ? (
        <p className="text-sm text-destructive">{(error as Error).message} <button className="underline" onClick={() => refetch()}>Retry</button></p>
      ) : (
        <div className="border overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted text-muted-foreground border-b font-medium text-xs">
                <th className="text-left p-3">Device</th>
                <th className="text-left p-3">Size</th>
                <th className="text-left p-3">Path</th>
                <th className="text-right p-3">Action</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => (
                <tr key={d.device} className="border-t">
                  <td className="p-3 font-mono font-medium">{d.device}{d.system && <span className="ml-2 font-sans text-xs text-muted-foreground">system</span>}</td>
                  <td className="p-3 tabular-nums">{d.sizeGB} GB</td>
                  <td className="p-3 font-mono text-xs truncate max-w-[280px]" title={d.path}>{d.path ?? ""}</td>
                  <td className="p-3">
                    <div className="flex flex-wrap justify-end gap-2">
                      <Button size="sm" variant="outline" onClick={() => { setGrowing(d); setGrowTo(String(d.sizeGB + 10)) }} disabled={!!active}>Grow</Button>
                      {canMove && <Button size="sm" variant="outline" onClick={() => { setMoving(d); setDestPool("") }} disabled={!!active}>Move</Button>}
                      {!d.system && (
                        <Button size="sm" variant="destructive" onClick={() => handleDetach(d.device)} disabled={!!active || (detach.isPending && detach.variables?.device === d.device)}>
                          {detach.isPending && detach.variables?.device === d.device ? <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" /> : <Trash2 className="w-4 h-4" />}Detach
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="text-xs text-muted-foreground mt-3">
        Disks hot-plug and grow on running VMs. After adding one, format and mount it inside the guest; after growing one, grow its partition and filesystem.
      </p>

      <Dialog open={!!growing} onOpenChange={(o) => { if (!o) setGrowing(null) }}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>Grow {growing?.device}</DialogTitle>
            <DialogDescription>Disks only grow. {running ? "The running VM sees the new size at once." : ""}</DialogDescription>
          </DialogHeader>
          <label htmlFor="grow-to" className="text-sm font-medium">New size (GB)</label>
          <Input id="grow-to" type="number" min={(growing?.sizeGB ?? 0) + 1} value={growTo} onChange={(e) => setGrowTo(e.target.value)} className="font-mono w-40" />
          <p className="text-xs text-muted-foreground">Inside the VM, grow the partition and filesystem afterwards (growpart and resize2fs or xfs_growfs on Linux, Disk Management on Windows).</p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setGrowing(null)}>Cancel</Button>
            <Button onClick={handleGrow} disabled={resize.isPending}>{resize.isPending && <Loader2 className="w-4 h-4 animate-spin" />}Grow</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!moving} onOpenChange={(o) => { if (!o) setMoving(null) }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Move {moving?.device} to another pool</DialogTitle>
            <DialogDescription>
              {running
                ? "The disk is copied while the VM runs, then the VM switches to the copy and the old disk is removed."
                : "The disk is copied while the VM is off, then the old disk is removed. The VM cannot start until the copy is done."}
            </DialogDescription>
          </DialogHeader>
          <label htmlFor="move-pool" className="text-sm font-medium">Storage pool on this node</label>
          <select id="move-pool" value={destPool} onChange={(e) => setDestPool(e.target.value)}
            className="h-10 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2">
            <option value="">Choose a pool</option>
            {nodePools.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} ({p.type === "dir" ? p.path : `${p.type} ${p.path}`}){p.tier ? `, ${p.tier}` : ""}, {Math.floor(p.available_space / 1073741824)} GB free
              </option>
            ))}
          </select>
          <p className="text-xs text-muted-foreground">Needs as much free space there as the disk&apos;s size. A VM with snapshots cannot move its disks.</p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setMoving(null)}>Cancel</Button>
            <Button onClick={handleMove} disabled={!destPool || move.isPending}>{move.isPending && <Loader2 className="w-4 h-4 animate-spin" />}Move disk</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
