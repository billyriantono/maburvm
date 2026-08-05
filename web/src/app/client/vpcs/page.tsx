"use client"

import { useState } from "react"
import { Loader2, Network, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"
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
import { useCreateVPC, useDeleteVPC, useVPCs } from "@/lib/hooks/use-vpcs"
import type { VPC } from "@/types"

// Customers define their own private networks and choose their own address
// range. Another customer may be using the very same range — each network gets
// its own router on the node — so the only ranges that must not overlap are the
// customer's own. That is what the error from the API says when it happens.
export default function ClientVPCsPage() {
  const { data: vpcs, isLoading } = useVPCs()
  const createVPC = useCreateVPC()
  const deleteVPC = useDeleteVPC()

  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [subnet, setSubnet] = useState("10.0.0.0/24")

  const handleCreate = async () => {
    try {
      const vpc = await createVPC.mutateAsync({ name: name.trim(), subnet: subnet.trim() })
      toast.success(`Private network "${vpc.name}" created on ${vpc.subnet}`)
      setOpen(false)
      setName("")
      setSubnet("10.0.0.0/24")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const handleDelete = async (vpc: VPC) => {
    if (!window.confirm(`Delete "${vpc.name}" (${vpc.subnet})?`)) return
    try {
      await deleteVPC.mutateAsync(vpc.id)
      toast.success(`"${vpc.name}" deleted`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Private Networks</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Your own address ranges. VMs in the same network reach each other privately and are not
            exposed to the internet until you attach a floating IP.
          </p>
        </div>
        <Button type="button" onClick={() => setOpen(true)} className="gap-1">
          <Plus className="w-4 h-4" />
          New network
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
        </div>
      ) : !vpcs?.length ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <Network className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">No private networks yet</p>
          <p className="text-sm text-muted-foreground mt-1">
            Create one, then order a VM into it.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-4">Name</div>
            <div className="col-span-3">Address range</div>
            <div className="col-span-3">Gateway</div>
            <div className="col-span-2 text-right">Actions</div>
          </div>
          {vpcs.map((vpc) => (
            <div key={vpc.id} className="grid grid-cols-12 gap-3 items-center p-4 border-b last:border-0">
              <div className="col-span-4 font-medium truncate">{vpc.name}</div>
              <div className="col-span-3 font-mono text-sm">{vpc.subnet}</div>
              <div className="col-span-3 font-mono text-sm text-muted-foreground">{vpc.gateway}</div>
              <div className="col-span-2 flex justify-end">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title="Delete (only when no VMs are in it)"
                  disabled={deleteVPC.isPending}
                  onClick={() => handleDelete(vpc)}
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
            <DialogTitle>New private network</DialogTitle>
            <DialogDescription>
              Pick any private range you like. It only has to avoid overlapping your own other
              networks — other customers are unaffected by your choice.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="vpc-name">Name</Label>
              <Input
                id="vpc-name"
                placeholder="production"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="vpc-subnet">Address range</Label>
              <Input
                id="vpc-subnet"
                placeholder="10.0.0.0/24"
                value={subnet}
                onChange={(e) => setSubnet(e.target.value)}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">
                Private ranges only: 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16. Use /29 or larger.
                The first address becomes the gateway.
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
              disabled={!name.trim() || !subnet.trim() || createVPC.isPending}
            >
              {createVPC.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
