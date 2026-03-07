"use client"

import { useState, useCallback } from "react"
import Link from "next/link"
import { 
  Plus, 
  Search,
  RefreshCw,
  Trash2,
  ExternalLink,
  FileArchive,
  CheckCircle2,
  XCircle,
  Clock,
  Loader2,
  Server,
  HardDrive
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// Types
type SyncStatus = "synced" | "pending" | "error" | "outdated"

interface TemplateNodeStatus {
  nodeId: string
  nodeName: string
  synced: SyncStatus
  lastSync?: string
}

interface Template {
  id: string
  name: string
  version: string
  size: number // in bytes
  osType: string
  createdAt: string
  vmCount: number
  nodeStatus: TemplateNodeStatus[]
}

// Mock data
const mockTemplates: Template[] = [
  {
    id: "1",
    name: "ubuntu-22.04-server",
    version: "1.0.0",
    size: 2_500_000_000, // ~2.5 GB
    osType: "Ubuntu 22.04 LTS",
    createdAt: "2024-01-15",
    vmCount: 12,
    nodeStatus: [
      { nodeId: "n1", nodeName: "node-01", synced: "synced", lastSync: "2024-01-28T10:30:00Z" },
      { nodeId: "n2", nodeName: "node-02", synced: "synced", lastSync: "2024-01-28T10:30:00Z" },
      { nodeId: "n3", nodeName: "node-03", synced: "pending", lastSync: "2024-01-27T15:00:00Z" },
    ]
  },
  {
    id: "2",
    name: "centos-stream-9",
    version: "2.1.0",
    size: 3_200_000_000, // ~3.2 GB
    osType: "CentOS Stream 9",
    createdAt: "2024-01-10",
    vmCount: 5,
    nodeStatus: [
      { nodeId: "n1", nodeName: "node-01", synced: "synced", lastSync: "2024-01-28T09:00:00Z" },
      { nodeId: "n2", nodeName: "node-02", synced: "error", lastSync: "2024-01-26T12:00:00Z" },
      { nodeId: "n3", nodeName: "node-03", synced: "outdated", lastSync: "2024-01-20T08:00:00Z" },
    ]
  },
  {
    id: "3",
    name: "debian-12-bookworm",
    version: "1.2.0",
    size: 1_800_000_000, // ~1.8 GB
    osType: "Debian 12",
    createdAt: "2024-01-20",
    vmCount: 8,
    nodeStatus: [
      { nodeId: "n1", nodeName: "node-01", synced: "synced", lastSync: "2024-01-28T11:00:00Z" },
      { nodeId: "n2", nodeName: "node-02", synced: "synced", lastSync: "2024-01-28T11:00:00Z" },
      { nodeId: "n3", nodeName: "node-03", synced: "synced", lastSync: "2024-01-28T11:00:00Z" },
    ]
  },
  {
    id: "4",
    name: "rocky-linux-9",
    version: "1.0.0",
    size: 2_100_000_000, // ~2.1 GB
    osType: "Rocky Linux 9",
    createdAt: "2024-01-22",
    vmCount: 3,
    nodeStatus: [
      { nodeId: "n1", nodeName: "node-01", synced: "pending", lastSync: "2024-01-25T14:00:00Z" },
      { nodeId: "n2", nodeName: "node-02", synced: "synced", lastSync: "2024-01-28T08:00:00Z" },
      { nodeId: "n3", nodeName: "node-03", synced: "synced", lastSync: "2024-01-28T08:00:00Z" },
    ]
  },
  {
    id: "5",
    name: "windows-server-2022",
    version: "3.0.0",
    size: 8_500_000_000, // ~8.5 GB
    osType: "Windows Server 2022",
    createdAt: "2024-01-05",
    vmCount: 6,
    nodeStatus: [
      { nodeId: "n1", nodeName: "node-01", synced: "synced", lastSync: "2024-01-28T07:00:00Z" },
      { nodeId: "n2", nodeName: "node-02", synced: "synced", lastSync: "2024-01-28T07:00:00Z" },
      { nodeId: "n3", nodeName: "node-03", synced: "error", lastSync: "2024-01-24T16:00:00Z" },
    ]
  },
]

// Format bytes to human readable
function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

// Format date
function formatDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString("en-US", { 
    year: "numeric", 
    month: "short", 
    day: "numeric" 
  })
}

