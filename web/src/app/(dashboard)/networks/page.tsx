"use client"

import { useState } from "react"
import { 
  Plus, 
  Search,
  Zap,
  Trash2,
  Settings,
  CheckCircle2,
  Wifi,
  WifiOff,
  Loader2,
  AlertCircle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useNetworks } from "@/lib/hooks/use-networks"
import { toast } from "sonner"

function formatBandwidth(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i]
}

function formatDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString("en-US", { 
    year: "numeric", 
    month: "short", 
    day: "numeric" 
  })
}

function ConfirmDialog({ 
  open, 
  title, 
  message, 
  onConfirm, 
  onCancel 
}: { 
  open: boolean
  title: string
  message: string
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
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black">
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            Confirm Delete
          </Button>
        </div>
      </div>
    </div>
  )
}

export default function NetworksPage() {
  const { data: networks, isLoading, error } = useNetworks()
  const [searchQuery, setSearchQuery] = useState("")
  const [deleteConfirm, setDeleteConfirm] = useState<NonNullable<typeof networks>[number] | null>(null)
  
  const filteredNetworks = networks?.filter(network => {
    if (!searchQuery) return true
    const query = searchQuery.toLowerCase()
    return (
      network.id.toLowerCase().includes(query) ||
      network.vm_id?.toLowerCase().includes(query) ||
      network.ip_address?.toLowerCase().includes(query)
    )
  }) ?? []
  
  const totalNetworks = networks?.length ?? 0
  const networksWithVlan = networks?.filter(n => n.vlan_id).length ?? 0
  const networksWithBandwidth = networks?.filter(n => n.bandwidth_limit && n.bandwidth_limit > 0).length ?? 0
  
  const handleDelete = () => {
    if (!deleteConfirm) return
    toast.success(`Network "${deleteConfirm.vm_id || deleteConfirm.id}" deleted`)
    setDeleteConfirm(null)
  }
  
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2">
              <Zap className="w-8 h-8" />
              Networks
            </h1>
          </div>
        </div>
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
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2">
              <Zap className="w-8 h-8" />
              Networks
            </h1>
          </div>
        </div>
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
            {totalNetworks} network interfaces
          </p>
        </div>
        <Button className="gap-2" onClick={() => toast.info("Network creation coming soon")}>
          <Plus className="w-4 h-4" />
          Create Network
        </Button>
      </div>
      
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">Total Networks</p>
              <p className="text-3xl font-black text-black mt-1">{totalNetworks}</p>
            </div>
            <Wifi className="w-8 h-8 text-gray-400" />
          </div>
        </div>
        
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">With VLANs</p>
              <p className="text-3xl font-black text-success mt-1">{networksWithVlan}</p>
            </div>
            <CheckCircle2 className="w-8 h-8 text-success" />
          </div>
        </div>
        
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">With Bandwidth Limit</p>
              <p className="text-3xl font-black text-black mt-1">{networksWithBandwidth}</p>
            </div>
            <Wifi className="w-8 h-8 text-primary" />
          </div>
        </div>
        
        <div className="border-4 border-black shadow-neo p-4 bg-white">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-xs font-bold uppercase text-gray-500 tracking-wider">Created Today</p>
              <p className="text-3xl font-black text-secondary mt-1">
                {networks?.filter(n => {
                  const created = new Date(n.created_at)
                  const today = new Date()
                  return created.toDateString() === today.toDateString()
                }).length ?? 0}
              </p>
            </div>
            <WifiOff className="w-8 h-8 text-secondary" />
          </div>
        </div>
      </div>
      
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input
              type="text"
              placeholder="Search by ID, VM, or IP..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10 border-2 border-black"
            />
          </div>
        </div>
      </div>
      
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
          <div className="col-span-2">VM ID</div>
          <div className="col-span-2">IP Address</div>
          <div className="col-span-2">VLAN ID</div>
          <div className="col-span-2">Bandwidth Limit</div>
          <div className="col-span-2">Created</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>
        
        {filteredNetworks.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-gray-500 font-bold uppercase">No networks found</p>
          </div>
        ) : (
          filteredNetworks.map((network, index) => (
            <div 
              key={network.id} 
              className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${
                index % 2 === 0 ? "bg-white" : "bg-gray-50"
              }`}
            >
              <div className="col-span-2">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                    <Wifi className="w-5 h-5" />
                  </div>
                  <span className="font-black text-black">{network.vm_id || network.id}</span>
                </div>
              </div>
              
              <div className="col-span-2">
                <span className="font-mono text-sm font-bold">{network.ip_address || 'N/A'}</span>
              </div>
              
              <div className="col-span-2">
                {network.vlan_id ? (
                  <span className="inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black bg-accent text-white">
                    VLAN {network.vlan_id}
                  </span>
                ) : (
                  <span className="text-gray-400 text-sm">-</span>
                )}
              </div>
              
              <div className="col-span-2">
                <span className="font-mono text-sm text-gray-600">
                  {network.bandwidth_limit ? formatBandwidth(network.bandwidth_limit) + '/s' : 'Unlimited'}
                </span>
              </div>
              
              <div className="col-span-2">
                <span className="font-mono text-xs">{formatDate(network.created_at)}</span>
              </div>
              
              <div className="col-span-2 flex items-center justify-end gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  className="gap-1"
                  title="Configure"
                  onClick={() => toast.info(`Configuration for ${network.vm_id || network.id} coming soon`)}
                >
                  <Settings className="w-4 h-4" />
                  <span className="hidden sm:inline">Configure</span>
                </Button>
                
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirm(network)}
                  className="h-8 w-8 p-0 border-2 border-black hover:bg-danger hover:text-white"
                  title="Delete"
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
        message={`Are you sure you want to delete network "${deleteConfirm?.vm_id || deleteConfirm?.id}"? This action cannot be undone.`}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />
    </div>
  )
}