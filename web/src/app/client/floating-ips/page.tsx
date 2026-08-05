"use client"

import { useState } from "react"
import { Link2, Link2Off, Loader2, Network, Plus } from "lucide-react"
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
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  useAttachFloatingIP,
  useDetachFloatingIP,
  useFloatingIPBilling,
  useFloatingIPs,
  useOrderFloatingIP,
} from "@/lib/hooks/use-floating-ips"
import { useVMs } from "@/lib/hooks/use-vms"
import type { IPAddress } from "@/types"

// Client-facing floating IPs. Deliberately narrower than the admin page: a
// customer can move an address they already hold between their own VMs, but
// cannot allocate a new one from a pool or release one back — those consume the
// node's public address space and stay with an administrator. NAT mode is not
// exposed either; the panel picks the only one that can work.
export default function ClientFloatingIPsPage() {
  const { data: floatingIPs, isLoading } = useFloatingIPs()
  const { data: vmsData } = useVMs({ pageSize: 100 })
  const attach = useAttachFloatingIP()
  const detach = useDetachFloatingIP()
  const order = useOrderFloatingIP()
  const { data: billing } = useFloatingIPBilling()

  const [target, setTarget] = useState<IPAddress | null>(null)
  const [vmID, setVmID] = useState("")

  const vms = vmsData?.data ?? []
  const vmName = (id?: string) => {
    if (!id) return "—"
    return vms.find((vm) => vm.id === id)?.hostname ?? `${id.slice(0, 8)}…`
  }

  const handleAttach = async () => {
    if (!target) return
    try {
      await attach.mutateAsync({ id: target.id, vm_id: vmID })
      toast.success(`${target.address} now points at ${vmName(vmID)}`)
      setTarget(null)
      setVmID("")
    } catch (err) {
      toast.error(`Could not move the address: ${(err as Error).message}`)
    }
  }

  const handleOrder = async () => {
    try {
      const fip = await order.mutateAsync()
      toast.success(`${fip.address} is yours — attach it to a VM to put it to use`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const handleDetach = async (fip: IPAddress) => {
    try {
      await detach.mutateAsync(fip.id)
      toast.success(`${fip.address} detached`)
    } catch (err) {
      toast.error(`Could not detach: ${(err as Error).message}`)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Floating IPs</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Move a public address between your VMs without changing anything inside them. The switch
            takes about a second, and the address stays yours even if you delete the VM it was on.
          </p>
          {billing && billing.total > 0 && (
            <p className="text-sm text-muted-foreground mt-2">
              {billing.free > 0
                ? `1 attached address is included. ${billing.billable} charged.`
                : `${billing.billable} charged — attach one to a VM and it becomes free.`}
            </p>
          )}
        </div>
        <Button type="button" onClick={handleOrder} disabled={order.isPending} className="gap-1 shrink-0">
          {order.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Plus className="w-4 h-4" />}
          Order an IP
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
        </div>
      ) : !floatingIPs?.length ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <Network className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">You have no floating IPs</p>
          <p className="text-sm text-muted-foreground mt-1">
            Order one above, then attach it to a VM.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-4">Address</div>
            <div className="col-span-4">Currently on</div>
            <div className="col-span-4 text-right">Actions</div>
          </div>
          {floatingIPs.map((fip) => (
            <div
              key={fip.id}
              className="grid grid-cols-12 gap-3 items-center p-4 border-b last:border-0"
            >
              <div className="col-span-4 font-mono text-sm">{fip.address}</div>
              <div className="col-span-4 text-sm text-muted-foreground truncate">
                {vmName(fip.vm_id)}
              </div>
              <div className="col-span-4 flex justify-end items-center gap-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="gap-1"
                  onClick={() => {
                    setTarget(fip)
                    setVmID(fip.vm_id ?? "")
                  }}
                >
                  <Link2 className="w-4 h-4" />
                  {fip.vm_id ? "Move" : "Attach"}
                </Button>
                {fip.vm_id && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-8 w-8 p-0"
                    title="Detach"
                    disabled={detach.isPending}
                    onClick={() => handleDetach(fip)}
                  >
                    <Link2Off className="w-4 h-4" />
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={!!target} onOpenChange={(open) => !open && setTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Move {target?.address}</DialogTitle>
            <DialogDescription>
              Pick which of your VMs this address should point at. Nothing needs to change inside
              the VM, and existing connections to your VM&apos;s own address are unaffected.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="cfip-vm">VM</Label>
            <Select value={vmID} onValueChange={setVmID}>
              <SelectTrigger id="cfip-vm">
                <SelectValue placeholder="Select one of your VMs" />
              </SelectTrigger>
              <SelectContent>
                {vms.map((vm) => (
                  <SelectItem key={vm.id} value={vm.id}>
                    {vm.hostname}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setTarget(null)}>
              Cancel
            </Button>
            <Button type="button" onClick={handleAttach} disabled={!vmID || attach.isPending}>
              {attach.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Move"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
