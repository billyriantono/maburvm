"use client"

import { useState, useMemo, useEffect, useCallback } from "react"
import Link from "next/link"
import { 
  Server, 
  Plus, 
  Search,
  X,
  Trash2,
  Loader2,
  Activity,
  HardDrive,
  Cpu,
  Network,
  AlertTriangle,
  CheckCircle,
  XCircle,
  Database,
  MemoryStick
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useNodes, useDeleteNode } from "@/lib/hooks/use-nodes"
import type { NodeStatus } from "@/types"

// Status indicator component
function StatusIndicator({ status }: { status: NodeStatus }) {
  const config: Record<string, { bg: string; text: string; label: string }> = {
        active: { bg: "bg-success", text: "text-success-dark", label: "Active" },
    offline: { bg: "bg-danger", text: "text-danger", label: "Offline" },
    maintenance: { bg: "bg-warning", text: "text-warning-dark", label: "Maintenance" },
  }
  
  const { bg, text, label } = config[status] || config.offline
  
  return (
    <div className="flex items-center gap-2">
      <span className={`w-3 h-3 rounded-full ${bg} ${status === "active" ? "animate-pulse" : ""}`} />
      <span className={`text-xs font-black uppercase ${text}`}>{label}</span>
    </div>
  )
}

// Confirm dialog component
function ConfirmDialog({ 
  open, 
  title, 
  message, 
  confirmLabel = "Confirm",
  loading = false,
  onConfirm, 
  onCancel 
}: { 
  open: boolean
  title: string
  message: string
  confirmLabel?: string
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
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black" disabled={loading}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={loading}>
            {loading && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}

// Toast notification component
function Toast({ message, type, onClose }: { message: string, type: "success" | "error", onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  
  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

function formatDate(dateString: string) {
  return new Date(dateString).toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })
}

export default function NodesListPage() {
  // State
  const [searchQuery, setSearchQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [deleteConfirm, setDeleteConfirm] = useState<{ id: string; name: string } | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)

  // Data hooks
  const { data: nodes, isLoading, error, refetch } = useNodes()
  const deleteNode = useDeleteNode()

  // Filter nodes
  const filteredNodes = useMemo(() => {
    if (!nodes) return []
    let result = [...nodes]
    
    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(node => 
        node.name.toLowerCase().includes(query) || 
        node.ip_address.toLowerCase().includes(query)
      )
    }
    
    if (statusFilter) {
      result = result.filter(node => node.status === statusFilter)
    }
    
    return result
  }, [nodes, searchQuery, statusFilter])

  // Action handlers
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    
    try {
      await deleteNode.mutateAsync(deleteConfirm.id)
      setToast({ message: `Node ${deleteConfirm.name} deleted`, type: "success" })
      setDeleteConfirm(null)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to delete node: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteConfirm, deleteNode, refetch])

  const clearFilters = () => {
    setSearchQuery("")
    setStatusFilter("")
  }
  
  const hasFilters = searchQuery || statusFilter

  // Calculate stats
  const stats = useMemo(() => ({
    total: nodes?.length || 0,
    active: nodes?.filter(n => n.status === "active").length || 0,
    offline: nodes?.filter(n => n.status === "offline").length || 0,
    maintenance: nodes?.filter(n => n.status === "maintenance").length || 0,
  }), [nodes])

  // Loading state
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black">Nodes</h1>
            <Skeleton className="h-5 w-48 mt-1" />
          </div>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-24 border-4 border-black" />)}
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-48 border-4 border-black" />)}
        </div>
      </div>
    )
  }

  // Error state
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertTriangle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Failed to load nodes</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()} className="gap-2">
            <Activity className="w-4 h-4" />
            Retry
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            Nodes
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
            {stats.total} nodes • {stats.active} active
          </p>
        </div>
        <Link href="/nodes/new">
          <Button className="gap-2">
            <Plus className="w-4 h-4" />
            Add Node
          </Button>
        </Link>
      </div>

      {/* Quick Stats */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total Nodes</span>
            <Server className="w-4 h-4 text-gray-600" />
          </div>
          <p className="text-3xl font-black text-black">{stats.total}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Active</span>
            <span className="w-3 h-3 bg-success rounded-full animate-pulse" />
          </div>
          <p className="text-3xl font-black text-success-dark">{stats.active}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Offline</span>
            <span className="w-3 h-3 bg-danger rounded-full" />
          </div>
          <p className="text-3xl font-black text-danger">{stats.offline}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Maintenance</span>
            <AlertTriangle className="w-4 h-4 text-warning-dark" />
          </div>
          <p className="text-3xl font-black text-warning-dark">{stats.maintenance}</p>
        </div>
      </div>
      
      {/* Filters */}
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          {/* Search */}
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
            <Input
              type="text"
              placeholder="Search by name or IP..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10 border-2 border-black"
            />
          </div>
          
          {/* Status Filter */}
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Status</option>
            <option value="active">Active</option>
            <option value="offline">Offline</option>
            <option value="maintenance">Maintenance</option>
          </select>
          
          {/* Clear filters */}
          {hasFilters && (
            <Button variant="ghost" onClick={clearFilters} className="border-2 border-black gap-1">
              <X className="w-4 h-4" />
              Clear
            </Button>
          )}
        </div>
      </div>
      
      {/* Nodes Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {filteredNodes.length === 0 ? (
          <div className="col-span-full bg-white border-4 border-black p-12 shadow-neo text-center">
            <Server className="w-16 h-16 text-gray-500 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase">No nodes found</p>
            {hasFilters && (
              <Button variant="ghost" onClick={clearFilters} className="mt-4 border-2 border-black">
                Clear filters
              </Button>
            )}
          </div>
        ) : (
          filteredNodes.map((node) => (
            <div key={node.id} className="bg-white border-4 border-black shadow-neo">
              {/* Node Header */}
              <div className="p-4 border-b-4 border-black bg-gray-50">
                <div className="flex items-start justify-between">
                  <div>
                    <Link href={`/nodes/${node.id}`} className="text-xl font-black uppercase hover:text-primary transition-colors">
                      {node.name}
                    </Link>
                    <p className="text-sm font-mono font-medium text-gray-500">{node.ip_address}</p>
                  </div>
                  <StatusIndicator status={node.status} />
                </div>
              </div>
              
              {/* Node Body */}
              <div className="p-4">
                {/* Info */}
                <div className="flex items-center gap-4 mb-4">
                  <div className="flex items-center gap-2">
                    <div className="w-8 h-8 bg-primary flex items-center justify-center border-2 border-black">
                      <Server className="w-4 h-4" />
                    </div>
                    <div>
                      <p className="text-xs font-bold text-gray-500 uppercase">ID</p>
                      <p className="text-xs font-mono">{node.id.slice(0, 12)}</p>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Activity className="w-4 h-4 text-gray-600" />
                    <span className="text-xs font-medium text-gray-500">Created: {formatDate(node.created_at)}</span>
                  </div>
                </div>
                
                {/* Status Alerts */}
                {node.status === "maintenance" && (
                  <div className="bg-warning/20 border-2 border-warning p-3 mb-4">
                    <p className="text-xs font-bold uppercase text-warning-dark">Node is in maintenance mode</p>
                  </div>
                )}
                
                {node.status === "offline" && (
                  <div className="bg-danger/10 border-2 border-danger p-3 mb-4">
                    <p className="text-xs font-bold uppercase text-danger">Node is offline</p>
                  </div>
                )}

                {node.status === "active" && (
                  <div className="bg-success/10 border-2 border-success p-3 mb-4">
                    <p className="text-xs font-bold uppercase text-success-dark">Node is active — view details for metrics</p>
                  </div>
                )}
                
                {/* Actions */}
                <div className="flex items-center gap-2">
                  <Link href={`/nodes/${node.id}`}>
                    <Button variant="ghost" size="sm" className="h-9 border-2 border-black">
                      <Activity className="w-4 h-4" />
                      <span className="ml-1">Details</span>
                    </Button>
                  </Link>
                  
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setDeleteConfirm({ id: node.id, name: node.name })}
                    disabled={deleteNode.isPending}
                    className="h-9 border-2 border-black hover:bg-danger hover:text-white"
                    title="Delete"
                  >
                    <Trash2 className="w-4 h-4" />
                  </Button>
                </div>
              </div>
            </div>
          ))
        )}
      </div>
      
      {/* Delete Confirmation Dialog */}
      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete Node"
        message={`Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`}
        confirmLabel="Delete"
        loading={deleteNode.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />
      
      {/* Toast */}
      {toast && (
        <Toast 
          message={toast.message} 
          type={toast.type} 
          onClose={() => setToast(null)} 
        />
      )}
    </div>
  )
}
