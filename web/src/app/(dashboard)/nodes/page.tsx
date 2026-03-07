"use client"

import { useState, useEffect, useCallback } from "react"
import Link from "next/link"
import { 
  Server, 
  Plus, 
  Search,
  X,
  Edit2,
  Trash2,
  RefreshCw,
  Loader2,
  Activity,
  HardDrive,
  Cpu,
  Network,
  Clock,
  AlertTriangle,
  CheckCircle,
  XCircle,
  Database
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// Types
type NodeStatus = "online" | "offline" | "maintenance"

interface NodeHealth {
  cpu: number
  memory: number
  disk: number
  networkIn: number
  networkOut: number
}

interface Node {
  id: string
  name: string
  ip: string
  status: NodeStatus
  vmCount: number
  runningVMs: number
  health: NodeHealth
  token: string
  lastSeen: string
  createdAt: string
}

// Mock data
const mockNodes: Node[] = [
  { 
    id: "1", 
    name: "node-01", 
    ip: "10.0.1.100", 
    status: "online", 
    vmCount: 12,
    runningVMs: 10,
    health: { cpu: 45, memory: 62, disk: 38, networkIn: 125, networkOut: 89 },
    token: "tok_abc123xyz",
    lastSeen: "2 minutes ago",
    createdAt: "2024-01-10"
  },
  { 
    id: "2", 
    name: "node-02", 
    ip: "10.0.2.100", 
    status: "online", 
    vmCount: 8,
    runningVMs: 6,
    health: { cpu: 72, memory: 85, disk: 55, networkIn: 234, networkOut: 156 },
    token: "tok_def456uvw",
    lastSeen: "1 minute ago",
    createdAt: "2024-01-12"
  },
  { 
    id: "3", 
    name: "node-03", 
    ip: "10.0.3.100", 
    status: "online", 
    vmCount: 15,
    runningVMs: 12,
    health: { cpu: 38, memory: 51, disk: 67, networkIn: 312, networkOut: 278 },
    token: "tok_ghi789rst",
    lastSeen: "30 seconds ago",
    createdAt: "2024-01-15"
  },
  { 
    id: "4", 
    name: "node-04", 
    ip: "10.0.4.100", 
    status: "offline", 
    vmCount: 5,
    runningVMs: 0,
    health: { cpu: 0, memory: 0, disk: 0, networkIn: 0, networkOut: 0 },
    token: "tok_jkl012mno",
    lastSeen: "2 hours ago",
    createdAt: "2024-01-18"
  },
  { 
    id: "5", 
    name: "node-05", 
    ip: "10.0.5.100", 
    status: "maintenance", 
    vmCount: 3,
    runningVMs: 0,
    health: { cpu: 5, memory: 12, disk: 22, networkIn: 0, networkOut: 0 },
    token: "tok_pqr345stu",
    lastSeen: "5 minutes ago",
    createdAt: "2024-01-20"
  },
  { 
    id: "6", 
    name: "node-06", 
    ip: "10.0.6.100", 
    status: "online", 
    vmCount: 10,
    runningVMs: 8,
    health: { cpu: 58, memory: 73, disk: 41, networkIn: 189, networkOut: 145 },
    token: "tok_vwx678yza",
    lastSeen: "10 seconds ago",
    createdAt: "2024-01-22"
  },
]

// Status indicator component
function StatusIndicator({ status }: { status: NodeStatus }) {
  const config = {
    online: { bg: "bg-success", icon: CheckCircle, text: "text-success", label: "Online" },
    offline: { bg: "bg-danger", icon: XCircle, text: "text-danger", label: "Offline" },
    maintenance: { bg: "bg-warning", icon: AlertTriangle, text: "text-warning", label: "Maintenance" },
  }
  
  const { bg, icon: Icon, text, label } = config[status]
  
  return (
    <div className="flex items-center gap-2">
      <span className={`w-3 h-3 rounded-full ${bg} ${status === "online" ? "animate-pulse" : ""}`} />
      <span className={`text-xs font-black uppercase ${text}`}>{label}</span>
    </div>
  )
}

// Progress bar component for resources
function ResourceBar({ value, label, color }: { value: number, label: string, color: string }) {
  const getColorClass = () => {
    if (value >= 90) return "bg-danger"
    if (value >= 70) return "bg-warning"
    return color
  }
  
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs font-medium text-gray-500 w-12">{label}</span>
      <div className="flex-1 h-2 bg-gray-200 border border-black">
        <div 
          className={`h-full ${getColorClass()} transition-all duration-300`} 
          style={{ width: `${value}%` }}
        />
      </div>
      <span className="text-xs font-black w-8 text-right">{value}%</span>
    </div>
  )
}

