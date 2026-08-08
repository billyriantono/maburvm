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
  AlertTriangle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useNodes, useDeleteNode } from "@/lib/hooks/use-nodes"
import type { NodeStatus } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

// Status indicator component
function StatusIndicator({ status }: { status: NodeStatus }) {
  const config: Record<string, { bg: string; text: string; label: string }> = {
    active: { bg: "bg-emerald-500", text: "text-emerald-600", label: "Active" },
    offline: { bg: "bg-red-500", text: "text-red-600", label: "Offline" },
    maintenance: { bg: "bg-amber-500", text: "text-amber-600", label: "Maintenance" },
  }

  const { bg, text, label } = config[status] || config.offline

  return (
    <div className="flex items-center gap-2">
      <span className={`w-2.5 h-2.5 rounded-full ${bg} ${status === "active" ? "animate-pulse" : ""}`} />
      <span className={`text-xs font-medium ${text}`}>{label}</span>
    </div>
  )
}

function Toast({ message, type, onClose }: { message: string, type: "success" | "error", onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 px-4 py-3 rounded-lg border shadow-md ${
      type === "success"
        ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900"
        : "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900"
    }`}>
      <p className="font-medium text-sm">{message}</p>
    </div>
  )
}

function formatDate(dateString: string) {
  return new Date(dateString).toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })
}

export default function NodesListPage() {
  const confirm = useConfirm()
  // State
  const [searchQuery, setSearchQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
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
  const handleDelete = useCallback(async (node: { id: string; name: string }) => {
    const ok = await confirm({
      title: `Delete node "${node.name}"?`,
      description:
        "The panel stops managing this hypervisor. VMs on it keep running, but become unmanageable from here.",
      confirmLabel: "Delete node",
      destructive: true,
      action: () => deleteNode.mutateAsync(node.id),
    })
    if (!ok) return
    setToast({ message: `Node ${node.name} deleted`, type: "success" })
    refetch()
  }, [confirm, deleteNode, refetch])

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
            <h1 className="text-2xl font-semibold text-foreground">Nodes</h1>
            <Skeleton className="h-5 w-48 mt-1" />
          </div>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-24 rounded-lg" />)}
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-48 rounded-lg" />)}
        </div>
      </div>
    )
  }

  // Error state
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-card text-card-foreground border rounded-lg p-12 shadow-sm text-center">
          <AlertTriangle className="w-16 h-16 text-red-500 mx-auto mb-4" />
          <h2 className="text-lg font-semibold mb-2">Failed to load nodes</h2>
          <p className="text-muted-foreground text-sm mb-6">{(error as Error).message}</p>
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
          <h1 className="text-2xl font-semibold text-foreground">
            Nodes
          </h1>
          <p className="text-muted-foreground text-sm">
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
        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Total Nodes</span>
            <Server className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-2xl font-semibold text-foreground">{stats.total}</p>
        </div>

        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Active</span>
            <span className="w-2.5 h-2.5 bg-emerald-500 rounded-full animate-pulse" />
          </div>
          <p className="text-2xl font-semibold text-emerald-600">{stats.active}</p>
        </div>

        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Offline</span>
            <span className="w-2.5 h-2.5 bg-red-500 rounded-full" />
          </div>
          <p className="text-2xl font-semibold text-red-600">{stats.offline}</p>
        </div>

        <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Maintenance</span>
            <AlertTriangle className="w-4 h-4 text-amber-600" />
          </div>
          <p className="text-2xl font-semibold text-amber-600">{stats.maintenance}</p>
        </div>
      </div>

      {/* Filters */}
      <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          {/* Search */}
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input
              type="text"
              placeholder="Search by name or IP..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10"
            />
          </div>

          {/* Status Filter */}
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <option value="">All Status</option>
            <option value="active">Active</option>
            <option value="offline">Offline</option>
            <option value="maintenance">Maintenance</option>
          </select>

          {/* Clear filters */}
          {hasFilters && (
            <Button variant="outline" onClick={clearFilters} className="gap-1">
              <X className="w-4 h-4" />
              Clear
            </Button>
          )}
        </div>
      </div>

      {/* Nodes Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {filteredNodes.length === 0 ? (
          <div className="col-span-full bg-card text-card-foreground border rounded-lg p-12 shadow-sm text-center">
            <Server className="w-16 h-16 text-muted-foreground mx-auto mb-4" />
            <p className="text-muted-foreground font-medium">No nodes found</p>
            {hasFilters && (
              <Button variant="outline" onClick={clearFilters} className="mt-4">
                Clear filters
              </Button>
            )}
          </div>
        ) : (
          filteredNodes.map((node) => (
            <div key={node.id} className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
              {/* Node Header */}
              <div className="p-4 border-b bg-muted/50">
                <div className="flex items-start justify-between">
                  <div>
                    <Link href={`/nodes/${node.id}`} className="text-lg font-semibold hover:text-primary transition-colors">
                      {node.name}
                    </Link>
                    <p className="text-sm font-mono text-muted-foreground">{node.ip_address}</p>
                  </div>
                  <StatusIndicator status={node.status} />
                </div>
              </div>

              {/* Node Body */}
              <div className="p-4">
                {/* Info */}
                <div className="flex items-center gap-4 mb-4">
                  <div className="flex items-center gap-2">
                    <div className="w-8 h-8 bg-muted text-muted-foreground flex items-center justify-center rounded-md">
                      <Server className="w-4 h-4" />
                    </div>
                    <div>
                      <p className="text-xs font-medium text-muted-foreground">ID</p>
                      <p className="text-xs font-mono">{node.id.slice(0, 12)}</p>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Activity className="w-4 h-4 text-muted-foreground" />
                    <span className="text-xs text-muted-foreground">Created: {formatDate(node.created_at)}</span>
                  </div>
                </div>

                {/* Status Alerts */}
                {node.status === "maintenance" && (
                  <div className="bg-amber-50 text-amber-700 border border-amber-200 rounded-md p-3 mb-4 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900">
                    <p className="text-xs font-medium">Node is in maintenance mode</p>
                  </div>
                )}

                {node.status === "offline" && (
                  <div className="bg-red-50 text-red-700 border border-red-200 rounded-md p-3 mb-4 dark:bg-red-950 dark:text-red-300 dark:border-red-900">
                    <p className="text-xs font-medium">Node is offline</p>
                  </div>
                )}

                {node.status === "active" && (
                  <div className="bg-emerald-50 text-emerald-700 border border-emerald-200 rounded-md p-3 mb-4 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900">
                    <p className="text-xs font-medium">Node is active — view details for metrics</p>
                  </div>
                )}

                {/* Actions */}
                <div className="flex items-center gap-2">
                  <Link href={`/nodes/${node.id}`}>
                    <Button variant="outline" size="sm">
                      <Activity className="w-4 h-4" />
                      <span className="ml-1">Details</span>
                    </Button>
                  </Link>

                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => handleDelete({ id: node.id, name: node.name })}
                    disabled={deleteNode.isPending}
                    className="text-red-600 hover:bg-red-50 hover:text-red-700 dark:hover:bg-red-950"
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
