"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import {
  Plus,
  Search,
  Zap,
  Trash2,
  CheckCircle2,
  Wifi,
  Network as NetworkIcon,
  Loader2,
  AlertCircle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useNetworks, useDeleteNetwork } from "@/lib/hooks/use-networks"
import { useConfirm } from "@/components/confirm-provider"
import { toast } from "sonner"

function formatDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" })
}
export default function NetworksPage() {
  const confirm = useConfirm()
  const router = useRouter()
  const { data: networks, isLoading, error } = useNetworks()
  const deleteNetwork = useDeleteNetwork()
  const [searchQuery, setSearchQuery] = useState("")

  const filteredNetworks = networks?.filter((n) => {
    if (!searchQuery) return true
    const q = searchQuery.toLowerCase()
    return (
      n.name.toLowerCase().includes(q) ||
      n.type.toLowerCase().includes(q) ||
      n.bridge?.toLowerCase().includes(q) ||
      n.subnet?.toLowerCase().includes(q)
    )
  }) ?? []

  const totalNetworks = networks?.length ?? 0
  const networksWithVlan = networks?.filter((n) => n.vlan_id).length ?? 0
  const natNetworks = networks?.filter((n) => n.type === "nat").length ?? 0
  const bridgeNetworks = networks?.filter((n) => n.type === "bridge").length ?? 0

  const handleDelete = async (network: { id: string; name: string }) => {
    const ok = await confirm({
      title: `Delete network "${network.name}"?`,
      description:
        "The managed network is removed from its node. VMs still attached to it lose that connection.",
      confirmLabel: "Delete network",
      destructive: true,
    })
    if (!ok) return
    try {
      await deleteNetwork.mutateAsync(network.id)
      toast.success(`Network "${network.name}" deleted`)
    } catch (err) {
      toast.error(`Failed to delete network: ${(err as Error).message}`)
    }
  }

  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2 mb-6">
          <Zap className="w-6 h-6" />Networks
        </h1>
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
          <span className="ml-3 text-muted-foreground">Loading networks...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2 mb-6">
          <Zap className="w-6 h-6" />Networks
        </h1>
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-destructive" />
            <div>
              <p className="font-semibold">Error loading networks</p>
              <p className="text-sm text-muted-foreground">{error.message}</p>
            </div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2">
            <Zap className="w-6 h-6" />
            Networks
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            {totalNetworks} managed network{totalNetworks === 1 ? "" : "s"} (bridge / NAT / isolated)
          </p>
        </div>
        <Button className="gap-2" onClick={() => router.push("/networks/new")}>
          <Plus className="w-4 h-4" />
          Create Network
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="rounded-lg border shadow-sm p-4 bg-card">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-medium text-muted-foreground">Total</p>
              <p className="text-2xl font-semibold text-foreground mt-1">{totalNetworks}</p>
            </div>
            <NetworkIcon className="w-8 h-8 text-muted-foreground" />
          </div>
        </div>
        <div className="rounded-lg border shadow-sm p-4 bg-card">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-medium text-muted-foreground">Bridge</p>
              <p className="text-2xl font-semibold text-foreground mt-1">{bridgeNetworks}</p>
            </div>
            <Wifi className="w-8 h-8 text-muted-foreground" />
          </div>
        </div>
        <div className="rounded-lg border shadow-sm p-4 bg-card">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-medium text-muted-foreground">NAT</p>
              <p className="text-2xl font-semibold text-foreground mt-1">{natNetworks}</p>
            </div>
            <Wifi className="w-8 h-8 text-muted-foreground" />
          </div>
        </div>
        <div className="rounded-lg border shadow-sm p-4 bg-card">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-medium text-muted-foreground">With VLAN</p>
              <p className="text-2xl font-semibold text-foreground mt-1">{networksWithVlan}</p>
            </div>
            <CheckCircle2 className="w-8 h-8 text-emerald-600" />
          </div>
        </div>
      </div>

      <div className="rounded-lg border bg-card p-4 shadow-sm mb-6">
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
          <Input
            type="text"
            placeholder="Search by name, type, bridge, or subnet..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10"
          />
        </div>
      </div>

      <div className="rounded-lg border bg-card shadow-sm overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-muted text-muted-foreground font-medium text-xs">
          <div className="col-span-3">Name</div>
          <div className="col-span-1">Type</div>
          <div className="col-span-2">Bridge</div>
          <div className="col-span-2">Subnet</div>
          <div className="col-span-2">Gateway</div>
          <div className="col-span-1">VLAN</div>
          <div className="col-span-1 text-right">·</div>
        </div>

        {filteredNetworks.length === 0 ? (
          <div className="p-12 text-center">
            <NetworkIcon className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
            <p className="text-muted-foreground font-medium">No networks found</p>
          </div>
        ) : (
          filteredNetworks.map((network) => (
            <div key={network.id} className="grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0 hover:bg-muted/50">
              <div className="col-span-3 min-w-0">
                <span className="font-medium text-foreground truncate block">{network.name}</span>
                <span className="text-[10px] font-mono text-muted-foreground">{formatDate(network.created_at)}</span>
              </div>
              <div className="col-span-1">
                <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-medium border rounded-md bg-muted text-muted-foreground">
                  {network.type}
                </span>
              </div>
              <div className="col-span-2 font-mono text-sm truncate">{network.bridge || "—"}</div>
              <div className="col-span-2 font-mono text-xs truncate">{network.subnet || "—"}</div>
              <div className="col-span-2 font-mono text-xs truncate">{network.gateway || "—"}</div>
              <div className="col-span-1">
                {network.vlan_id ? (
                  <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-medium border rounded-md bg-muted text-muted-foreground">
                    {network.vlan_id}
                  </span>
                ) : (
                  <span className="text-muted-foreground text-sm">—</span>
                )}
              </div>
              <div className="col-span-1 flex justify-end">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleDelete(network)}
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title="Delete network"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>

    </div>
  )
}