// Sparkline component for network
function Sparkline({ data, color }: { data: number[], color: string }) {
  const max = Math.max(...data, 1)
  const points = data.map((v, i) => {
    const x = (i / (data.length - 1)) * 100
    const y = 100 - (v / max) * 100
    return `${x},${y}`
  }).join(" ")
  
  return (
    <svg viewBox="0 0 100 30" className="w-20 h-6" preserveAspectRatio="none" aria-label="Network throughput sparkline">
      <polyline
        fill="none"
        stroke={color}
        strokeWidth="2"
        points={points}
      />
    </svg>
  )
}

// Confirm dialog component
function ConfirmDialog({ 
  open, 
  title, 
  message, 
  confirmLabel = "Confirm",
  onConfirm, 
  onCancel 
}: { 
  open: boolean
  title: string
  message: string
  confirmLabel?: string
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
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}

// Migration dialog component
function MigrationDialog({
  open,
  nodeName,
  onMigrate,
  onCancel
}: {
  open: boolean
  nodeName: string
  onMigrate: (targetNode: string) => void
  onCancel: () => void
}) {
  const [selectedTarget, setSelectedTarget] = useState("")
  
  if (!open) return null
  
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default" onClick={onCancel} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
        <h3 className="text-xl font-black uppercase mb-2">Migrate VMs First</h3>
        <p className="text-gray-600 font-medium mb-4">
          Node "{nodeName}" has running VMs. Select a target node to migrate them to before deletion.
        </p>
        
        <select
          value={selectedTarget}
          onChange={(e) => setSelectedTarget(e.target.value)}
          className="w-full h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm mb-6"
        >
          <option value="">Select target node...</option>
          {mockNodes.filter(n => n.status === "online" && n.name !== nodeName).map(n => (
            <option key={n.id} value={n.id}>{n.name} ({n.ip}) - {n.runningVMs} VMs free</option>
          ))}
        </select>
        
        <div className="flex gap-3 justify-end">
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black">
            Cancel
          </Button>
          <Button 
            variant="destructive" 
            onClick={() => onMigrate(selectedTarget)}
            disabled={!selectedTarget}
          >
            Migrate & Delete
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

export default function NodesListPage() {
  // State
  const [nodes, setNodes] = useState<Node[]>(mockNodes)
  const [filteredNodes, setFilteredNodes] = useState<Node[]>(mockNodes)
  const [searchQuery, setSearchQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [deleteConfirm, setDeleteConfirm] = useState<Node | null>(null)
  const [migrateDialog, setMigrateDialog] = useState<Node | null>(null)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [editNode, setEditNode] = useState<Node | null>(null)
  const [regenerateToken, setRegenerateToken] = useState<Node | null>(null)
  
  // Filter nodes
  useEffect(() => {
    let result = [...nodes]
    
    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(node => 
        node.name.toLowerCase().includes(query) || 
        node.ip.toLowerCase().includes(query)
      )
    }
    
    if (statusFilter) {
      result = result.filter(node => node.status === statusFilter)
    }
    
    setFilteredNodes(result)
  }, [nodes, searchQuery, statusFilter])
  
  // Action handlers
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    
    setActionLoading(`delete-${deleteConfirm.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000))
    
    setNodes(prev => prev.filter(node => node.id !== deleteConfirm.id))
    setToast({ message: `Node ${deleteConfirm.name} deleted`, type: "success" })
    setDeleteConfirm(null)
    setActionLoading(null)
  }, [deleteConfirm])

  const handleMigrateAndDelete = useCallback(async (targetNodeId: string) => {
    if (!migrateDialog || !targetNodeId) return
    
    setActionLoading(`migrate-${migrateDialog.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 2000))
    
    setNodes(prev => prev.filter(node => node.id !== migrateDialog.id))
    setToast({ message: `VMs migrated and node ${migrateDialog.name} deleted`, type: "success" })
    setMigrateDialog(null)
    setActionLoading(null)
  }, [migrateDialog])

  const handleDeleteClick = (node: Node) => {
    if (node.runningVMs > 0) {
      setMigrateDialog(node)
    } else {
      setDeleteConfirm(node)
    }
  }

  const handleRegenerateToken = useCallback(async (node: Node) => {
    setActionLoading(`token-${node.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1500))
    
    // Generate new token
    const newToken = "tok_" + Math.random().toString(36).substring(2, 10) + Math.random().toString(36).substring(2, 6)
    
    setNodes(prev => prev.map(n => 
      n.id === node.id ? { ...n, token: newToken } : n
    ))
    setToast({ message: `Token regenerated for ${node.name}`, type: "success" })
    setRegenerateToken(null)
    setActionLoading(null)
  }, [])

  const handleEdit = useCallback(async (node: Node) => {
    setActionLoading(`edit-${node.id}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000))
    
    setNodes(prev => prev.map(n => 
      n.id === node.id ? { ...n, name: node.name, ip: node.ip } : n
    ))
    setToast({ message: `Node ${node.name} updated`, type: "success" })
    setEditNode(null)
    setActionLoading(null)
  }, [])

  const clearFilters = () => {
    setSearchQuery("")
    setStatusFilter("")
  }
  
  const hasFilters = searchQuery || statusFilter

  // Calculate stats
  const stats = {
    total: nodes.length,
    online: nodes.filter(n => n.status === "online").length,
    offline: nodes.filter(n => n.status === "offline").length,
    totalVMs: nodes.reduce((sum, n) => sum + n.vmCount, 0),
    runningVMs: nodes.reduce((sum, n) => sum + n.runningVMs, 0),
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
            {stats.total} nodes • {stats.online} online • {stats.totalVMs} total VMs
          </p>
        </div>
        <Link href="/dashboard/nodes/new">
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
            <Server className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{stats.total}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Online</span>
            <span className="w-3 h-3 bg-success rounded-full animate-pulse" />
          </div>
          <p className="text-3xl font-black text-success">{stats.online}</p>
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
            <span className="text-xs font-black uppercase text-gray-500">Running VMs</span>
            <Activity className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{stats.runningVMs}</p>
        </div>
      </div>
      
      {/* Filters */}
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col lg:flex-row gap-4">
          {/* Search */}
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
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
            <option value="online">Online</option>
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
            <Server className="w-16 h-16 text-gray-300 mx-auto mb-4" />
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
                    <Link href={`/dashboard/nodes/${node.id}`} className="text-xl font-black uppercase hover:text-primary transition-colors">
                      {node.name}
                    </Link>
                    <p className="text-sm font-mono font-medium text-gray-500">{node.ip}</p>
                  </div>
                  <StatusIndicator status={node.status} />
                </div>
              </div>
              
              {/* Node Body */}
              <div className="p-4">
                {/* VM Count */}
                <div className="flex items-center gap-4 mb-4">
                  <div className="flex items-center gap-2">
                    <div className="w-8 h-8 bg-primary flex items-center justify-center border-2 border-black">
                      <Database className="w-4 h-4" />
                    </div>
                    <div>
                      <p className="text-lg font-black">{node.runningVMs}/{node.vmCount}</p>
                      <p className="text-[10px] font-bold text-gray-500 uppercase">VMs</p>
                    </div>
                  </div>
                  
                  <div className="flex items-center gap-2">
                    <Clock className="w-4 h-4 text-gray-400" />
                    <span className="text-xs font-medium text-gray-500">Last seen: {node.lastSeen}</span>
                  </div>
                </div>
                
                {/* Resource Usage - Only show if online */}
                {node.status === "online" && (
                  <div className="space-y-2 mb-4">
                    <ResourceBar value={node.health.cpu} label="CPU" color="bg-primary" />
                    <ResourceBar value={node.health.memory} label="RAM" color="bg-secondary" />
                    <ResourceBar value={node.health.disk} label="Disk" color="bg-accent" />
                    
                    {/* Network Sparklines */}
                    <div className="flex items-center justify-between pt-2 border-t border-gray-200">
                      <div className="flex items-center gap-2">
                        <Network className="w-4 h-4 text-gray-400" />
                        <span className="text-xs font-medium text-gray-500">Network</span>
                      </div>
                      <div className="flex items-center gap-4">
                        <div className="flex items-center gap-1">
                          <Sparkline data={[45, 78, 125, 89, 156, 125]} color="#22c55e" />
                          <span className="text-[10px] font-bold text-success">{node.health.networkIn} MB/s</span>
                        </div>
                        <div className="flex items-center gap-1">
                          <Sparkline data={[23, 56, 89, 67, 134, 89]} color="#3b82f6" />
                          <span className="text-[10px] font-bold text-primary">{node.health.networkOut} MB/s</span>
                        </div>
                      </div>
                    </div>
                  </div>
                )}
                
                {node.status === "maintenance" && (
                  <div className="bg-warning/20 border-2 border-warning p-3 mb-4">
                    <p className="text-xs font-bold uppercase text-warning">Node is in maintenance mode</p>
                  </div>
                )}
                
                {node.status === "offline" && (
                  <div className="bg-danger/10 border-2 border-danger p-3 mb-4">
                    <p className="text-xs font-bold uppercase text-danger">Node is offline - cannot manage VMs</p>
                  </div>
                )}
                
                {/* Token Display */}
                <div className="bg-gray-100 border-2 border-black p-2 mb-4">
                  <div className="flex items-center justify-between">
                    <span className="text-[10px] font-bold text-gray-500 uppercase">Token</span>
                    <span className="font-mono text-xs font-medium">{node.token}</span>
                  </div>
                </div>
                
                {/* Actions */}
                <div className="flex items-center gap-2">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setEditNode(node)}
                    className="h-9 border-2 border-black"
                    title="Edit"
                  >
                    <Edit2 className="w-4 h-4" />
                    <span className="ml-1">Edit</span>
                  </Button>
                  
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setRegenerateToken(node)}
                    disabled={node.status !== "online" || !!actionLoading}
                    className="h-9 border-2 border-black"
                    title="Regenerate Token"
                  >
                    {actionLoading === `token-${node.id}` ? (
                      <Loader2 className="w-4 h-4 animate-spin" />
                    ) : (
                      <RefreshCw className="w-4 h-4" />
                    )}
                    <span className="ml-1">Token</span>
                  </Button>
                  
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleDeleteClick(node)}
                    disabled={!!actionLoading}
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

      {/* Import Section */}
      <div className="mt-8 bg-white border-4 border-black p-6 shadow-neo">
        <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
          Import
        </h2>
        <div className="flex items-center justify-between">
          <div>
            <p className="font-bold text-black">Virtualizor Import</p>
            <p className="text-sm text-gray-500">Import nodes and VMs from Virtualizor panel</p>
          </div>
          <Button variant="outline" className="gap-2">
            <Database className="w-4 h-4" />
            Import from Virtualizor
          </Button>
        </div>
      </div>
      
      {/* Delete Confirmation Dialog */}
      <ConfirmDialog
        open={!!deleteConfirm && !migrateDialog}
        title="Delete Node"
        message={`Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />
      
      {/* Migration Dialog */}
      <MigrationDialog
        open={!!migrateDialog}
        nodeName={migrateDialog?.name || ""}
        onMigrate={handleMigrateAndDelete}
        onCancel={() => setMigrateDialog(null)}
      />
      
      {/* Edit Dialog */}
      {editNode && (
        <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true">
          <button type="button" className="absolute inset-0 bg-black/50 cursor-default" onClick={() => setEditNode(null)} />
          <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
            <h3 className="text-xl font-black uppercase mb-4">Edit Node</h3>
            
            <div className="space-y-4 mb-6">
              <div>
                <label htmlFor="edit-node-name" className="block text-xs font-black uppercase text-gray-500 mb-1">Name</label>
                <Input
                  id="edit-node-name"
                  type="text"
                  value={editNode.name}
                  onChange={(e) => setEditNode({ ...editNode, name: e.target.value })}
                  className="border-2 border-black"
                />
              </div>
              <div>
                <label htmlFor="edit-node-ip" className="block text-xs font-black uppercase text-gray-500 mb-1">IP Address</label>
                <Input
                  id="edit-node-ip"
                  type="text"
                  value={editNode.ip}
                  onChange={(e) => setEditNode({ ...editNode, ip: e.target.value })}
                  className="border-2 border-black"
                />
              </div>
            </div>
            
            <div className="flex gap-3 justify-end">
              <Button variant="ghost" onClick={() => setEditNode(null)} className="border-2 border-black">
                Cancel
              </Button>
              <Button onClick={() => handleEdit(editNode)} disabled={!!actionLoading}>
                {actionLoading === `edit-${editNode.id}` && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
                Save Changes
              </Button>
            </div>
          </div>
        </div>
      )}
      
      {/* Regenerate Token Confirmation */}
      <ConfirmDialog
        open={!!regenerateToken}
        title="Regenerate Token"
        message={`Are you sure you want to regenerate the token for "${regenerateToken?.name}"? The old token will become invalid.`}
        confirmLabel="Regenerate"
        onConfirm={() => regenerateToken && handleRegenerateToken(regenerateToken)}
        onCancel={() => setRegenerateToken(null)}
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