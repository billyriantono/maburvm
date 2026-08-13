"use client"

import { useState, useEffect, useCallback, useMemo } from "react"
import Link from "next/link"
import { 
  Play, 
  Square, 
  RotateCcw, 
  Terminal, 
  Trash2, 
  Search,
  Plus,
  ChevronLeft,
  ChevronRight,
  X,
  Loader2,
  AlertCircle,
  Server
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useVMs, useDeleteVM, useVMActions, useVMStatusStream, useVMOperation } from "@/lib/hooks/use-vms"
import { useNodes } from "@/lib/hooks/use-nodes"
import type { VM, VMNodeStatus, VMStatus } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

// Status badge component
function StatusBadge({ status }: { status: VMStatus }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900",
    stopped: "bg-muted text-muted-foreground border",
    suspended: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
    creating: "bg-sky-50 text-sky-700 border-sky-200 dark:bg-sky-950 dark:text-sky-300 dark:border-sky-900",
    deleting: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
    error: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900",
  }

  return (
    <span className={`inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium border capitalize ${colors[status] || "bg-muted text-muted-foreground border"}`}>
      <span className={`w-1.5 h-1.5 mr-1.5 rounded-full ${status === "running" ? "bg-emerald-500 animate-pulse" : "bg-current"}`} />
      {status}
    </span>
  )
}

function NodeBadge({ name, status, fallback }: { name?: string; status?: VMNodeStatus; fallback?: string }) {
  const label = name || fallback || "Unknown"
  const state = status || ""
  const styles: Record<string, string> = {
    active: "bg-muted text-foreground border",
    maintenance: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900",
    offline: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900",
    "": "bg-muted text-muted-foreground border",
  }

  return (
    <div className="flex flex-col items-start gap-1">
      <span className={`inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium border ${styles[state] || styles[""]}`}>
        {label}
      </span>
      {state && state !== "active" && (
        <span className="text-[10px] font-semibold text-destructive">
          Node {state}
        </span>
      )}
    </div>
  )
}

// Pagination component
function Pagination({ 
  currentPage, 
  totalPages, 
  onPageChange 
}: { 
  currentPage: number
  totalPages: number
  onPageChange: (page: number) => void
}) {
  const pages = []
  const maxVisible = 5
  
  let start = Math.max(1, currentPage - Math.floor(maxVisible / 2))
  let end = Math.min(totalPages, start + maxVisible - 1)
  
  if (end - start < maxVisible - 1) {
    start = Math.max(1, end - maxVisible + 1)
  }
  
  for (let i = start; i <= end; i++) {
    pages.push(i)
  }
  
  return (
    <div className="flex items-center gap-1">
      <Button
        variant="ghost"
        size="sm"
        onClick={() => onPageChange(currentPage - 1)}
        disabled={currentPage === 1}
        className="border"
      >
        <ChevronLeft className="w-4 h-4" />
      </Button>
      
      {start > 1 && (
        <>
          <Button variant="ghost" size="sm" onClick={() => onPageChange(1)} className="border">
            1
          </Button>
          {start > 2 && <span className="px-2">...</span>}
        </>
      )}
      
      {pages.map(page => (
        <Button
          key={page}
          variant={page === currentPage ? "default" : "ghost"}
          size="sm"
          onClick={() => onPageChange(page)}
          className="border min-w-[40px]"
        >
          {page}
        </Button>
      ))}
      
      {end < totalPages && (
        <>
          {end < totalPages - 1 && <span className="px-2">...</span>}
          <Button variant="ghost" size="sm" onClick={() => onPageChange(totalPages)} className="border">
            {totalPages}
          </Button>
        </>
      )}
      
      <Button
        variant="ghost"
        size="sm"
        onClick={() => onPageChange(currentPage + 1)}
        disabled={currentPage === totalPages}
        className="border"
      >
        <ChevronRight className="w-4 h-4" />
      </Button>
    </div>
  )
}

// Skeleton row for loading state
function SkeletonRow() {
  return (
    <div className="grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0">
      <div className="col-span-3">
        <Skeleton className="h-5 w-32" />
        <Skeleton className="h-3 w-24 mt-1" />
      </div>
      <div className="col-span-2">
        <Skeleton className="h-6 w-16" />
      </div>
      <div className="col-span-2">
        <Skeleton className="h-6 w-20" />
      </div>
      <div className="col-span-2">
        <div className="flex items-center gap-3">
          <Skeleton className="h-6 w-6" />
          <Skeleton className="h-6 w-6" />
        </div>
      </div>
      <div className="col-span-3 flex items-center justify-end gap-2">
        <Skeleton className="h-8 w-8" />
        <Skeleton className="h-8 w-8" />
        <Skeleton className="h-8 w-8" />
        <Skeleton className="h-8 w-8" />
        <Skeleton className="h-8 w-8" />
      </div>
    </div>
  )
}

