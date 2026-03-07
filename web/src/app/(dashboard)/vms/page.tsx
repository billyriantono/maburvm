"use client"

import { useState, useEffect, useCallback } from "react"
import Link from "next/link"
import { 
  Play, 
  Square, 
  RotateCcw, 
  Terminal, 
  Trash2, 
  Search,
  Plus,
  Filter,
  ChevronLeft,
  ChevronRight,
  X,
  Loader2
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// Types
type VMStatus = "running" | "stopped" | "suspended"

interface VM {
  id: string
  name: string
  hostname: string
  node: string
  status: VMStatus
  ip: string
  cpuCores: number
  ramGB: number
  user?: string
  createdAt: string
}

// Mock data
const mockVMs: VM[] = [
  { id: "1", name: "web-server-01", hostname: "web01.internal", node: "node-01", status: "running", ip: "10.0.1.10", cpuCores: 4, ramGB: 8, user: "admin", createdAt: "2024-01-15" },
  { id: "2", name: "web-server-02", hostname: "web02.internal", node: "node-01", status: "running", ip: "10.0.1.11", cpuCores: 4, ramGB: 8, user: "admin", createdAt: "2024-01-16" },
  { id: "3", name: "db-primary", hostname: "db01.internal", node: "node-02", status: "running", ip: "10.0.2.10", cpuCores: 8, ramGB: 32, user: "dba", createdAt: "2024-01-10" },
  { id: "4", name: "db-replica", hostname: "db02.internal", node: "node-02", status: "stopped", ip: "10.0.2.11", cpuCores: 8, ramGB: 32, user: "dba", createdAt: "2024-01-11" },
  { id: "5", name: "cache-server", hostname: "cache01.internal", node: "node-03", status: "running", ip: "10.0.3.10", cpuCores: 2, ramGB: 4, user: "ops", createdAt: "2024-01-18" },
  { id: "6", name: "worker-01", hostname: "worker01.internal", node: "node-03", status: "suspended", ip: "10.0.3.11", cpuCores: 4, ramGB: 16, user: "dev", createdAt: "2024-01-20" },
  { id: "7", name: "api-gateway", hostname: "api01.internal", node: "node-01", status: "running", ip: "10.0.1.20", cpuCores: 2, ramGB: 4, user: "admin", createdAt: "2024-01-22" },
  { id: "8", name: "monitoring", hostname: "mon01.internal", node: "node-04", status: "running", ip: "10.0.4.10", cpuCores: 2, ramGB: 2, user: "ops", createdAt: "2024-01-25" },
  { id: "9", name: "backup-server", hostname: "backup01.internal", node: "node-04", status: "stopped", ip: "10.0.4.11", cpuCores: 4, ramGB: 8, user: "admin", createdAt: "2024-01-26" },
  { id: "10", name: "dev-container", hostname: "dev01.internal", node: "node-02", status: "running", ip: "10.0.2.20", cpuCores: 2, ramGB: 4, user: "dev", createdAt: "2024-01-28" },
]

const nodes = ["node-01", "node-02", "node-03", "node-04"]
const users = ["admin", "dba", "ops", "dev"]

// Status badge component
function StatusBadge({ status }: { status: VMStatus }) {
  const colors = {
    running: "bg-[#CCFF00] text-black",
    stopped: "bg-[#FF4444] text-white",
    suspended: "bg-[#FFAA00] text-black",
  }
  
  return (
    <span className={`inline-flex items-center px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${colors[status]}`}>
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

// Confirm dialog component
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

export default function VMListPage() {
  // State
  const [vms, setVms] = useState<VM[]>(mockVMs)
  const [filteredVMs, setFilteredVMs] = useState<VM[]>(mockVMs)
  const [searchQuery, setSearchQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [nodeFilter, setNodeFilter] = useState<string>("")
  const [userFilter, setUserFilter] = useState<string>("")
  const [currentPage, setCurrentPage] = useState(1)
  const [sseConnected, setSseConnected] = useState(false)
  const [sseError, setSseError] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<VM | null>(null)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  
  const itemsPerPage = 5
  const totalPages = Math.ceil(filteredVMs.length / itemsPerPage)
  
  // Filter VMs
  useEffect(() => {
    let result = [...vms]
    
    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(vm => 
        vm.name.toLowerCase().includes(query) || 
        vm.hostname.toLowerCase().includes(query)
      )
    }
    
    if (statusFilter) {
      result = result.filter(vm => vm.status === statusFilter)
    }
    
    if (nodeFilter) {
      result = result.filter(vm => vm.node === nodeFilter)
    }
    
    if (userFilter) {
      result = result.filter(vm => vm.user === userFilter)
    }
    
    setFilteredVMs(result)
    setCurrentPage(1)
  }, [vms, searchQuery, statusFilter, nodeFilter, userFilter])
  
  // Paginate
  const paginatedVMs = filteredVMs.slice(
    (currentPage - 1) * itemsPerPage,
    currentPage * itemsPerPage
  )
  
  // SSE connection
  useEffect(() => {
    let eventSource: EventSource | null = null
    let reconnectTimeout: NodeJS.Timeout | null = null
    
    const connectSSE = () => {
      try {
        eventSource = new EventSource("/api/vm-events")
        
        eventSource.onopen = () => {
          setSseConnected(true)
          setSseError(false)
        }
        
        eventSource.onmessage = (event) => {
          try {
            const data = JSON.parse(event.data)
            if (data.type === "vm_update") {
              setVms(prev => prev.map(vm => 
                vm.id === data.vm.id 
                  ? { ...vm, status: data.vm.status }
                  : vm
              ))
            }
          } catch (e) {
            console.error("Failed to parse SSE data:", e)
          }
        }
        
        eventSource.onerror = () => {
          setSseConnected(false)
          setSseError(true)
          eventSource?.close()
          
          // Reconnect after 5 seconds
          reconnectTimeout = setTimeout(connectSSE, 5000)
        }
      } catch (error) {
        setSseError(true)
        console.error("SSE connection error:", error)
      }
    }
    
    connectSSE()
    
    return () => {
      eventSource?.close()
      if (reconnectTimeout) clearTimeout(reconnectTimeout)
    }
  }, [])
  
  // Action handlers
  const handleAction = useCallback(async (vm: VM, action: "start" | "stop" | "restart" | "console") => {
    setActionLoading(`${vm.id}-${action}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000))
    
    // Update local state
    setVms(prev => prev.map(v => {
      if (v.id !== vm.id) return v
      
      const statusMap: Record<string, VMStatus> = {
        start: "running",
        stop: "stopped",
        restart: "running",
        console: v.status
      }
      
      return { ...v, status: statusMap[action] }
    }))
    
    setToast({ message: `VM ${vm.name} ${action} successful`, type: "success" })
    setActionLoading(null)
  }, [])
  
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    
    setActionLoading(`delete-${deleteConfirm.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000))
    
    setVms(prev => prev.filter(vm => vm.id !== deleteConfirm.id))
    setToast({ message: `VM ${deleteConfirm.name} deleted`, type: "success" })
    setDeleteConfirm(null)
    setActionLoading(null)
  }, [deleteConfirm])
  
  const clearFilters = () => {
    setSearchQuery("")
    setStatusFilter("")
    setNodeFilter("")
    setUserFilter("")
  }
  
  const hasFilters = searchQuery || statusFilter || nodeFilter || userFilter
  
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
              {filteredVMs.length} VMs
            </p>
            {sseConnected && (
              <span className="flex items-center gap-1 text-xs font-bold text-success">
                <span className="w-2 h-2 bg-success rounded-full animate-pulse" />
                LIVE
              </span>
            )}
            {sseError && (
              <span className="flex items-center gap-1 text-xs font-bold text-danger">
                <span className="w-2 h-2 bg-danger rounded-full" />
                DISCONNECTED
              </span>
            )}
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
              placeholder="Search by name or hostname..."
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
          </select>
          
          {/* Node Filter */}
          <select
            value={nodeFilter}
            onChange={(e) => setNodeFilter(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Nodes</option>
            {nodes.map(node => (
              <option key={node} value={node}>{node}</option>
            ))}
          </select>
          
          {/* User Filter (Admin only) */}
          <select
            value={userFilter}
            onChange={(e) => setUserFilter(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Users</option>
            {users.map(user => (
              <option key={user} value={user}>{user}</option>
            ))}
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
          <div className="col-span-2">Name</div>
          <div className="col-span-1">Node</div>
          <div className="col-span-2">Status</div>
          <div className="col-span-2">IP Address</div>
          <div className="col-span-2">Resources</div>
          <div className="col-span-3 text-right">Actions</div>
        </div>
        
        {/* Table Body */}
        {paginatedVMs.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-gray-500 font-bold uppercase">No VMs found</p>
            {hasFilters && (
              <Button variant="ghost" onClick={clearFilters} className="mt-4 border-2 border-black">
                Clear filters
              </Button>
            )}
          </div>
        ) : (
          paginatedVMs.map((vm, index) => (
            <div 
              key={vm.id} 
              className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${
                index % 2 === 0 ? "bg-white" : "bg-gray-50"
              }`}
            >
              <div className="col-span-2 flex flex-col justify-center">
                <Link href={`/vms/${vm.id}`} className="font-black text-black hover:underline w-fit border-none">
                  {vm.name}
                </Link>
                <p className="text-xs text-gray-500 font-medium">{vm.hostname}</p>
              </div>
              <div className="col-span-1">
                <span className="inline-flex items-center px-2 py-1 text-xs font-bold border border-black bg-gray-100">
                  {vm.node}
                </span>
              </div>
              <div className="col-span-2">
                <StatusBadge status={vm.status} />
              </div>
              <div className="col-span-2">
                <p className="font-mono text-sm font-bold">{vm.ip}</p>
              </div>
              <div className="col-span-2">
                <div className="flex items-center gap-3">
                  <div className="flex items-center gap-1">
                    <span className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-xs font-black">
                      {vm.cpuCores}
                    </span>
                    <span className="text-xs text-gray-500">CPU</span>
                  </div>
                  <div className="flex items-center gap-1">
                    <span className="w-6 h-6 bg-secondary flex items-center justify-center border border-black text-xs font-black">
                      {vm.ramGB}
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
                  {actionLoading === `${vm.id}-console` ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : (
                    <Terminal className="w-4 h-4" />
                  )}
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
      {totalPages > 1 && (
        <div className="flex items-center justify-between mt-6">
          <p className="text-sm font-medium text-gray-500">
            Showing {(currentPage - 1) * itemsPerPage + 1} to {Math.min(currentPage * itemsPerPage, filteredVMs.length)} of {filteredVMs.length} VMs
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
        message={`Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`}
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