"use client"

import { useState } from "react"
import Link from "next/link"
import { ArrowLeft, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useNodes } from "@/lib/hooks/use-nodes"
import { useOrphanDisks, useDeleteOrphanDisk } from "@/lib/hooks/use-storage"
import type { OrphanDisk } from "@/types/storage"

function formatBytes(bytes: number): string {
  if (!bytes) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  return `${parseFloat((bytes / Math.pow(1024, i)).toFixed(1))} ${units[i]}`
}

// Disk files and logical volumes on a node that no VM there and no panel
// record uses, with what each looks like, for an operator to delete on purpose.
export default function OrphanDisksPage() {
  const { data: nodes } = useNodes()
  const [nodeId, setNodeId] = useState("")
  const [scanned, setScanned] = useState(false)
  const { data: disks, isFetching, error, refetch } = useOrphanDisks(nodeId, scanned)
  const del = useDeleteOrphanDisk()
  const [target, setTarget] = useState<OrphanDisk | null>(null)
  const [typed, setTyped] = useState("")
  const [foreign, setForeign] = useState(false)

  const total = (disks ?? []).reduce((n, d) => n + d.size_bytes, 0)

  const scan = () => {
    if (!nodeId) return
    if (scanned) refetch()
    else setScanned(true)
  }

  const confirmDelete = async () => {
    if (!target) return
    try {
      await del.mutateAsync({ disk: target, allowForeign: foreign })
      toast.success(`${target.path} deleted`)
      setTarget(null)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="max-w-6xl mx-auto">
      <Link href="/storage" className="inline-flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground mb-3">
        <ArrowLeft className="w-4 h-4" />Back to storage
      </Link>
      <h1 className="text-2xl font-semibold tracking-tight">Orphaned disks</h1>
      <p className="text-muted-foreground text-sm mt-1 mb-6 max-w-3xl">
        Disk files and logical volumes in a node&apos;s registered pools that no VM on the node uses and no panel record names:
        what failed creates, restores, moves and removed VMs left behind. Each is checked again, here and on the node, before
        it is deleted. Files written in the last six hours are not listed.
      </p>

      <div className="flex flex-wrap items-end gap-3 mb-6">
        <div>
          <label htmlFor="orphan-node" className="block text-sm font-medium mb-1">Node</label>
          <select id="orphan-node" value={nodeId} onChange={(e) => { setNodeId(e.target.value); setScanned(false) }}
            className="h-10 min-w-56 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2">
            <option value="">Choose a node</option>
            {(nodes ?? []).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </select>
        </div>
        <Button onClick={scan} disabled={!nodeId || isFetching}>
          {isFetching && <Loader2 className="w-4 h-4 animate-spin" />}Look for orphaned disks
        </Button>
      </div>

      {error ? (
        <p role="alert" className="text-sm text-destructive">{(error as Error).message}</p>
      ) : !scanned || !disks ? null : disks.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing orphaned on this node.</p>
      ) : (
        <>
          <p className="text-sm mb-3 tabular-nums">{disks.length} found, {formatBytes(total)} in all</p>
          <div className="border rounded-lg overflow-x-auto bg-card">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted text-muted-foreground text-xs font-medium">
                  <th className="text-left p-3">Path</th>
                  <th className="text-left p-3">What it looks like</th>
                  <th className="text-left p-3">VM</th>
                  <th className="text-right p-3">Size</th>
                  <th className="text-right p-3">Action</th>
                </tr>
              </thead>
              <tbody>
                {disks.map((d) => (
                  <tr key={d.path} className="border-t">
                    <td className="p-3 font-mono text-xs break-all">{d.path}</td>
                    <td className="p-3">
                      {d.reason}
                      {!d.maburvm && <span className="block text-xs text-amber-700 dark:text-amber-300">Not made by MaburVM</span>}
                      {d.modified_unix > 0 && <span className="block text-xs text-muted-foreground">Last written {new Date(d.modified_unix * 1000).toLocaleString()}</span>}
                    </td>
                    <td className="p-3 text-xs">
                      {d.vm_hostname ? <>{d.vm_hostname}{d.vm_deleted ? " (deleted)" : ""}</> : d.vm_id ? <span className="font-mono">{d.vm_id}</span> : ""}
                    </td>
                    <td className="p-3 text-right tabular-nums">{formatBytes(d.size_bytes)}</td>
                    <td className="p-3 text-right">
                      <Button size="sm" variant="destructive" onClick={() => { setTarget(d); setTyped(""); setForeign(false) }}>Delete</Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      <Dialog open={!!target} onOpenChange={(o) => { if (!o) setTarget(null) }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Delete this disk?</DialogTitle>
            <DialogDescription>
              Everything on it is gone for good. It is deleted only if it is still unused and has not changed since it was listed.
            </DialogDescription>
          </DialogHeader>
          <p className="font-mono text-xs break-all">{target?.path}</p>
          <p className="text-sm">{target?.reason}, {formatBytes(target?.size_bytes ?? 0)}</p>
          <label htmlFor="orphan-confirm" className="text-sm font-medium">Type the path to confirm</label>
          <Input id="orphan-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} className="font-mono" spellCheck={false} />
          {target && !target.maburvm && (
            <label className="flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-1" checked={foreign} onChange={(e) => setForeign(e.target.checked)} />
              <span>MaburVM did not make this disk. Delete it anyway.</span>
            </label>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setTarget(null)}>Cancel</Button>
            <Button variant="destructive" onClick={confirmDelete}
              disabled={del.isPending || typed !== target?.path || (!!target && !target.maburvm && !foreign)}>
              {del.isPending && <Loader2 className="w-4 h-4 animate-spin" />}Delete disk
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
