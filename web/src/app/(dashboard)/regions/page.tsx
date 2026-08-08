"use client"

import { useState } from "react"
import { Globe, Loader2, Plus, Server, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  useAssignNodeToRegion,
  useCreateRegion,
  useDeleteRegion,
  useRegions,
  useUpdateRegion,
} from "@/lib/hooks/use-regions"
import { useNodes } from "@/lib/hooks/use-nodes"
import { CountryFlag } from "@/components/country-flag"
import type { Region } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

// Regions are the locations customers choose between when ordering. A region
// holds one or more nodes; today each holds exactly one, which is why VPCs and
// floating IPs — both node-scoped — behave the way customers expect
// region-scoped resources to behave.
export default function RegionsPage() {
  const confirm = useConfirm()
  const { data: regions, isLoading } = useRegions()
  const { data: nodesData } = useNodes()
  const createRegion = useCreateRegion()
  const updateRegion = useUpdateRegion()
  const deleteRegion = useDeleteRegion()
  const assignNode = useAssignNodeToRegion()

  const [open, setOpen] = useState(false)
  const [slug, setSlug] = useState("")
  const [name, setName] = useState("")
  const [country, setCountry] = useState("ID")
  const [assignTarget, setAssignTarget] = useState<Region | null>(null)
  const [nodeId, setNodeId] = useState("")

  const nodes = nodesData ?? []

  const handleCreate = async () => {
    try {
      const r = await createRegion.mutateAsync({ slug: slug.trim(), name: name.trim(), country })
      toast.success(`Region "${r.name}" created`)
      setOpen(false)
      setSlug("")
      setName("")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const handleToggle = async (r: Region) => {
    try {
      await updateRegion.mutateAsync({ id: r.id, enabled: !r.enabled })
      toast.success(r.enabled ? `"${r.name}" hidden from ordering` : `"${r.name}" is orderable`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const handleDelete = async (r: Region) => {
    const ok = await confirm({
      title: `Delete region "${r.name}"?`,
      description:
        "Customers will no longer see it when ordering. Nodes must be moved out of it first.",
      confirmLabel: "Delete region",
      destructive: true,
      details: [
        { label: "Slug", value: r.slug },
        { label: "Active nodes", value: r.node_count },
      ],
    })
    if (!ok) return
    try {
      await deleteRegion.mutateAsync(r.id)
      toast.success(`"${r.name}" deleted`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const handleAssign = async () => {
    if (!assignTarget || !nodeId) return
    try {
      await assignNode.mutateAsync({ regionId: assignTarget.id, nodeId })
      toast.success(`Node moved into ${assignTarget.name}`)
      setAssignTarget(null)
      setNodeId("")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold flex items-center gap-2">
            <Globe className="w-6 h-6" />
            Regions
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            The locations customers choose from when ordering. A region with no active node is
            hidden from them, so nobody picks a place that cannot take the order.
          </p>
        </div>
        <Button type="button" onClick={() => setOpen(true)} className="gap-1">
          <Plus className="w-4 h-4" />
          New region
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
        </div>
      ) : !regions?.length ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <Globe className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">No regions yet</p>
          <p className="text-sm text-muted-foreground mt-1">
            Customers cannot order until at least one region exists and has a node.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-4">Region</div>
            <div className="col-span-2">Slug</div>
            <div className="col-span-2">Active nodes</div>
            <div className="col-span-2">Orderable</div>
            <div className="col-span-2 text-right">Actions</div>
          </div>
          {regions.map((r) => (
            <div key={r.id} className="grid grid-cols-12 gap-3 items-center p-4 border-b last:border-0">
              <div className="col-span-4 font-medium flex items-center gap-2">
                <CountryFlag country={r.country} className="text-xl" />
                {r.name}
              </div>
              <div className="col-span-2 font-mono text-sm text-muted-foreground">{r.slug}</div>
              <div className="col-span-2 text-sm">
                {r.node_count === 0 ? (
                  <span className="text-muted-foreground">none</span>
                ) : (
                  r.node_count
                )}
              </div>
              <div className="col-span-2">
                <Badge variant={r.enabled && r.node_count > 0 ? "success" : "warning"}>
                  {!r.enabled ? "disabled" : r.node_count > 0 ? "yes" : "no capacity"}
                </Badge>
              </div>
              <div className="col-span-2 flex justify-end items-center gap-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-8 w-8 p-0"
                  title="Move a node into this region"
                  onClick={() => setAssignTarget(r)}
                >
                  <Server className="w-4 h-4" />
                </Button>
                <Button type="button" variant="outline" size="sm" onClick={() => handleToggle(r)}>
                  {r.enabled ? "Disable" : "Enable"}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title="Delete (only when no nodes are assigned)"
                  onClick={() => handleDelete(r)}
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New region</DialogTitle>
            <DialogDescription>
              Name it after the city it is really in — not after the node hostname, which can go
              stale when hardware moves.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="rg-name">Name</Label>
              <Input
                id="rg-name"
                placeholder="Jakarta"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="rg-slug">Slug</Label>
              <Input
                id="rg-slug"
                placeholder="jakarta"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">
                Lowercase letters, digits and dashes. Customers may order by this value.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="rg-country">Country</Label>
              <Input
                id="rg-country"
                placeholder="ID"
                maxLength={2}
                value={country}
                onChange={(e) => setCountry(e.target.value.toUpperCase())}
                className="font-mono w-24"
              />
              <p className="text-xs text-muted-foreground">
                Two-letter ISO code; it selects the flag customers see.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              onClick={handleCreate}
              disabled={!name.trim() || !slug.trim() || createRegion.isPending}
            >
              {createRegion.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!assignTarget} onOpenChange={(o) => !o && setAssignTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Move a node into {assignTarget?.name}</DialogTitle>
            <DialogDescription>
              A node belongs to exactly one region. Moving it changes where new orders for that
              region land; machines already on it are unaffected.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="rg-node">Node</Label>
            <Select value={nodeId} onValueChange={setNodeId}>
              <SelectTrigger id="rg-node">
                <SelectValue placeholder="Select a node" />
              </SelectTrigger>
              <SelectContent>
                {nodes.map((n) => (
                  <SelectItem key={n.id} value={n.id}>
                    {n.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setAssignTarget(null)}>
              Cancel
            </Button>
            <Button type="button" onClick={handleAssign} disabled={!nodeId || assignNode.isPending}>
              {assignNode.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Move"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