// Format relative time
function formatRelativeTime(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMins / 60)
  const diffDays = Math.floor(diffHours / 24)
  
  if (diffMins < 60) return `${diffMins}m ago`
  if (diffHours < 24) return `${diffHours}h ago`
  return `${diffDays}d ago`
}

// Sync status badge
function SyncStatusBadge({ status }: { status: SyncStatus }) {
  const styles: Record<SyncStatus, string> = {
    synced: "bg-success text-black",
    pending: "bg-warning text-black",
    error: "bg-danger text-white",
    outdated: "bg-gray-400 text-white",
  }
  
  const icons: Record<SyncStatus, React.ReactNode> = {
    synced: <CheckCircle2 className="w-3 h-3" />,
    pending: <Clock className="w-3 h-3" />,
    error: <XCircle className="w-3 h-3" />,
    outdated: <RefreshCw className="w-3 h-3" />,
  }
  
  return (
    <span className={`inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black ${styles[status]}`}>
      {icons[status]}
      {status}
    </span>
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
  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success text-black" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

// Template detail view component
function TemplateNodeStatusList({ nodeStatus }: { nodeStatus: TemplateNodeStatus[] }) {
  return (
    <div className="flex flex-wrap gap-1">
      {nodeStatus.map((node) => (
        <div 
          key={node.nodeId}
          className="inline-flex flex-col items-center gap-0.5"
          title={`${node.nodeName}: ${node.synced}${node.lastSync ? ` (${formatRelativeTime(node.lastSync)})` : ""}`}
        >
          <Server className="w-3 h-3 text-gray-400" />
          <SyncStatusBadge status={node.synced} />
        </div>
      ))}
    </div>
  )
}

export default function TemplateListPage() {
  const [templates, setTemplates] = useState<Template[]>(mockTemplates)
  const [filteredTemplates, setFilteredTemplates] = useState<Template[]>(mockTemplates)
  const [searchQuery, setSearchQuery] = useState("")
  const [osFilter, setOsFilter] = useState("")
  const [deleteConfirm, setDeleteConfirm] = useState<Template | null>(null)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [syncingTemplateId, setSyncingTemplateId] = useState<string | null>(null)
  
  // Get unique OS types
  const osTypes = Array.from(new Set(templates.map(t => t.osType)))
  
  // Filter templates
  const filterTemplates = () => {
    let result = [...templates]
    
    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(t => 
        t.name.toLowerCase().includes(query) || 
        t.osType.toLowerCase().includes(query)
      )
    }
    
    if (osFilter) {
      result = result.filter(t => t.osType === osFilter)
    }
    
    setFilteredTemplates(result)
  }
  
  // Apply filters on search/osType change
  useState(() => {
    filterTemplates()
  })
  
  // Filter when state changes
  const handleSearchChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setSearchQuery(e.target.value)
    const query = e.target.value.toLowerCase()
    let result = [...templates]
    
    if (query) {
      result = result.filter(t => 
        t.name.toLowerCase().includes(query) || 
        t.osType.toLowerCase().includes(query)
      )
    }
    
    if (osFilter) {
      result = result.filter(t => t.osType === osFilter)
    }
    
    setFilteredTemplates(result)
  }
  
  const handleOsFilterChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    setOsFilter(e.target.value)
    const osType = e.target.value
    let result = [...templates]
    
    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(t => 
        t.name.toLowerCase().includes(query) || 
        t.osType.toLowerCase().includes(query)
      )
    }
    
    if (osType) {
      result = result.filter(t => t.osType === osType)
    }
    
    setFilteredTemplates(result)
  }
  
  // Sync handler
  const handleSync = useCallback(async (template: Template) => {
    setSyncingTemplateId(template.id)
    setActionLoading(`sync-${template.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 2000))
    
    // Update local state
    setTemplates(prev => prev.map(t => {
      if (t.id !== template.id) return t
      
      return {
        ...t,
        nodeStatus: t.nodeStatus.map(n => ({
          ...n,
          synced: "synced" as SyncStatus,
          lastSync: new Date().toISOString()
        }))
      }
    }))
    
    setToast({ message: `Template "${template.name}" synced to all nodes`, type: "success" })
    setSyncingTemplateId(null)
    setActionLoading(null)
  }, [])
  
  // Delete handler
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    
    setActionLoading(`delete-${deleteConfirm.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000))
    
    setTemplates(prev => prev.filter(t => t.id !== deleteConfirm.id))
    setToast({ message: `Template "${deleteConfirm.name}" deleted`, type: "success" })
    setDeleteConfirm(null)
    setActionLoading(null)
  }, [deleteConfirm])
  
  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            OS Templates
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            {filteredTemplates.length} templates
          </p>
        </div>
        <Link href="/templates/new">
          <Button className="gap-2">
            <Plus className="w-4 h-4" />
            Add Template
          </Button>
        </Link>
      </div>
      
      {/* Filters */}
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          {/* Search */}
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input
              type="text"
              placeholder="Search templates..."
              value={searchQuery}
              onChange={handleSearchChange}
              className="pl-10 border-2 border-black"
            />
          </div>
          
          {/* OS Type Filter */}
          <select
            value={osFilter}
            onChange={handleOsFilterChange}
            className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm"
          >
            <option value="">All OS Types</option>
            {osTypes.map(os => (
              <option key={os} value={os}>{os}</option>
            ))}
          </select>
        </div>
      </div>
      
      {/* Data Table */}
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        {/* Table Header */}
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
          <div className="col-span-3">Template</div>
          <div className="col-span-1">Version</div>
          <div className="col-span-2">Size</div>
          <div className="col-span-2">Sync Status</div>
          <div className="col-span-1">VMs</div>
          <div className="col-span-3 text-right">Actions</div>
        </div>
        
        {/* Table Body */}
        {filteredTemplates.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-gray-500 font-bold uppercase">No templates found</p>
          </div>
        ) : (
          filteredTemplates.map((template, index) => (
            <div 
              key={template.id} 
              className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${
                index % 2 === 0 ? "bg-white" : "bg-gray-50"
              }`}
            >
              {/* Template Info */}
              <div className="col-span-3">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 bg-secondary flex items-center justify-center border-2 border-black">
                    <FileArchive className="w-5 h-5" />
                  </div>
                  <div>
                    <Link 
                      href={`/templates/${template.id}`}
                      className="font-black text-black hover:text-primary transition-colors flex items-center gap-1"
                    >
                      {template.name}
                      <ExternalLink className="w-3 h-3" />
                    </Link>
                    <p className="text-xs text-gray-500 font-medium">{template.osType}</p>
                  </div>
                </div>
              </div>
              
              {/* Version */}
              <div className="col-span-1">
                <span className="inline-flex items-center px-2 py-1 text-xs font-bold border border-black bg-gray-100">
                  v{template.version}
                </span>
              </div>
              
              {/* Size */}
              <div className="col-span-2">
                <div className="flex items-center gap-2">
                  <HardDrive className="w-4 h-4 text-gray-400" />
                  <span className="font-mono text-sm font-bold">{formatBytes(template.size)}</span>
                </div>
              </div>
              
              {/* Sync Status */}
              <div className="col-span-2">
                <TemplateNodeStatusList nodeStatus={template.nodeStatus} />
              </div>
              
              {/* VM Count */}
              <div className="col-span-1">
                <span className="text-sm font-bold">{template.vmCount}</span>
              </div>
              
              {/* Actions */}
              <div className="col-span-3 flex items-center justify-end gap-2">
                {/* Sync to All */}
                <Button
                  variant="success"
                  size="sm"
                  onClick={() => handleSync(template)}
                  disabled={!!actionLoading}
                  className="gap-1"
                  title="Sync to all nodes"
                >
                  {syncingTemplateId === template.id ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : (
                    <RefreshCw className="w-4 h-4" />
                  )}
                  <span className="hidden sm:inline">Sync</span>
                </Button>
                
                {/* View Detail */}
                <Link href={`/templates/${template.id}`}>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="h-8 w-8 p-0"
                    title="View Details"
                  >
                    <ExternalLink className="w-4 h-4" />
                  </Button>
                </Link>
                
                {/* Delete */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirm(template)}
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
      
      {/* Delete Confirmation Dialog */}
      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete Template"
        message={
          deleteConfirm?.vmCount 
            ? `WARNING: "${deleteConfirm.name}" is currently used by ${deleteConfirm.vmCount} VM(s). Deleting this template will break those VMs. Are you sure you want to proceed?`
            : `Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`
        }
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