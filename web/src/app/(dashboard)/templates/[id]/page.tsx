"use client"

import { useState, useCallback } from "react"
import Link from "next/link"
import { useParams } from "next/navigation"
import { 
  ArrowLeft, 
  FileArchive,
  HardDrive,
  Clock,
  CheckCircle2,
  XCircle,
  RefreshCw,
  Trash2,
  Loader2,
  Server,
  ExternalLink,
  Monitor,
  Calendar
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

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
  size: number
  osType: string
  description?: string
  createdAt: string
  vmCount: number
  nodeStatus: TemplateNodeStatus[]
}

// Mock data - in production, fetch based on ID
const mockTemplate: Template = {
  id: "1",
  name: "ubuntu-22.04-server",
  version: "1.0.0",
  size: 2_500_000_000,
  osType: "Ubuntu 22.04 LTS",
  description: "Ubuntu Server 22.04 LTS with cloud-init support. Minimal installation with OpenSSH server.",
  createdAt: "2024-01-15",
  vmCount: 12,
  nodeStatus: [
    { nodeId: "n1", nodeName: "node-01", synced: "synced", lastSync: "2024-01-28T10:30:00Z" },
    { nodeId: "n2", nodeName: "node-02", synced: "synced", lastSync: "2024-01-28T10:30:00Z" },
    { nodeId: "n3", nodeName: "node-03", synced: "pending", lastSync: "2024-01-27T15:00:00Z" },
    { nodeId: "n4", nodeName: "node-04", synced: "error", lastSync: "2024-01-20T12:00:00Z" },
  ]
}

const mockVMs = [
  { id: "v1", name: "web-server-01", hostname: "web01.internal", node: "node-01" },
  { id: "v2", name: "web-server-02", hostname: "web02.internal", node: "node-01" },
  { id: "v3", name: "api-gateway", hostname: "api01.internal", node: "node-02" },
  { id: "v4", name: "dev-container", hostname: "dev01.internal", node: "node-02" },
]

