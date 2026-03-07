"use client"

import { useState, useEffect, useMemo } from "react"
import { 
  Search, 
  Filter, 
  Download, 
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
  RotateCcw
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"

// Types
type ActionType = "CREATE_VM" | "DELETE_VM" | "START_VM" | "STOP_VM" | "LOGIN" | "LOGOUT" | "NETWORK_CHANGE"

interface AuditLog {
  id: string
  timestamp: string
  user: string
  action: ActionType
  resource: string
  resourceType: string
  ipAddress: string
  details?: {
    before?: Record<string, unknown>
    after?: Record<string, unknown>
  }
}

// Mock data
const mockAuditLogs: AuditLog[] = [
  { id: "1", timestamp: "2026-03-07T10:30:00Z", user: "admin", action: "CREATE_VM", resource: "vm-101", resourceType: "VM", ipAddress: "192.168.1.100", details: { after: { name: "web-server", cpu: 4, ram: 8, status: "running" } } },
  { id: "2", timestamp: "2026-03-07T09:45:00Z", user: "admin", action: "START_VM", resource: "vm-102", resourceType: "VM", ipAddress: "192.168.1.100", details: { before: { status: "stopped" }, after: { status: "running" } } },
  { id: "3", timestamp: "2026-03-07T08:20:00Z", user: "operator", action: "STOP_VM", resource: "vm-103", resourceType: "VM", ipAddress: "192.168.1.101", details: { before: { status: "running" }, after: { status: "stopped" } } },
  { id: "4", timestamp: "2026-03-06T18:30:00Z", user: "admin", action: "DELETE_VM", resource: "vm-099", resourceType: "VM", ipAddress: "192.168.1.100", details: { before: { name: "old-server", cpu: 2, ram: 4 } } },
  { id: "5", timestamp: "2026-03-06T15:00:00Z", user: "dba", action: "LOGIN", resource: "web-ui", resourceType: "Session", ipAddress: "192.168.1.50" },
  { id: "6", timestamp: "2026-03-06T14:30:00Z", user: "admin", action: "NETWORK_CHANGE", resource: "net-01", resourceType: "Network", ipAddress: "192.168.1.100", details: { before: { vlan: 100 }, after: { vlan: 200 } } },
  { id: "7", timestamp: "2026-03-06T12:00:00Z", user: "dev", action: "START_VM", resource: "vm-105", resourceType: "VM", ipAddress: "192.168.1.102", details: { before: { status: "stopped" }, after: { status: "running" } } },
  { id: "8", timestamp: "2026-03-06T10:15:00Z", user: "admin", action: "CREATE_VM", resource: "vm-106", resourceType: "VM", ipAddress: "192.168.1.100", details: { after: { name: "api-server", cpu: 8, ram: 16, status: "running" } } },
  { id: "9", timestamp: "2026-03-05T22:00:00Z", user: "dba", action: "LOGOUT", resource: "web-ui", resourceType: "Session", ipAddress: "192.168.1.50" },
  { id: "10", timestamp: "2026-03-05T18:30:00Z", user: "admin", action: "NETWORK_CHANGE", resource: "net-02", resourceType: "Network", ipAddress: "192.168.1.100", details: { before: { dhcp: true }, after: { dhcp: false, ip: "10.0.0.50" } } },
  { id: "11", timestamp: "2026-03-05T16:00:00Z", user: "ops", action: "STOP_VM", resource: "vm-107", resourceType: "VM", ipAddress: "192.168.1.103", details: { before: { status: "running" }, after: { status: "stopped" } } },
  { id: "12", timestamp: "2026-03-05T14:30:00Z", user: "admin", action: "DELETE_VM", resource: "vm-098", resourceType: "VM", ipAddress: "192.168.1.100", details: { before: { name: "test-vm" } } },
  { id: "13", timestamp: "2026-03-05T10:00:00Z", user: "dev", action: "LOGIN", resource: "web-ui", resourceType: "Session", ipAddress: "192.168.1.102" },
  { id: "14", timestamp: "2026-03-04T20:30:00Z", user: "admin", action: "CREATE_VM", resource: "vm-108", resourceType: "VM", ipAddress: "192.168.1.100", details: { after: { name: "db-server", cpu: 16, ram: 64, status: "running" } } },
  { id: "15", timestamp: "2026-03-04T18:00:00Z", user: "admin", action: "START_VM", resource: "vm-108", resourceType: "VM", ipAddress: "192.168.1.100", details: { before: { status: "stopped" }, after: { status: "running" } } },
]

const actionOptions: { value: ActionType | "all"; label: string }[] = [
  { value: "all", label: "All Actions" },
  { value: "CREATE_VM", label: "Create VM" },
  { value: "DELETE_VM", label: "Delete VM" },
  { value: "START_VM", label: "Start VM" },
  { value: "STOP_VM", label: "Stop VM" },
  { value: "LOGIN", label: "Login" },
  { value: "LOGOUT", label: "Logout" },
  { value: "NETWORK_CHANGE", label: "Network Change" },
]

const uniqueUsers = Array.from(new Set(mockAuditLogs.map(log => log.user)))

// Action icon component
function ActionIcon({ action }: { action: ActionType }) {
  const iconClass = "w-5 h-5"
  
  switch (action) {
    case "CREATE_VM":
    case "DELETE_VM":
      return <Server className={iconClass} />
    case "START_VM":
      return <Play className={iconClass} />
    case "STOP_VM":
      return <Square className={iconClass} />
    case "LOGIN":
    case "LOGOUT":
      return <User className={iconClass} />
    case "NETWORK_CHANGE":
      return <Wifi className={iconClass} />
    default:
      return <FileText className={iconClass} />
  }
}

// Action badge component
function ActionBadge({ action }: { action: ActionType }) {
  const colors: Record<ActionType, string> = {
    CREATE_VM: "bg-success text-black",
    DELETE_VM: "bg-danger text-white",
    START_VM: "bg-secondary text-black",
    STOP_VM: "bg-warning text-black",
    LOGIN: "bg-primary text-black",
    LOGOUT: "bg-muted text-black",
    NETWORK_CHANGE: "bg-accent text-white",
  }
  
  return (
    <span className={`inline-flex items-center gap-2 px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${colors[action]}`}>
      <ActionIcon action={action} />
      {action.replace("_", " ")}
    </span>
  )
}

// JSON Viewer component
function JsonViewer({ data, title }: { data: Record<string, unknown> | undefined; title: string }) {
  if (!data) return <p className="text-gray-400 italic">No data</p>
  
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

// Export functions
function exportToCSV(logs: AuditLog[]) {
  const headers = ["Timestamp", "User", "Action", "Resource", "Resource Type", "IP Address"]
  const rows = logs.map(log => [
    log.timestamp,
    log.user,
    log.action,
    log.resource,
    log.resourceType,
    log.ipAddress,
  ])
  
  const csv = [headers.join(","), ...rows.map(row => row.join(","))].join("\n")
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

export default function AuditLogsPage() {
  // State
  const [searchTerm, setSearchTerm] = useState("")
  const [selectedUser, setSelectedUser] = useState<string>("all")
  const [selectedAction, setSelectedAction] = useState<ActionType | "all">("all")
  const [dateFrom, setDateFrom] = useState("")
  const [dateTo, setDateTo] = useState("")
  const [currentPage, setCurrentPage] = useState(1)
  const [selectedLog, setSelectedLog] = useState<AuditLog | null>(null)
  const [showExportMenu, setShowExportMenu] = useState(false)
  const itemsPerPage = 10
  
  // Filtered logs
  const filteredLogs = useMemo(() => {
    return mockAuditLogs.filter(log => {
      // Search by resource ID
      if (searchTerm && !log.resource.toLowerCase().includes(searchTerm.toLowerCase())) {
        return false
      }
      // Filter by user
      if (selectedUser !== "all" && log.user !== selectedUser) {
        return false
      }
      // Filter by action type
      if (selectedAction !== "all" && log.action !== selectedAction) {
        return false
      }
      // Filter by date range
      if (dateFrom && new Date(log.timestamp) < new Date(dateFrom)) {
        return false
      }
      if (dateTo && new Date(log.timestamp) > new Date(dateTo + "T23:59:59")) {
        return false
      }
      return true
    })
  }, [searchTerm, selectedUser, selectedAction, dateFrom, dateTo])
  
  // Paginated logs
  const paginatedLogs = useMemo(() => {
    const start = (currentPage - 1) * itemsPerPage
    return filteredLogs.slice(start, start + itemsPerPage)
  }, [filteredLogs, currentPage])
  
  const totalPages = Math.ceil(filteredLogs.length / itemsPerPage)
  
  // Reset to page 1 when filters change
  useEffect(() => {
    setCurrentPage(1)
  }, [])
  
  // Format timestamp
  const formatTimestamp = (timestamp: string) => {
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
  
  // Clear filters
  const clearFilters = () => {
    setSearchTerm("")
    setSelectedUser("all")
    setSelectedAction("all")
    setDateFrom("")
    setDateTo("")
  }
  
  const hasActiveFilters = searchTerm || selectedUser !== "all" || selectedAction !== "all" || dateFrom || dateTo
  
  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight">Audit Logs</h1>
          <p className="text-gray-500 font-medium mt-1">
            View and track all system activities
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
          {/* Search by Resource ID */}
          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input
              type="text"
              placeholder="Search resource ID..."
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="pl-10"
            />
          </div>
          
          {/* User Filter */}
          <Select value={selectedUser} onValueChange={setSelectedUser}>
            <SelectTrigger>
              <SelectValue placeholder="Select user" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All Users</SelectItem>
              {uniqueUsers.map(user => (
                <SelectItem key={user} value={user}>{user}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          
          {/* Action Type Filter */}
          <Select value={selectedAction} onValueChange={(value) => setSelectedAction(value as ActionType | "all")}>
            <SelectTrigger>
              <SelectValue placeholder="Select action" />
            </SelectTrigger>
            <SelectContent>
              {actionOptions.map(option => (
                <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          
          {/* Date From */}
          <Input
            type="date"
            value={dateFrom}
            onChange={(e) => setDateFrom(e.target.value)}
            placeholder="From date"
          />
          
          {/* Date To */}
          <Input
            type="date"
            value={dateTo}
            onChange={(e) => setDateTo(e.target.value)}
            placeholder="To date"
          />
        </div>
        
        {/* Results count */}
        <div className="mt-4 text-sm font-bold text-gray-500 uppercase">
          Showing {filteredLogs.length} of {mockAuditLogs.length} logs
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
              {paginatedLogs.length === 0 ? (
                <tr>
                  <td colSpan={6} className="px-4 py-8 text-center text-gray-500 font-medium">
                    No audit logs found matching your filters.
                  </td>
                </tr>
              ) : (
                paginatedLogs.map((log) => (
                  <tr key={log.id} className="border-b-2 border-gray-100 hover:bg-gray-50 transition-colors">
                    <td className="px-4 py-3 text-sm font-mono">
                      {formatTimestamp(log.timestamp)}
                    </td>
                    <td className="px-4 py-3">
                      <span className="inline-flex items-center gap-2 text-sm font-bold">
                        <User className="w-4 h-4 text-gray-400" />
                        {log.user}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <ActionBadge action={log.action} />
                    </td>
                    <td className="px-4 py-3 text-sm font-medium">
                      <span className="font-mono">{log.resource}</span>
                      <span className="text-gray-400 text-xs ml-2">({log.resourceType})</span>
                    </td>
                    <td className="px-4 py-3 text-sm font-mono text-gray-500">
                      {log.ipAddress}
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
          >
            <FileText className="w-4 h-4" />
            <span className="ml-2 uppercase font-bold text-xs">CSV</span>
          </Button>
          <Button 
            variant="ghost"
            onClick={() => exportToJSON(filteredLogs)}
            className="border-2 border-black"
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
                    <p className="font-mono text-sm">{selectedLog.timestamp}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Action</p>
                    <ActionBadge action={selectedLog.action} />
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">User</p>
                    <p className="font-bold">{selectedLog.user}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">IP Address</p>
                    <p className="font-mono text-sm">{selectedLog.ipAddress}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Resource</p>
                    <p className="font-mono font-bold">{selectedLog.resource}</p>
                  </div>
                  <div>
                    <p className="text-xs font-bold uppercase text-gray-500 mb-1">Resource Type</p>
                    <p className="font-bold">{selectedLog.resourceType}</p>
                  </div>
                </div>
              </div>
              
              {/* Before/After Snapshots */}
              <div className="space-y-4">
                <h3 className="font-black uppercase text-sm">Before / After Snapshots</h3>
                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <JsonViewer data={selectedLog.details?.before} title="Before" />
                  </div>
                  <div>
                    <JsonViewer data={selectedLog.details?.after} title="After" />
                  </div>
                </div>
              </div>
              
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