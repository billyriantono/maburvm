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
import { useVMs, useDeleteVM, useVMActions } from "@/lib/hooks/use-vms"
import type { VM, VMStatus } from "@/types"

// Status badge component
function StatusBadge({ status }: { status: VMStatus }) {
  const colors: Record<string, string> = {
    running: "bg-[#CCFF00] text-black",
    stopped: "bg-[#FF4444] text-white",
    suspended: "bg-[#FFAA00] text-black",
    creating: "bg-[#00CCFF] text-black",
    error: "bg-[#FF0000] text-white",
  }
  
  return (
    <span className={`inline-flex items-center px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${colors[status] || "bg-gray-200 text-black"}`}>
      <span className={`w-2 h-2 mr-2 rounded-full ${status === "running" ? "bg-black animate-pulse" : "bg-current"}`} />
      {status}
    </span>
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
        className="border-2 border-black"
      >
        <ChevronLeft className="w-4 h-4" />
      </Button>
      
      {start > 1 && (
        <>
          <Button variant="ghost" size="sm" onClick={() => onPageChange(1)} className="border-2 border-black">
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
          className={`border-2 border-black min-w-[40px] ${page === currentPage ? "bg-black text-white hover:bg-gray-800" : ""}`}
        >
          {page}
        </Button>
      ))}
      
      {end < totalPages && (
        <>
          {end < totalPages - 1 && <span className="px-2">...</span>}
          <Button variant="ghost" size="sm" onClick={() => onPageChange(totalPages)} className="border-2 border-black">
            {totalPages}
          </Button>
        </>
      )}
      
      <Button
        variant="ghost"
        size="sm"
        onClick={() => onPageChange(currentPage + 1)}
        disabled={currentPage === totalPages}
        className="border-2 border-black"
      >
        <ChevronRight className="w-4 h-4" />
      </Button>
    </div>
  )
}