// Format bytes
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
    month: "long", 
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
  
  if (diffMins < 60) return `${diffMins} minutes ago`
  if (diffHours < 24) return `${diffHours} hours ago`
  return `${diffDays} days ago`
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
    synced: <CheckCircle2 className="w-4 h-4" />,
    pending: <Clock className="w-4 h-4" />,
    error: <XCircle className="w-4 h-4" />,
    outdated: <RefreshCw className="w-4 h-4" />,
  }
  
  return (
    <span className={`inline-flex items-center gap-2 px-3 py-1 text-xs font-black uppercase tracking-wider border-2 border-black ${styles[status]}`}>
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
        <h3 className="text-xl font-black uppercase mb-4 flex items-center gap-2">
          <AlertTriangle className="w-6 h-6 text-warning" />
          {title}
        </h3>
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

import { AlertTriangle } from "lucide-react"

export default function TemplateDetailPage() {
  const params = useParams()
  const templateId = params.id as string
  
  // State
  const [template, setTemplate] = useState<Template>(mockTemplate)
  const [syncingNodes, setSyncingNodes] = useState<string[]>([])
  const [deleteConfirm, setDeleteConfirm] = useState(false)
  const [actionLoading, setActionLoading] = useState(false)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  
  // Sync to specific node
  const handleSyncNode = useCallback(async (nodeId: string) => {
    setSyncingNodes(prev => [...prev, nodeId])
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 2000))
    
    // Update local state
    setTemplate(prev => ({
      ...prev,
      nodeStatus: prev.nodeStatus.map(n => 
        n.nodeId === nodeId 
          ? { ...n, synced: "synced" as SyncStatus, lastSync: new Date().toISOString() }
          : n
      )
    }))
    
    setSyncingNodes(prev => prev.filter(id => id !== nodeId))
    setToast({ message: "Node synced successfully", type: "success" })
  }, [])
  
  // Sync to all nodes
  const handleSyncAll = useCallback(async () => {
    const pendingNodes = template.nodeStatus
      .filter(n => n.synced !== "synced")
      .map(n => n.nodeId)
    
    if (pendingNodes.length === 0) {
      setToast({ message: "All nodes are already in sync", type: "success" })
      return
    }
    
    setSyncingNodes(pendingNodes)
    
    // Simulate API call for each node with staggered timing
    for (const nodeId of pendingNodes) {
      await new Promise(resolve => setTimeout(resolve, 1500))
      setTemplate(prev => ({
        ...prev,
        nodeStatus: prev.nodeStatus.map(n => 
          n.nodeId === nodeId 
            ? { ...n, synced: "synced" as SyncStatus, lastSync: new Date().toISOString() }
            : n
        )
      }))
    }
    
    setSyncingNodes([])
    setToast({ message: "All nodes synced successfully", type: "success" })
  }, [template.nodeStatus])
  
  // Delete template
  const handleDelete = useCallback(async () => {
    setActionLoading(true)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1500))
    
    // In production, redirect to list after delete
    setToast({ message: `Template "${template.name}" deleted`, type: "success" })
    
    setTimeout(() => {
      window.location.href = "/templates"
    }, 1500)
  }, [template.name])
  
  // Calculate sync stats
  const syncedCount = template.nodeStatus.filter(n => n.synced === "synced").length
  const totalNodes = template.nodeStatus.length
  const hasIssues = template.nodeStatus.some(n => n.synced === "error" || n.synced === "outdated")
  
  return (
    <div className="max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/templates">
          <Button variant="ghost" size="sm" className="border-2 border-black">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <div className="w-12 h-12 bg-secondary flex items-center justify-center border-2 border-black">
              <FileArchive className="w-6 h-6" />
            </div>
            <div>
              <h1 className="text-3xl font-black uppercase tracking-tight text-black">
                {template.name}
              </h1>
              <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
                v{template.version} • {template.osType}
              </p>
            </div>
          </div>
        </div>
        <div className="flex gap-2">
          <Button 
            variant="destructive"
            onClick={() => setDeleteConfirm(true)}
            className="gap-2"
          >
            <Trash2 className="w-4 h-4" />
            Delete
          </Button>
        </div>
      </div>
      
      {/* Template Info */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6 mb-6">
        {/* Left: Info Card */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2">
            <FileArchive className="w-5 h-5" />
            Template Details
          </h2>
          
          <div className="space-y-4">
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Version</span>
              <span className="font-black">v{template.version}</span>
            </div>
            
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Size</span>
              <span className="font-mono font-bold">{formatBytes(template.size)}</span>
            </div>
            
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">OS Type</span>
              <span className="font-bold">{template.osType}</span>
            </div>
            
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Created</span>
              <span className="font-bold">{formatDate(template.createdAt)}</span>
            </div>
            
            <div className="flex items-center justify-between py-2">
              <span className="text-sm font-bold uppercase text-gray-500">VMs Using</span>
              <span className="flex items-center gap-2 font-bold">
                <Monitor className="w-4 h-4" />
                {template.vmCount}
              </span>
            </div>
          </div>
          
          {template.description && (
            <div className="mt-6 pt-4 border-t-2 border-black">
              <h3 className="text-xs font-black uppercase text-gray-500 mb-2">Description</h3>
              <p className="text-sm font-medium">{template.description}</p>
            </div>
          )}
        </div>
        
        {/* Right: Sync Overview */}
        <div className={`bg-white border-4 border-black p-6 shadow-neo ${hasIssues ? "border-danger" : ""}`}>
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-lg font-black uppercase flex items-center gap-2">
              <Server className="w-5 h-5" />
              Sync Status
            </h2>
            <Button 
              variant="success" 
              size="sm" 
              onClick={handleSyncAll}
              disabled={syncingNodes.length > 0}
              className="gap-2"
            >
              {syncingNodes.length > 0 ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                <RefreshCw className="w-4 h-4" />
              )}
              Sync All
            </Button>
          </div>
          
          {/* Sync Summary */}
          <div className="mb-6 p-4 bg-gray-50 border-2 border-black">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-bold uppercase">Overall Status</span>
              <SyncStatusBadge 
                status={hasIssues ? "error" : syncedCount === totalNodes ? "synced" : "pending"} 
              />
            </div>
            <div className="w-full h-4 bg-gray-200 border-2 border-black">
              <div 
                className="h-full bg-success transition-all duration-500"
                style={{ width: `${(syncedCount / totalNodes) * 100}%` }}
              />
            </div>
            <p className="text-xs font-bold mt-2">
              {syncedCount} of {totalNodes} nodes synced
            </p>
          </div>
          
          {/* Per-node status */}
          <div className="space-y-3">
            {template.nodeStatus.map((node) => (
              <div 
                key={node.nodeId}
                className="flex items-center justify-between p-3 bg-gray-50 border-2 border-black"
              >
                <div className="flex items-center gap-3">
                  <Server className="w-5 h-5 text-gray-400" />
                  <div>
                    <p className="font-bold">{node.nodeName}</p>
                    {node.lastSync && (
                      <p className="text-xs text-gray-500">
                        Last sync: {formatRelativeTime(node.lastSync)}
                      </p>
                    )}
                  </div>
                </div>
                
                <div className="flex items-center gap-2">
                  <SyncStatusBadge status={node.synced} />
                  {node.synced !== "synced" && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleSyncNode(node.nodeId)}
                      disabled={syncingNodes.includes(node.nodeId)}
                      className="border-2 border-black hover:bg-success"
                    >
                      {syncingNodes.includes(node.nodeId) ? (
                        <Loader2 className="w-4 h-4 animate-spin" />
                      ) : (
                        <RefreshCw className="w-4 h-4" />
                      )}
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
      
      {/* VMs using this template */}
      {mockVMs.length > 0 && (
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2">
            <Monitor className="w-5 h-5" />
            Virtual Machines Using This Template
          </h2>
          
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {mockVMs.map((vm) => (
              <Link 
                key={vm.id}
                href={`/vms`}
                className="flex items-center gap-3 p-4 bg-gray-50 border-2 border-black hover:bg-warning hover:border-warning transition-colors"
              >
                <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                  <Monitor className="w-5 h-5" />
                </div>
                <div className="flex-1">
                  <p className="font-black">{vm.name}</p>
                  <p className="text-xs text-gray-500">{vm.hostname}</p>
                </div>
                <span className="text-xs font-bold text-gray-400">{vm.node}</span>
                <ExternalLink className="w-4 h-4 text-gray-400" />
              </Link>
            ))}
          </div>
        </div>
      )}
      
      {/* Delete Confirmation Dialog */}
      <ConfirmDialog
        open={deleteConfirm}
        title="Delete Template"
        message={
          template.vmCount > 0
            ? `WARNING: This template is currently used by ${template.vmCount} virtual machine(s). Deleting it will break those VMs. Are you absolutely sure you want to proceed?`
            : `Are you sure you want to delete "${template.name}"? This action cannot be undone.`
        }
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(false)}
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