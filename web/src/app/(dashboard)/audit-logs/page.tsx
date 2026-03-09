"use client"

import { useState, useEffect, useMemo, useCallback } from "react"
import { 
  Search, 
  Filter, 
  ChevronLeft, 
  ChevronRight,
  Server,
  Play,
  Square,
  User,
  Wifi,
  X,
  FileJson,
  FileText,
  Eye,
  Loader2,
  AlertCircle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { useAuditLogs } from "@/lib/hooks/use-audit-logs"
import type { AuditLog } from "@/types"

// Action icon component
function ActionIcon({ action }: { action: string }) {
  const iconClass = "w-5 h-5"
  const a = action.toUpperCase()
  
  if (a.includes("CREATE") || a.includes("DELETE")) return <Server className={iconClass} />
  if (a.includes("START")) return <Play className={iconClass} />
  if (a.includes("STOP")) return <Square className={iconClass} />
  if (a.includes("LOGIN") || a.includes("LOGOUT") || a.includes("USER")) return <User className={iconClass} />
  if (a.includes("NETWORK")) return <Wifi className={iconClass} />
  return <FileText className={iconClass} />
}

// Action badge component
function ActionBadge({ action }: { action: string }) {
  const a = action.toUpperCase()
  let color = "bg-muted text-black"
  
  if (a.includes("CREATE")) color = "bg-success text-black"
  else if (a.includes("DELETE")) color = "bg-danger text-white"
  else if (a.includes("START")) color = "bg-secondary text-black"
  else if (a.includes("STOP")) color = "bg-warning text-black"
  else if (a.includes("LOGIN")) color = "bg-primary text-black"
  else if (a.includes("NETWORK")) color = "bg-accent text-white"
  
  return (
    <span className={`inline-flex items-center gap-2 px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${color}`}>
      <ActionIcon action={action} />
      {action.replace(/_/g, " ")}
    </span>
  )
}

// JSON Viewer component
function JsonViewer({ data, title }: { data: Record<string, unknown> | undefined; title: string }) {
  if (!data || Object.keys(data).length === 0) return <p className="text-gray-400 italic">No data</p>
  
  return (
    <div className="bg-gray-50 border-2 border-black p-4 font-mono text-xs overflow-x-auto">
      <p className="font-bold mb-2 uppercase text-gray-500">{title}</p>
      <pre className="whitespace-pre-wrap break-all">
        {JSON.stringify(data, null, 2)}
      </pre>
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
  const end = Math.min(totalPages, start + maxVisible - 1)
  
  if (end - start < maxVisible - 1) {
    start = Math.max(1, end - maxVisible + 1)
  }
  
  for (let i = start; i <= end; i++) {
    pages.push(i)
  }
  
  if (totalPages <= 1) return null
  
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

// Export functions
function exportToCSV(logs: AuditLog[]) {
  const headers = ["Timestamp", "User ID", "Action", "Resource Type", "Resource ID", "IP Address"]
  const rows = logs.map(log => [
    log.created_at,
    log.user_id || "",
    log.action,
    log.resource_type || "",
    log.resource_id || "",
    log.ip_address || "",
  ])
  
  const csv = [headers.join(","), ...rows.map(row => row.map(cell => `"${cell}"`).join(","))].join("\n")
  const blob = new Blob([csv], { type: "text/csv" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = `audit-logs-${new Date().toISOString().split("T")[0]}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

function exportToJSON(logs: AuditLog[]) {
  const json = JSON.stringify(logs, null, 2)
  const blob = new Blob([json], { type: "application/json" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = `audit-logs-${new Date().toISOString().split("T")[0]}.json`
  a.click()
  URL.revokeObjectURL(url)
}

// Format timestamp
function formatTimestamp(timestamp: string) {
  const date = new Date(timestamp)
  return date.toLocaleString("en-US", {
    month: "short",
    day: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  })
}

export default function AuditLogsPage() {
  // Filter state
  const [searchTerm, setSearchTerm] = useState("")
  const [selectedAction, setSelectedAction] = useState<string>("")
  const [selectedResourceType, setSelectedResourceType] = useState<string>("")
  const [dateFrom, setDateFrom] = useState("")
  const [dateTo, setDateTo] = useState("")
  const [currentPage, setCurrentPage] = useState(1)
  const [selectedLog, setSelectedLog] = useState<AuditLog | null>(null)
  const itemsPerPage = 20
  
  // Data hook
  const { data: logsData, isLoading, error, refetch } = useAuditLogs({
    action: selectedAction || undefined,
    resource_type: selectedResourceType || undefined,
    start_date: dateFrom || undefined,
    end_date: dateTo || undefined,
    page: currentPage,
    pageSize: itemsPerPage,
  })

  const logs = logsData?.data || []
  const totalPages = logsData?.totalPages || 1
  const totalLogs = logsData?.total || 0

  // Client-side search filter (for resource_id search since API may not support text search)
  const filteredLogs = useMemo(() => {
    if (!searchTerm) return logs
    const query = searchTerm.toLowerCase()
    return logs.filter(log =>
      log.resource_id?.toLowerCase().includes(query) ||
      log.user_id?.toLowerCase().includes(query) ||
      log.action.toLowerCase().includes(query)
    )
  }, [logs, searchTerm])

  // Reset page on filter change
  useEffect(() => {
    setCurrentPage(1)
  }, [selectedAction, selectedResourceType, dateFrom, dateTo])

  // Clear filters
  const clearFilters = useCallback(() => {
    setSearchTerm("")
    setSelectedAction("")
    setSelectedResourceType("")
    setDateFrom("")
    setDateTo("")
    setCurrentPage(1)
  }, [])
  
  const hasActiveFilters = searchTerm || selectedAction || selectedResourceType || dateFrom || dateTo

  // Loading
  if (isLoading) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight">Audit Logs</h1>
          <Skeleton className="h-5 w-48 mt-1" />
        </div>
        <Skeleton className="h-32 border-4 border-black" />
        <div className="space-y-2">
          {[1,2,3,4,5].map(i => <Skeleton key={i} className="h-14 border-2 border-black" />)}
        </div>
      </div>
    )
  }

  // Error
  if (error) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight">Audit Logs</h1>
        </div>
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Failed to load audit logs</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()}>Retry</Button>
        </div>
      </div>
    )
  }
  
  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight">Audit Logs</h1>
          <p className="text-gray-500 font-medium mt-1">
            {totalLogs} total log entries
          </p>
        </div>
      </div>
      
      {/* Filters */}
      <div className="bg-white border-4 border-black p-6 shadow-neo">
        <div className="flex items-center gap-2 mb-4">
          <Filter className="w-5 h-5" />
          <h2 className="text-lg font-black uppercase">Filters</h2>
          {hasActiveFilters && (
            <Button 
              variant="ghost" 
              size="sm" 
              onClick={clearFilters}
              className="ml-auto text-xs font-bold uppercase text-gray-500 hover:text-black"
            >
              <X className="w-4 h-4 mr-1" />
              Clear
            </Button>
          )}
        </div>
        
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-5 gap-4">
          {/* Search */}
          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input
              type="text"
              placeholder="Search resource, user..."
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="pl-10 border-2 border-black"
            />
          </div>
          
          {/* Action Filter */}
          <select
            value={selectedAction}
            onChange={(e) => setSelectedAction(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Actions</option>
            <option value="CREATE_VM">Create VM</option>
            <option value="DELETE_VM">Delete VM</option>
            <option value="START_VM">Start VM</option>
            <option value="STOP_VM">Stop VM</option>
            <option value="LOGIN">Login</option>
            <option value="LOGOUT">Logout</option>
            <option value="NETWORK_CHANGE">Network Change</option>
          </select>
          
          {/* Resource Type Filter */}
          <select
            value={selectedResourceType}
            onChange={(e) => setSelectedResourceType(e.target.value)}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All Resources</option>
            <option value="VM">VM</option>
            <option value="Network">Network</option>
            <option value="Session">Session</option>
            <option value="User">User</option>
            <option value="Node">Node</option>
          </select>
          
          {/* Date From */}
          <Input
            type="date"
            value={dateFrom}
            onChange={(e) => setDateFrom(e.target.value)}
            placeholder="From date"
            className="border-2 border-black"
          />
          
          {/* Date To */}
          <Input
            type="date"
            value={dateTo}
            onChange={(e) => setDateTo(e.target.value)}
            placeholder="To date"
            className="border-2 border-black"
          />
        </div>
        
        {/* Results count */}
        <div className="mt-4 text-sm font-bold text-gray-500 uppercase">
          Showing {filteredLogs.length} of {totalLogs} logs
        </div>
      </div>
      
      {/* Table */}
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead className="bg-gray-50 border-b-4 border-black">
              <tr>
                <th className="text-left px-4 py-3 text-xs font-black uppercase tracking-wider">Timestamp</th>
                <th className="text-left px-4 py-3 text-xs font-black uppercase tracking-wider">User</th>
                <th className="text-left px-4 py-3 text-xs font-black uppercase tracking-wider">Action</th>
                <th className="text-left px-4 py-3 text-xs font-black uppercase tracking-wider">Resource</th>
                <th className="text-left px-4 py-3 text-xs font-black uppercase tracking-wider">IP Address</th>
                <th className="text-left px-4 py-3 text-xs font-black uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filteredLogs.length === 0 ? (
                <tr>
                  <td colSpan={6} className="px-4 py-12 text-center">
                    <FileText className="w-12 h-12 text-gray-300 mx-auto mb-4" />
                    <p className="text-gray-500 font-bold uppercase">No audit logs found</p>
                    {hasActiveFilters && (
                      <Button variant="ghost" onClick={clearFilters} className="mt-4 border-2 border-black">Clear filters</Button>
                    )}
                  </td>
                </tr>
              ) : (
                filteredLogs.map((log) => (
                  <tr key={log.id} className="border-b-2 border-gray-100 hover:bg-gray-50 transition-colors">
                    <td className="px-4 py-3 text-sm font-mono">
                      {formatTimestamp(log.created_at)}
                    </td>
                    <td className="px-4 py-3">
                      <span className="inline-flex items-center gap-2 text-sm font-bold">
                        <User className="w-4 h-4 text-gray-400" />
                        <span className="font-mono text-xs">{log.user_id ? log.user_id.slice(0, 12) + "..." : "system"}</span>
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <ActionBadge action={log.action} />
                    </td>
                    <td className="px-4 py-3 text-sm font-medium">
                      <span className="font-mono">{log.resource_id ? log.resource_id.slice(0, 12) + "..." : "-"}</span>
                      {log.resource_type && (
                        <span className="text-gray-400 text-xs ml-2">({log.resource_type})</span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-sm font-mono text-gray-500">
                      {log.ip_address || "-"}
                    </td>
                    <td className="px-4 py-3">
                      <Button 
                        variant="ghost" 
                        size="sm"
                        onClick={() => setSelectedLog(log)}
                        className="border-2 border-black hover:bg-black hover:text-white"
                      >
                        <Eye className="w-4 h-4" />
                        <span className="ml-2 uppercase font-bold text-xs">View</span>
                      </Button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
      
      {/* Pagination and Export */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
        <Pagination 
          currentPage={currentPage} 
          totalPages={totalPages} 
          onPageChange={setCurrentPage} 
        />
        
        {/* Export Buttons */}
        <div className="flex items-center gap-2">
          <span className="text-sm font-bold uppercase text-gray-500 mr-2">Export:</span>
          <Button 
            variant="ghost"
            onClick={() => exportToCSV(filteredLogs)}
            className="border-2 border-black"
            disabled={filteredLogs.length === 0}
          >
            <FileText className="w-4 h-4" />
            <span className="ml-2 uppercase font-bold text-xs">CSV</span>
          </Button>
          <Button 
            variant="ghost"
            onClick={() => exportToJSON(filteredLogs)}
            className="border-2 border-black"
            disabled={filteredLogs.length === 0}
          >
            <FileJson className="w-4 h-4" />
            <span className="ml-2 uppercase font-bold text-xs">JSON</span>
          </Button>
        </div>
      </div>
      
      {/* Detail Modal */}
      <Dialog open={!!selectedLog} onOpenChange={() => setSelectedLog(null)}>
        <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="text-xl">Audit Log Details</DialogTitle>
            <DialogDescription>
              Full details of the audit log entry
            </DialogDescription>
          </DialogHeader>
          
          {selectedLog && (
            <div className="space-y-6 mt-4">
              {/* Summary */}
              <div className="bg-gray-50 border-2 border-black p-4">
                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Timestamp</p>
                    <p className="font-mono text-sm">{formatTimestamp(selectedLog.created_at)}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Action</p>
                    <ActionBadge action={selectedLog.action} />
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">User ID</p>
                    <p className="font-mono text-sm">{selectedLog.user_id || "system"}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">IP Address</p>
                    <p className="font-mono text-sm">{selectedLog.ip_address || "-"}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Resource ID</p>
                    <p className="font-mono font-bold text-sm">{selectedLog.resource_id || "-"}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Resource Type</p>
                    <p className="font-bold">{selectedLog.resource_type || "-"}</p>
                  </div>
                  {selectedLog.user_agent && (
                    <div className="col-span-2">
                      <p className="text-xs font-bold uppercase text-gray-500 mb-1">User Agent</p>
                      <p className="font-mono text-xs break-all">{selectedLog.user_agent}</p>
                    </div>
                  )}
                </div>
              </div>
              
              {/* Before/After Snapshots */}
              {(selectedLog.before_snapshot || selectedLog.after_snapshot) && (
                <div className="space-y-4">
                  <h3 className="font-black uppercase text-sm">Before / After Snapshots</h3>
                  <div className="grid grid-cols-2 gap-4">
                    <div>
                      <JsonViewer data={selectedLog.before_snapshot} title="Before" />
                    </div>
                    <div>
                      <JsonViewer data={selectedLog.after_snapshot} title="After" />
                    </div>
                  </div>
                </div>
              )}

              {/* Details */}
              {selectedLog.details && Object.keys(selectedLog.details).length > 0 && (
                <div>
                  <h3 className="font-black uppercase text-sm mb-2">Details</h3>
                  <JsonViewer data={selectedLog.details} title="Additional Data" />
                </div>
              )}
              
              {/* Raw JSON */}
              <div>
                <h3 className="font-black uppercase text-sm mb-2">Raw JSON</h3>
                <div className="bg-gray-900 text-gray-100 border-2 border-black p-4 font-mono text-xs overflow-x-auto max-h-64 overflow-y-auto">
                  <pre>{JSON.stringify(selectedLog, null, 2)}</pre>
                </div>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