function DeleteProgressDialog({ vm, onClose }: { vm: { id: string; hostname: string } | null; onClose: () => void }) {
  const { data: op } = useVMOperation(vm?.id ?? null, !!vm)
  if (!vm) return null

  const total = op?.total_steps || 3
  const step = op?.current_step || 0
  const done = op?.status === "completed"
  const failed = op?.status === "failed"
  const pct = done ? 100 : Math.round((Math.max(step - (op?.status === "running" ? 1 : 0), 0) / total) * 100)
  const finished = done || failed
  const label = failed
    ? "Deletion failed"
    : done
      ? "VM deleted"
      : op?.step_label || "Starting…"

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Delete progress">
      <div className="absolute inset-0 bg-black/50" />
      <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-md w-full mx-4">
        <h3 className="text-lg font-semibold mb-1">Deleting {vm.hostname}</h3>
        <p className="text-sm font-medium mb-4 flex items-center gap-2">
          {!finished && <Loader2 className="w-4 h-4 animate-spin" />}
          <span className={failed ? "text-destructive" : done ? "text-emerald-600" : ""}>{label}</span>
          {!failed && <span className="text-muted-foreground">· step {Math.min(step, total)}/{total}</span>}
        </p>

        <div className="w-full h-2 rounded-full border bg-muted overflow-hidden mb-4">
          <div
            className={`h-full transition-all duration-500 ${failed ? "bg-destructive" : done ? "bg-emerald-500" : "bg-primary"}`}
            style={{ width: `${failed ? 100 : pct}%` }}
          />
        </div>

        {failed && op?.error && (
          <p className="text-xs font-mono text-destructive border border-red-200 bg-red-50 rounded-md dark:bg-red-950 dark:border-red-900 p-2 mb-4 break-words">{op.error}</p>
        )}
        {failed && (
          <p className="text-sm text-muted-foreground mb-4">The VM was not fully removed. It&apos;s marked as error — you can retry the delete.</p>
        )}

        <div className="flex justify-end">
          <Button onClick={onClose} disabled={!finished} className="border">
            {finished ? "Close" : "Working…"}
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
    <div className={`fixed bottom-4 right-4 z-50 px-4 py-3 border rounded-lg shadow-md bg-background ${
      type === "success" ? "text-emerald-700 dark:text-emerald-300" : "text-destructive"
    }`}>
      <p className="font-medium text-sm">{message}</p>
    </div>
  )
}

// Error state component
function ErrorState({ message, onRetry }: { message: string, onRetry: () => void }) {
  return (
    <div className="p-12 text-center">
      <AlertCircle className="w-12 h-12 mx-auto text-destructive mb-4" />
      <p className="text-foreground font-semibold mb-2">Failed to load VMs</p>
      <p className="text-muted-foreground text-sm mb-4">{message}</p>
      <Button onClick={onRetry} className="gap-2">
        <RotateCcw className="w-4 h-4" />
        Retry
      </Button>
    </div>
  )
}

// Empty state component
function EmptyState({ hasFilters, onClearFilters }: { hasFilters: boolean, onClearFilters: () => void }) {
  return (
    <div className="p-12 text-center">
      <Server className="w-12 h-12 mx-auto text-muted-foreground mb-4" />
      <p className="text-foreground font-semibold mb-2">No VMs found</p>
      <p className="text-muted-foreground text-sm mb-4">
        {hasFilters ? "Try adjusting your filters to see more results." : "Get started by creating your first VM."}
      </p>
      {hasFilters ? (
        <Button variant="ghost" onClick={onClearFilters} className="border gap-2">
          <X className="w-4 h-4" />
          Clear filters
        </Button>
      ) : (
        <Link href="/vms/new">
          <Button className="gap-2">
            <Plus className="w-4 h-4" />
            Create VM
          </Button>
        </Link>
      )}
    </div>
  )
}