// Skeleton row for loading state
function SkeletonRow() {
  return (
    <div className="grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0">
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

// Confirm dialog component
function ConfirmDialog({ 
  open, 
  title, 
  message, 
  onConfirm, 
  onCancel,
  loading = false
}: { 
  open: boolean
  title: string
  message: string
  onConfirm: () => void
  onCancel: () => void
  loading?: boolean
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
            {loading ? <Loader2 className="w-4 h-4 animate-spin" /> : "Confirm Delete"}
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

// Error state component
function ErrorState({ message, onRetry }: { message: string, onRetry: () => void }) {
  return (
    <div className="p-12 text-center">
      <AlertCircle className="w-12 h-12 mx-auto text-danger mb-4" />
      <p className="text-gray-700 font-bold uppercase mb-2">Failed to load VMs</p>
      <p className="text-gray-500 text-sm mb-4">{message}</p>
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
      <Server className="w-12 h-12 mx-auto text-gray-300 mb-4" />
      <p className="text-gray-700 font-bold uppercase mb-2">No VMs found</p>
      <p className="text-gray-500 text-sm mb-4">
        {hasFilters ? "Try adjusting your filters to see more results." : "Get started by creating your first VM."}
      </p>
      {hasFilters ? (
        <Button variant="ghost" onClick={onClearFilters} className="border-2 border-black gap-2">
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
  // State
  const [currentPage, setCurrentPage] = useState(1)
  const [searchQuery, setSearchQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [debouncedSearch, setDebouncedSearch] = useState("")
  const [deleteConfirm, setDeleteConfirm] = useState<VM | null>(null)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  
  const itemsPerPage = 10
  
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
  }, [statusFilter])
  
  // Fetch VMs with pagination
  const { data, isLoading, error, refetch } = useVMs({
    page: currentPage,
    pageSize: itemsPerPage,
    status: statusFilter || undefined,
  })
  
  const deleteVM = useDeleteVM()
  const vmActions = useVMActions()
  
  // Filter VMs client-side for search
  const filteredVMs = useMemo(() => {
    if (!data?.data) return []
    
    if (!debouncedSearch) return data.data
    
    const query = debouncedSearch.toLowerCase()
    return data.data.filter(vm => 
      vm.hostname.toLowerCase().includes(query) || 
      vm.id.toLowerCase().includes(query)
    )
  }, [data, debouncedSearch])
  
  // Calculate total pages
  const totalPages = data?.totalPages || 1
  const totalItems = data?.total || 0
  
  // Clear all filters
  const clearFilters = () => {
    setSearchQuery("")
    setStatusFilter("")
    setCurrentPage(1)
  }
  
  const hasFilters = Boolean(searchQuery || statusFilter)
  
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
  
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    
    try {
      await deleteVM.mutateAsync(deleteConfirm.id)
      setToast({ message: `VM ${deleteConfirm.hostname} deleted`, type: "success" })
      setDeleteConfirm(null)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to delete VM: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteConfirm, deleteVM, refetch])
  
  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            Virtual Machines
          </h1>
          <div className="flex items-center gap-2 mt-1">
            <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
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
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          {/* Search */}
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input
              type="text"
              placeholder="Search by hostname or ID..."
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
            <option value="running">Running</option>
            <option value="stopped">Stopped</option>
            <option value="suspended">Suspended</option>
            <option value="creating">Creating</option>
            <option value="error">Error</option>
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
      
      {/* Data Table */}
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        {/* Table Header */}
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
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
          filteredVMs.map((vm, index) => (
            <div 
              key={vm.id} 
              className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${
                index % 2 === 0 ? "bg-white" : "bg-gray-50"
              }`}
            >
              <div className="col-span-3 flex flex-col justify-center">
                <Link href={`/vms/${vm.id}`} className="font-black text-black hover:underline w-fit border-none">
                  {vm.hostname}
                </Link>
                <p className="text-xs text-gray-500 font-medium">ID: {vm.id.slice(0, 8)}</p>
              </div>
              <div className="col-span-2">
                <span className="inline-flex items-center px-2 py-1 text-xs font-bold border border-black bg-gray-100">
                  {vm.node_id}
                </span>
              </div>
              <div className="col-span-2">
                <StatusBadge status={vm.status} />
              </div>
              <div className="col-span-2">
                <div className="flex items-center gap-3">
                  <div className="flex items-center gap-1">
                    <span className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-xs font-black">
                      {vm.resources.cpu}
                    </span>
                    <span className="text-xs text-gray-500">CPU</span>
                  </div>
                  <div className="flex items-center gap-1">
                    <span className="w-6 h-6 bg-secondary flex items-center justify-center border border-black text-xs font-black">
                      {Math.round(vm.resources.ram / 1024)}
                    </span>
                    <span className="text-xs text-gray-500">GB</span>
                  </div>
                </div>
              </div>
              <div className="col-span-3 flex items-center justify-end gap-2">
                {/* Start */}
                <Button
                  variant="success"
                  size="sm"
                  onClick={() => handleAction(vm, "start")}
                  disabled={vm.status === "running" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title="Start"
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
                  disabled={vm.status === "stopped" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title="Stop"
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
                  disabled={vm.status !== "running" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title="Restart"
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
                  disabled={vm.status !== "running" || !!actionLoading}
                  className="h-8 w-8 p-0"
                  title="Console"
                >
                  <Terminal className="w-4 h-4" />
                </Button>
                
                {/* Delete */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirm(vm)}
                  disabled={!!actionLoading}
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
      
      {/* Pagination */}
      {!isLoading && !error && totalPages > 1 && (
        <div className="flex items-center justify-between mt-6">
          <p className="text-sm font-medium text-gray-500">
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
      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete VM"
        message={`Are you sure you want to delete "${deleteConfirm?.hostname}"? This action cannot be undone.`}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
        loading={deleteVM.isPending}
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
