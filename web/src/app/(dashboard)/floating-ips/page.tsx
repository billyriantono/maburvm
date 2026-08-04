"use client"

import { useState } from "react"
import { AlertCircle, Link2, Link2Off, Loader2, Network, Plus, RefreshCw, Trash2 } from "lucide-react"
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
  useAllocateFloatingIP,
  useAttachFloatingIP,
  useDetachFloatingIP,
  useFloatingIPs,
  useReleaseFloatingIP,
} from "@/lib/hooks/use-floating-ips"
import { useIPPools } from "@/lib/hooks/use-ipam"
import { useUsers } from "@/lib/hooks/use-users"
import { useVMs } from "@/lib/hooks/use-vms"
import type { IPAddress, NATMode } from "@/types"

export default function FloatingIPsPage() {
  const { data: floatingIPs, isLoading, error, refetch } = useFloatingIPs()
  const { data: pools } = useIPPools()
  const { data: vmsData } = useVMs({ pageSize: 100 })
  const { data: usersData } = useUsers({ pageSize: 100 })
  const allocate = useAllocateFloatingIP()
  const attach = useAttachFloatingIP()
  const detach = useDetachFloatingIP()
  const release = useReleaseFloatingIP()

  const [allocateOpen, setAllocateOpen] = useState(false)
  const [poolId, setPoolId] = useState("")
  const [requestedIP, setRequestedIP] = useState("")
  const [ownerId, setOwnerId] = useState("")
  const [attachTarget, setAttachTarget] = useState<IPAddress | null>(null)
  const [attachVMID, setAttachVMID] = useState("")
  const [natMode, setNatMode] = useState<NATMode | "auto">("auto")

  const vms = vmsData?.data ?? []
  const vmName = (vmId?: string) => {
    if (!vmId) return "—"
    return vms.find((vm) => vm.id === vmId)?.hostname ?? `${vmId.slice(0, 8)}…`
  }

  const handleAllocate = async () => {
    try {
      const fip = await allocate.mutateAsync({
        pool_id: poolId,
        requested_ip: requestedIP.trim() || undefined,
        user_id: ownerId || undefined,
      })
      toast.success(`Floating IP ${fip.address} allocated`)
      setAllocateOpen(false)
      setRequestedIP("")
      setOwnerId("")
    } catch (err) {
      toast.error(`Allocation failed: ${(err as Error).message}`)
    }
  }

  const handleAttach = async () => {
    if (!attachTarget) return
    try {
      await attach.mutateAsync({
        id: attachTarget.id,
        vm_id: attachVMID,
        nat_mode: natMode === "auto" ? undefined : natMode,
      })
      toast.success(`${attachTarget.address} now points at ${vmName(attachVMID)}`)
      setAttachTarget(null)
      setAttachVMID("")
      setNatMode("auto")
    } catch (err) {
      toast.error(`Attach failed: ${(err as Error).message}`)
    }
  }

  const handleDetach = async (fip: IPAddress) => {
    try {
      await detach.mutateAsync(fip.id)
      toast.success(`${fip.address} detached`)
    } catch (err) {
      toast.error(`Detach failed: ${(err as Error).message}`)
    }
  }

  const handleRelease = async (fip: IPAddress) => {
    if (!window.confirm(`Release ${fip.address} back to its pool? It becomes allocatable to any VM.`)) return
    try {
      await release.mutateAsync(fip.id)
      toast.success(`${fip.address} released`)
    } catch (err) {
      toast.error(`Release failed: ${(err as Error).message}`)
    }
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2">
            <Network className="w-6 h-6" />
            Floating IPs
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            Addresses that live on the node and are NATed to a VM — move one between VMs in seconds,
            and it survives the VM being deleted. Floating IPs stay on the node they were allocated from.
          </p>
        </div>
        <Button type="button" onClick={() => setAllocateOpen(true)} className="gap-1">
          <Plus className="w-4 h-4" />
          Allocate
        </Button>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6 shadow-sm mb-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-destructive" />
            <div className="flex-1">
              <p className="font-semibold">Error loading floating IPs</p>
              <p className="text-sm text-muted-foreground">{(error as Error).message}</p>
            </div>
            <Button variant="outline" size="sm" onClick={() => refetch()} className="gap-1">
              <RefreshCw className="w-4 h-4" />
              Retry
            </Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
          <span className="ml-3 text-muted-foreground">Loading floating IPs...</span>
        </div>
      ) : !floatingIPs?.length ? (
        <div className="rounded-lg border bg-card p-12 shadow-sm text-center">
          <Network className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">No floating IPs yet</p>
          <p className="text-sm text-muted-foreground mt-1">
            Allocate one from an IP pool, then attach it to any VM on that pool&apos;s node.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card shadow-sm overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-3">Address</div>
            <div className="col-span-2">Status</div>
            <div className="col-span-2">NAT mode</div>
            <div className="col-span-2">Attached to</div>
            <div className="col-span-3 text-right">Actions</div>
          </div>
          {floatingIPs.map((fip) => (
            <div key={fip.id} className="grid grid-cols-12 gap-3 items-center p-4 border-b last:border-0">
              <div className="col-span-3 font-mono text-sm text-foreground">{fip.address}</div>
              <div className="col-span-2">
                <Badge variant={fip.vm_id ? "success" : "warning"}>
                  {fip.vm_id ? "attached" : "available"}
                </Badge>
              </div>
              <div className="col-span-2 text-sm text-muted-foreground">
                {fip.nat_mode ? (
                  <span title={fip.nat_mode === "full" ? "Inbound plus outbound: the VM also egresses as this address" : "Inbound only; the VM keeps its own outbound address"}>
                    {fip.nat_mode}
                  </span>
                ) : (
                  "—"
                )}
              </div>
              <div className="col-span-2 text-sm text-muted-foreground truncate">{vmName(fip.vm_id)}</div>
              <div className="col-span-3 flex justify-end items-center gap-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="gap-1"
                  onClick={() => {
                    setAttachTarget(fip)
                    setAttachVMID(fip.vm_id ?? "")
                    setNatMode((fip.nat_mode as NATMode) ?? "auto")
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
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title={fip.vm_id ? "Detach before releasing" : "Release back to pool"}
                  disabled={!!fip.vm_id || release.isPending}
                  onClick={() => handleRelease(fip)}
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={allocateOpen} onOpenChange={setAllocateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Allocate floating IP</DialogTitle>
            <DialogDescription>
              Takes an address out of a pool and reserves it for you. It is only attachable to VMs on
              that pool&apos;s node.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="fip-pool">IP pool</Label>
              <Select value={poolId} onValueChange={setPoolId}>
                <SelectTrigger id="fip-pool">
                  <SelectValue placeholder="Select a pool" />
                </SelectTrigger>
                <SelectContent>
                  {pools?.map((pool) => (
                    <SelectItem key={pool.id} value={pool.id}>
                      {pool.name} {pool.cidr ? `(${pool.cidr})` : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="fip-owner">Owner</Label>
              <Select value={ownerId} onValueChange={setOwnerId}>
                <SelectTrigger id="fip-owner">
                  <SelectValue placeholder="Yourself" />
                </SelectTrigger>
                <SelectContent>
                  {(usersData?.data ?? []).map((u) => (
                    <SelectItem key={u.id} value={u.id}>
                      {u.email}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                The customer this address belongs to. They can then move it between their own VMs
                from their portal; only you can allocate or release it.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="fip-address">Specific address (optional)</Label>
              <Input
                id="fip-address"
                placeholder="leave empty for the next free address"
                value={requestedIP}
                onChange={(e) => setRequestedIP(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setAllocateOpen(false)}>
              Cancel
            </Button>
            <Button type="button" onClick={handleAllocate} disabled={!poolId || allocate.isPending}>
              {allocate.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Allocate"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!attachTarget} onOpenChange={(open) => !open && setAttachTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Attach {attachTarget?.address}</DialogTitle>
            <DialogDescription>
              Traffic to this address is forwarded to the VM. Moving it to another VM takes effect
              within seconds and needs no change inside either guest.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="fip-vm">VM</Label>
              <Select value={attachVMID} onValueChange={setAttachVMID}>
                <SelectTrigger id="fip-vm">
                  <SelectValue placeholder="Select a VM" />
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
            <div className="space-y-2">
              <Label htmlFor="fip-nat">NAT mode</Label>
              <Select value={natMode} onValueChange={(v) => setNatMode(v as NATMode | "auto")}>
                <SelectTrigger id="fip-nat">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">Automatic (recommended)</SelectItem>
                  <SelectItem value="inbound">Inbound only — VM keeps its own outbound IP</SelectItem>
                  <SelectItem value="full">Full 1:1 — private-network VMs only</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                Leave this on Automatic unless you know otherwise. Full 1:1 also rewrites the VM&apos;s
                outbound address, which only works for a VM on a private address routed through the
                node — a VM with its own public IP sends traffic straight to the upstream gateway, so
                the node never sees it and the request is rejected.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setAttachTarget(null)}>
              Cancel
            </Button>
            <Button type="button" onClick={handleAttach} disabled={!attachVMID || attach.isPending}>
              {attach.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Attach"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