export default function VMListPage() {
  const confirm = useConfirm()
  // State
  const [currentPage, setCurrentPage] = useState(1)
  const [searchQuery, setSearchQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [nodeFilter, setNodeFilter] = useState<string>("")
  const [debouncedSearch, setDebouncedSearch] = useState("")
  // VM whose deletion is in progress — drives the step-by-step progress dialog.
  const [deletingVM, setDeletingVM] = useState<{ id: string; hostname: string } | null>(null)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  
  const itemsPerPage = 10

  // Live-update VM statuses via SSE (pushes refreshes the moment status changes).
  useVMStatusStream()

  // Debounce search query
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(searchQuery)
      setCurrentPage(1)
    }, 300)
    return () => clearTimeout(timer)
  }, [searchQuery])
  
  // Reset to page 1 when status filter changes
  useEffect(() => {
    setCurrentPage(1)
  }, [statusFilter, nodeFilter])
  
  // Fetch VMs with pagination
  const { data, isLoading, error, refetch } = useVMs({
    page: currentPage,
    pageSize: itemsPerPage,
    status: statusFilter || undefined,
    nodeId: nodeFilter || undefined,
    search: debouncedSearch || undefined,
  })
  
  const { data: nodes } = useNodes()
  const nodeStatusByID = useMemo(() => {
    const map = new Map<string, VMNodeStatus>()
    for (const node of nodes || []) {
      map.set(node.id, node.status)
    }
    return map
  }, [nodes])
  
  const deleteVM = useDeleteVM()
  const vmActions = useVMActions()
  
  // The API does the searching. Filtering here instead would only ever look at
  // the ten rows this page happens to hold, so a VM on any other page reads as
  // "No VMs found" — beside a total that still says 65.
  const filteredVMs = data?.data ?? []
  
  // Calculate total pages
  const totalPages = data?.totalPages || 1
  const totalItems = data?.total || 0
  
  // Clear all filters
  const clearFilters = () => {
    setSearchQuery("")
    setStatusFilter("")
    setNodeFilter("")
    setCurrentPage(1)
  }
  
  const hasFilters = Boolean(searchQuery || statusFilter || nodeFilter)
  
  // Action handlers
  const handleAction = useCallback(async (vm: VM, action: "start" | "stop" | "restart" | "console") => {
    if (action === "console") {
      // Open console in new window/tab
      window.open(`/vms/${vm.id}/console`, "_blank")
      return
    }
    
    setActionLoading(`${vm.id}-${action}`)
    
    try {
      await vmActions.mutateAsync({ vmId: vm.id, action })
      setToast({ message: `VM ${vm.hostname} ${action} successful`, type: "success" })
      refetch()
    } catch (err) {
      setToast({ message: `Failed to ${action} VM: ${(err as Error).message}`, type: "error" })
    } finally {
      setActionLoading(null)
    }
  }, [vmActions, refetch])
  
  const handleDelete = useCallback(async (vm: VM) => {
    const ok = await confirm({
      title: `Delete "${vm.hostname}"?`,
      description:
        "The machine and its disks are destroyed and its addresses go back to the pool. This cannot be undone.",
      confirmLabel: "Delete VM",
      destructive: true,
      details: [
        { label: "Status", value: vm.status },
        { label: "Location", value: vm.region_name ?? vm.node_name ?? "—" },
      ],
      action: () => deleteVM.mutateAsync(vm.id),
    })
    if (!ok) return
    // Deletion is async + multi-step; the dialog only covers accepting the
    // request, so hand over to the progress dialog that tracks the rest.
    setDeletingVM({ id: vm.id, hostname: vm.hostname })
  }, [confirm, deleteVM])
  
  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">
            Virtual Machines
          </h1>
          <div className="flex items-center gap-2 mt-1">
            <p className="text-muted-foreground text-sm">
              {isLoading ? "Loading..." : `${totalItems} VMs`}
            </p>
          </div>
        </div>
        <Link href="/vms/new">
          <Button className="gap-2">
            <Plus className="w-4 h-4" />
            Create VM
          </Button>
        </Link>
      </div>
      
      {/* Filters */}
      <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          {/* Search */}
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input
              type="text"
              placeholder="Search by hostname or ID..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10 h-10"
            />
          </div>

          {/* Status Filter */}
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <option value="">All Status</option>
            <option value="running">Running</option>
            <option value="stopped">Stopped</option>
            <option value="suspended">Suspended</option>
            <option value="creating">Creating</option>
            <option value="error">Error</option>
          </select>
          
          {/* Node Filter */}
          <select
            value={nodeFilter}
            onChange={(e) => setNodeFilter(e.target.value)}
            className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <option value="">All Nodes</option>
            {nodes?.map((node) => (
              <option key={node.id} value={node.id}>{node.name}</option>
            ))}
          </select>
          
          {/* Clear filters */}
          {hasFilters && (
            <Button variant="ghost" onClick={clearFilters} className="border gap-1">
              <X className="w-4 h-4" />
              Clear
            </Button>
          )}
        </div>
      </div>
      
      {/* Data Table */}
      <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
        {/* Table Header */}
        <div className="grid grid-cols-12 gap-4 px-4 py-3 bg-muted text-muted-foreground border-b font-medium text-xs">
          <div className="col-span-3">Hostname</div>
          <div className="col-span-2">Node</div>
          <div className="col-span-2">Status</div>
          <div className="col-span-2">Resources</div>
          <div className="col-span-3 text-right">Actions</div>
        </div>
        
        {/* Table Body */}
        {isLoading ? (
          // Loading skeleton
          <>
            <SkeletonRow />
            <SkeletonRow />
            <SkeletonRow />
            <SkeletonRow />
            <SkeletonRow />
          </>
        ) : error ? (
          <ErrorState message={(error as Error).message} onRetry={() => refetch()} />
        ) : filteredVMs.length === 0 ? (
          <EmptyState hasFilters={hasFilters} onClearFilters={clearFilters} />
        ) : (
          filteredVMs.map((vm) => {
            const effectiveNodeStatus = vm.node_status || nodeStatusByID.get(vm.node_id) || ""
            const nodeUnavailable = Boolean(effectiveNodeStatus && effectiveNodeStatus !== "active")

            return (
            <div 
              key={vm.id} 
              className={`grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0 transition-colors ${
                nodeUnavailable ? "bg-red-50 dark:bg-red-950/30" : "hover:bg-muted/50"
              }`}
            >
              <div className="col-span-3 flex flex-col justify-center">
                <Link href={`/vms/${vm.id}`} className="font-medium text-foreground hover:underline w-fit">
                  {vm.hostname}
                </Link>
                <p className="text-xs text-muted-foreground">ID: {vm.id.slice(0, 8)}</p>
              </div>
              <div className="col-span-2">
                <NodeBadge name={vm.node_name} status={effectiveNodeStatus} fallback={vm.node_id?.slice(0, 8)} />
              </div>
              <div className="col-span-2">
                <StatusBadge status={vm.status} />
              </div>
              <div className="col-span-2">
                <div className="flex items-center gap-3">
                  <div className="flex items-center gap-1">
                    <span className="w-6 h-6 bg-muted text-foreground flex items-center justify-center rounded-md border text-xs font-medium">
                      {vm.resources.cpu}
                    </span>
                    <span className="text-xs text-muted-foreground">CPU</span>
                  </div>
                  <div className="flex items-center gap-1">
                    <span className="w-6 h-6 bg-muted text-foreground flex items-center justify-center rounded-md border text-xs font-medium">
                      {Math.round(vm.resources.ram / 1024)}
                    </span>
                    <span className="text-xs text-muted-foreground">GB</span>
                  </div>
                </div>
              </div>
              <div className="col-span-3 flex items-center justify-end gap-2">
                {/* Start */}
                <Button
                  variant="success"
                  size="sm"
                  onClick={() => handleAction(vm, "start")}
                  disabled={nodeUnavailable || vm.status === "running" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title={nodeUnavailable ? "Node is not active" : "Start"}
                >
                  {actionLoading === `${vm.id}-start` ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : (
                    <Play className="w-4 h-4" />
                  )}
                </Button>
                
                {/* Stop */}
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => handleAction(vm, "stop")}
                  disabled={nodeUnavailable || vm.status === "stopped" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title={nodeUnavailable ? "Node is not active" : "Stop"}
                >
                  {actionLoading === `${vm.id}-stop` ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : (
                    <Square className="w-4 h-4" />
                  )}
                </Button>
                
                {/* Restart */}
                <Button
                  variant="warning"
                  size="sm"
                  onClick={() => handleAction(vm, "restart")}
                  disabled={nodeUnavailable || vm.status !== "running" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title={nodeUnavailable ? "Node is not active" : "Restart"}
                >
                  {actionLoading === `${vm.id}-restart` ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : (
                    <RotateCcw className="w-4 h-4" />
                  )}
                </Button>
                
                {/* Console */}
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => handleAction(vm, "console")}
                  disabled={nodeUnavailable || vm.status !== "running" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title={nodeUnavailable ? "Node is not active" : "Console"}
                >
                  <Terminal className="w-4 h-4" />
                </Button>
                
                {/* Delete */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleDelete(vm)}
                  disabled={!!actionLoading}
                  className="h-8 w-8 p-0 border hover:bg-destructive hover:text-destructive-foreground"
                  title="Delete"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
            )
          })
        )}
      </div>
      
      {/* Pagination */}
      {!isLoading && !error && totalPages > 1 && (
        <div className="flex items-center justify-between mt-6">
          <p className="text-sm text-muted-foreground">
            Showing {(currentPage - 1) * itemsPerPage + 1} to {Math.min(currentPage * itemsPerPage, totalItems)} of {totalItems} VMs
          </p>
          <Pagination
            currentPage={currentPage}
            totalPages={totalPages}
            onPageChange={setCurrentPage}
          />
        </div>
      )}
      
      {/* Delete Confirmation Dialog */}

      {/* Delete Progress (step-by-step) */}
      <DeleteProgressDialog
        vm={deletingVM}
        onClose={() => { setDeletingVM(null); refetch() }}
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
