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
import { toast } from "sonner"

function formatDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" })
}

function ConfirmDialog({ open, title, message, loading, onConfirm, onCancel }: {
  open: boolean
  title: string
  message: string
  loading?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Confirm dialog">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onCancel} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
        <h3 className="text-xl font-black uppercase mb-4">{title}</h3>
        <p className="text-gray-600 font-medium mb-6">{message}</p>
        <div className="flex gap-3 justify-end">
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black" disabled={loading}>Cancel</Button>
          <Button variant="destructive" onClick={onConfirm} disabled={loading}>
            {loading && <Loader2 className="w-4 h-4 mr-2 animate-spin" />}
            Confirm Delete
          </Button>
        </div>
      </div>
    </div>
  )
}

export default function NetworksPage() {
  const router = useRouter()
  const { data: networks, isLoading, error } = useNetworks()
  const deleteNetwork = useDeleteNetwork()
  const [searchQuery, setSearchQuery] = useState("")
  const [deleteConfirm, setDeleteConfirm] = useState<NonNullable<typeof networks>[number] | null>(null)

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

  const handleDelete = async () => {
    if (!deleteConfirm) return
    try {
      await deleteNetwork.mutateAsync(deleteConfirm.id)
      toast.success(`Network "${deleteConfirm.name}" deleted`)
      setDeleteConfirm(null)
    } catch (err) {
      toast.error(`Failed to delete network: ${(err as Error).message}`)
    }
  }

  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2 mb-6">
          <Zap className="w-8 h-8" />Networks
        </h1>
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
          <span className="ml-3 font-bold uppercase">Loading networks...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2 mb-6">
          <Zap className="w-8 h-8" />Networks
        </h1>
        <div className="bg-danger/10 border-4 border-danger p-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-danger" />
            <div>
              <p className="font-black uppercase">Error loading networks</p>
              <p className="text-sm font-medium">{error.message}</p>
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
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2">
            <Zap className="w-8 h-8" />
            Networks
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            {totalNetworks} managed network{totalNetworks === 1 ? "" : "s"} (bridge / NAT / isolated)
          </p>
        </div>
        <Button className="gap-2" onClick={() => router.push("/networks/new")}>
          <Plus className="w-4 h-4" />
          Create Network
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">Total</p>
              <p className="text-3xl font-black text-black mt-1">{totalNetworks}</p>
            </div>
            <NetworkIcon className="w-8 h-8 text-gray-600" />
          </div>
        </div>
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">Bridge</p>
              <p className="text-3xl font-black text-black mt-1">{bridgeNetworks}</p>
            </div>
            <Wifi className="w-8 h-8 text-primary" />
          </div>
        </div>
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">NAT</p>
              <p className="text-3xl font-black text-secondary mt-1">{natNetworks}</p>
            </div>
            <Wifi className="w-8 h-8 text-secondary" />
          </div>
        </div>
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">With VLAN</p>
              <p className="text-3xl font-black text-success mt-1">{networksWithVlan}</p>
            </div>
            <CheckCircle2 className="w-8 h-8 text-success" />
          </div>
        </div>
      </div>

      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
          <Input
            type="text"
            placeholder="Search by name, type, bridge, or subnet..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10 border-2 border-black"
          />
        </div>
      </div>

      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
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
            <NetworkIcon className="w-16 h-16 text-gray-300 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase">No networks found</p>
          </div>
        ) : (
          filteredNetworks.map((network, index) => (
            <div key={network.id} className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${index % 2 === 0 ? "bg-white" : "bg-gray-50"}`}>
              <div className="col-span-3 min-w-0">
                <span className="font-black text-black truncate block">{network.name}</span>
                <span className="text-[10px] font-mono text-gray-500">{formatDate(network.created_at)}</span>
              </div>
              <div className="col-span-1">
                <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black bg-muted">
                  {network.type}
                </span>
              </div>
              <div className="col-span-2 font-mono text-sm font-bold truncate">{network.bridge || "—"}</div>
              <div className="col-span-2 font-mono text-xs truncate">{network.subnet || "—"}</div>
              <div className="col-span-2 font-mono text-xs truncate">{network.gateway || "—"}</div>
              <div className="col-span-1">
                {network.vlan_id ? (
                  <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase border border-black bg-accent text-white">
                    {network.vlan_id}
                  </span>
                ) : (
                  <span className="text-gray-500 text-sm">—</span>
                )}
              </div>
              <div className="col-span-1 flex justify-end">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirm(network)}
                  className="h-8 w-8 p-0 border-2 border-black hover:bg-danger hover:text-white"
                  title="Delete network"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>

      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete Network"
        message={`Are you sure you want to delete network "${deleteConfirm?.name}"? This cannot be undone.`}
        loading={deleteNetwork.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />
    </div>
  )
}
