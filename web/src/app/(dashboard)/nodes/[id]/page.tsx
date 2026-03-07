"use client"

import { useState, useEffect } from "react"
import Link from "next/link"
import { useParams } from "next/navigation"
import { 
  Server, 
  ArrowLeft,
  Play,
  Square,
  RotateCcw,
  Terminal,
  Trash2,
  Edit2,
  RefreshCw,
  Activity,
  HardDrive,
  Cpu,
  Network,
  Clock,
  AlertTriangle,
  CheckCircle,
  XCircle,
  Database,
  MemoryStick,
  Gauge,
  Loader2,
  Copy,
  Check,
  Upload,
  Search,
  FileWarning,
  FolderOpen,
  Circle,
  HelpCircle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"

// Types
type VMStatus = "running" | "stopped" | "suspended"
type NodeStatus = "online" | "offline" | "maintenance"
type ImportStatus = "pending" | "importing" | "completed" | "error"

interface ImportCandidate {
  id: string
  name: string
  hostname: string
  vcpu: number
  ramGB: number
  diskPath: string
  diskSizeGB: number
  status: ImportStatus
  error?: string
  uuid?: string
  existingVM?: string
  diskExists: boolean
  hasUuidConflict: boolean
}

interface NodeHealth {
  cpu: number
  memory: number
  disk: number
  networkIn: number
  networkOut: number
}

interface VM {
  id: string
  name: string
  hostname: string
  status: VMStatus
  ip: string
  cpuCores: number
  ramGB: number
  user?: string
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
  vms: VM[]
}

// Mock data
const mockNodes: Record<string, Node> = {
  "1": { 
    id: "1", 
    name: "node-01", 
    ip: "10.0.1.100", 
    status: "online", 
    vmCount: 12,
    runningVMs: 10,
    health: { cpu: 45, memory: 62, disk: 38, networkIn: 125, networkOut: 89 },
    token: "tok_abc123xyz",
    lastSeen: "2 minutes ago",
    createdAt: "2024-01-10",
    vms: [
      { id: "v1", name: "web-server-01", hostname: "web01.internal", status: "running", ip: "10.0.1.10", cpuCores: 4, ramGB: 8, user: "admin" },
      { id: "v2", name: "web-server-02", hostname: "web02.internal", status: "running", ip: "10.0.1.11", cpuCores: 4, ramGB: 8, user: "admin" },
      { id: "v3", name: "api-gateway", hostname: "api01.internal", status: "running", ip: "10.0.1.20", cpuCores: 2, ramGB: 4, user: "admin" },
      { id: "v4", name: "monitoring", hostname: "mon01.internal", status: "running", ip: "10.0.1.30", cpuCores: 2, ramGB: 2, user: "ops" },
      { id: "v5", name: "test-vm-01", hostname: "test01.internal", status: "stopped", ip: "10.0.1.40", cpuCores: 2, ramGB: 4, user: "dev" },
      { id: "v6", name: "test-vm-02", hostname: "test02.internal", status: "stopped", ip: "10.0.1.41", cpuCores: 4, ramGB: 8, user: "dev" },
    ]
  },
  "2": { 
    id: "2", 
    name: "node-02", 
    ip: "10.0.2.100", 
    status: "online", 
    vmCount: 8,
    runningVMs: 6,
    health: { cpu: 72, memory: 85, disk: 55, networkIn: 234, networkOut: 156 },
    token: "tok_def456uvw",
    lastSeen: "1 minute ago",
    createdAt: "2024-01-12",
    vms: [
      { id: "v7", name: "db-primary", hostname: "db01.internal", status: "running", ip: "10.0.2.10", cpuCores: 8, ramGB: 32, user: "dba" },
      { id: "v8", name: "db-replica", hostname: "db02.internal", status: "running", ip: "10.0.2.11", cpuCores: 8, ramGB: 32, user: "dba" },
      { id: "v9", name: "cache-server", hostname: "cache01.internal", status: "running", ip: "10.0.2.20", cpuCores: 2, ramGB: 4, user: "ops" },
    ]
  },
  "3": { 
    id: "3", 
    name: "node-03", 
    ip: "10.0.3.100", 
    status: "offline", 
    vmCount: 15,
    runningVMs: 0,
    health: { cpu: 0, memory: 0, disk: 0, networkIn: 0, networkOut: 0 },
    token: "tok_ghi789rst",
    lastSeen: "2 hours ago",
    createdAt: "2024-01-15",
    vms: []
  },
  "4": { 
    id: "4", 
    name: "node-04", 
    ip: "10.0.4.100", 
    status: "maintenance", 
    vmCount: 3,
    runningVMs: 0,
    health: { cpu: 5, memory: 12, disk: 22, networkIn: 0, networkOut: 0 },
    token: "tok_jkl012mno",
    lastSeen: "5 minutes ago",
    createdAt: "2024-01-18",
    vms: []
  },
  "5": { 
    id: "5", 
    name: "node-05", 
    ip: "10.0.5.100", 
    status: "maintenance", 
    vmCount: 5,
    runningVMs: 0,
    health: { cpu: 2, memory: 8, disk: 15, networkIn: 0, networkOut: 0 },
    token: "tok_pqr345stu",
    lastSeen: "10 minutes ago",
    createdAt: "2024-01-20",
    vms: []
  },
  "6": { 
    id: "6", 
    name: "node-06", 
    ip: "10.0.6.100", 
    status: "online", 
    vmCount: 10,
    runningVMs: 8,
    health: { cpu: 58, memory: 73, disk: 41, networkIn: 189, networkOut: 145 },
    token: "tok_vwx678yza",
    lastSeen: "10 seconds ago",
    createdAt: "2024-01-22",
    vms: []
  },
}

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
      <span className={`text-sm font-black uppercase ${text}`}>{label}</span>
    </div>
  )
}

// VM Status badge
function VMStatusBadge({ status }: { status: VMStatus }) {
  const colors = {
    running: "bg-[#CCFF00] text-black",
    stopped: "bg-[#FF4444] text-white",
    suspended: "bg-[#FFAA00] text-black",
  }
  
  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black ${colors[status]}`}>
      <span className={`w-1.5 h-1.5 mr-1.5 rounded-full ${status === "running" ? "bg-black animate-pulse" : "bg-current"}`} />
      {status}
    </span>
  )
}

// Resource gauge component
function ResourceGauge({ value, label, icon: Icon, color }: { value: number, label: string, icon: React.ElementType, color: string }) {
  const getColorClass = () => {
    if (value >= 90) return "bg-danger"
    if (value >= 70) return "bg-warning"
    return color
  }
  
  return (
    <div className="bg-white border-4 border-black p-4 shadow-neo">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <div className={`w-8 h-8 ${color} flex items-center justify-center border-2 border-black`}>
            <Icon className="w-4 h-4" />
          </div>
          <span className="text-xs font-black uppercase text-gray-500">{label}</span>
        </div>
        <span className="text-2xl font-black">{value}%</span>
      </div>
      <div className="h-3 bg-gray-200 border-2 border-black">
        <div 
          className={`h-full ${getColorClass()} transition-all duration-500`}
          style={{ width: `${value}%` }}
        />
      </div>
    </div>
  )
}

// Toast notification
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

export default function NodeDetailPage() {
  const params = useParams()
  const nodeId = params.id as string
  
  const [node, setNode] = useState<Node | null>(null)
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [copied, setCopied] = useState(false)
  
  const [activeTab, setActiveTab] = useState("overview")
  const [scanStatus, setScanStatus] = useState<"idle" | "scanning" | "complete">("idle")
  const [importCandidates, setImportCandidates] = useState<ImportCandidate[]>([])
  const [selectedVMs, setSelectedVMs] = useState<Set<string>>(new Set())
  const [isImporting, setIsImporting] = useState(false)
  const [importProgress, setImportProgress] = useState(0)
  const [importResults, setImportResults] = useState<{ success: number; failed: number } | null>(null)
  
  // Load node data
  useEffect(() => {
    // Simulate API call
    setTimeout(() => {
      setNode(mockNodes[nodeId] || null)
      setLoading(false)
    }, 500)
  }, [nodeId])
  
  // Copy token to clipboard
  const copyToken = () => {
    if (node) {
      navigator.clipboard.writeText(node.token)
      setCopied(true)
      setToast({ message: "Token copied to clipboard", type: "success" })
      setTimeout(() => setCopied(false), 2000)
    }
  }
  
  // Handle VM actions
  const handleVMAction = async (vmId: string, action: "start" | "stop" | "restart") => {
    if (!node) return
    
    setActionLoading(`${vmId}-${action}`)
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1000))
    
    // Update local state
    setNode(prev => {
      if (!prev) return null
      return {
        ...prev,
        vms: prev.vms.map(vm => {
          if (vm.id !== vmId) return vm
          const statusMap: Record<string, VMStatus> = {
            start: "running",
            stop: "stopped",
            restart: "running",
          }
          return { ...vm, status: statusMap[action] }
        })
      }
    })
    
    setToast({ message: `VM ${action} successful`, type: "success" })
    setActionLoading(null)
  }
  
  // Handle regenerate token
  const handleRegenerateToken = async () => {
    if (!node) return
    
    setActionLoading("regenerate-token")
    
    await new Promise(resolve => setTimeout(resolve, 1500))
    
    const newToken = "tok_" + Math.random().toString(36).substring(2, 10) + Math.random().toString(36).substring(2, 6)
    
    setNode(prev => prev ? { ...prev, token: newToken } : null)
    setToast({ message: "Token regenerated successfully", type: "success" })
    setActionLoading(null)
  }
  
  const handleScan = async () => {
    setScanStatus("scanning")
    setImportResults(null)
    
    await new Promise(resolve => setTimeout(resolve, 2000))
    
    const mockCandidates: ImportCandidate[] = [
      {
        id: "cand-1",
        name: "web-server-old",
        hostname: "web-old.internal",
        vcpu: 4,
        ramGB: 8,
        diskPath: "/var/lib/libvirt/images/web-server-old.qcow2",
        diskSizeGB: 80,
        status: "pending",
        uuid: "550e8400-e29b-41d4-a716-446655440000",
        diskExists: true,
        hasUuidConflict: false,
      },
      {
        id: "cand-2",
        name: "db-backup",
        hostname: "db-backup.internal",
        vcpu: 8,
        ramGB: 32,
        diskPath: "/var/lib/libvirt/images/db-backup.qcow2",
        diskSizeGB: 500,
        status: "pending",
        uuid: "550e8400-e29b-41d4-a716-446655440001",
        diskExists: true,
        hasUuidConflict: true,
        existingVM: "db-primary",
      },
      {
        id: "cand-3",
        name: "test-vm",
        hostname: "test-old.internal",
        vcpu: 2,
        ramGB: 4,
        diskPath: "/var/lib/libvirt/images/test-vm.qcow2",
        diskSizeGB: 40,
        status: "pending",
        uuid: "550e8400-e29b-41d4-a716-446655440002",
        diskExists: false,
        hasUuidConflict: false,
      },
      {
        id: "cand-4",
        name: "cache-old",
        hostname: "cache-old.internal",
        vcpu: 2,
        ramGB: 4,
        diskPath: "/var/lib/libvirt/images/cache-old.qcow2",
        diskSizeGB: 25,
        status: "pending",
        uuid: "550e8400-e29b-41d4-a716-446655440003",
        diskExists: true,
        hasUuidConflict: false,
      },
      {
        id: "cand-5",
        name: "dev-box",
        hostname: "dev-old.internal",
        vcpu: 4,
        ramGB: 16,
        diskPath: "/var/lib/libvirt/images/dev-box.qcow2",
        diskSizeGB: 120,
        status: "pending",
        uuid: "550e8400-e29b-41d4-a716-446655440004",
        diskExists: true,
        hasUuidConflict: false,
      },
    ]
    
    setImportCandidates(mockCandidates)
    setScanStatus("complete")
    setToast({ message: `Found ${mockCandidates.length} import candidates`, type: "success" })
  }
  
  const handleToggleVM = (id: string) => {
    setSelectedVMs(prev => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }
  
  const handleSelectAll = () => {
    const validCandidates = importCandidates.filter(c => c.diskExists && !c.hasUuidConflict)
    if (selectedVMs.size === validCandidates.length) {
      setSelectedVMs(new Set())
    } else {
      setSelectedVMs(new Set(validCandidates.map(c => c.id)))
    }
  }
  
  const handleImport = async () => {
    if (selectedVMs.size === 0) return
    
    setIsImporting(true)
    setImportProgress(0)
    setImportResults(null)
    
    const selectedIds = Array.from(selectedVMs)
    let successCount = 0
    let failedCount = 0
    
    for (let i = 0; i < selectedIds.length; i++) {
      const id = selectedIds[i]
      
      setImportCandidates(prev => 
        prev.map(c => c.id === id ? { ...c, status: "importing" } : c)
      )
      
      await new Promise(resolve => setTimeout(resolve, 1500))
      
      const candidate = importCandidates.find(c => c.id === id)
      const success = candidate?.diskExists && !candidate?.hasUuidConflict
      
      if (success) {
        successCount++
        setImportCandidates(prev => 
          prev.map(c => c.id === id ? { ...c, status: "completed" } : c)
        )
      } else {
        failedCount++
        setImportCandidates(prev => 
          prev.map(c => c.id === id ? { ...c, status: "error", error: "Disk not found" } : c)
        )
      }
      
      setImportProgress(Math.round(((i + 1) / selectedIds.length) * 100))
    }
    
    setIsImporting(false)
    setImportResults({ success: successCount, failed: failedCount })
    setSelectedVMs(new Set())
    setToast({ 
      message: `Import complete: ${successCount} succeeded, ${failedCount} failed`, 
      type: failedCount > 0 ? "error" : "success" 
    })
  }
  
  if (loading) {
    return (
      <div className="max-w-7xl mx-auto flex items-center justify-center min-h-[400px]">
        <Loader2 className="w-8 h-8 animate-spin text-gray-400" />
      </div>
    )
  }
  
  if (!node) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <Server className="w-16 h-16 text-gray-300 mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Node Not Found</h2>
          <p className="text-gray-500 font-medium mb-6">The requested node does not exist.</p>
          <Link href="/nodes">
            <Button className="gap-2">
              <ArrowLeft className="w-4 h-4" />
              Back to Nodes
            </Button>
          </Link>
        </div>
      </div>
    )
  }
  
  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/nodes">
          <Button variant="ghost" size="icon" className="border-2 border-black">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <h1 className="text-3xl font-black uppercase tracking-tight text-black">
              {node.name}
            </h1>
            <StatusIndicator status={node.status} />
          </div>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
            {node.ip} • Created {node.createdAt}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" className="border-2 border-black gap-2">
            <Edit2 className="w-4 h-4" />
            Edit
          </Button>
          <Button 
            variant="ghost" 
            onClick={handleRegenerateToken}
            disabled={node.status !== "online" || !!actionLoading}
            className="border-2 border-black gap-2"
          >
            {actionLoading === "regenerate-token" ? (
              <Loader2 className="w-4 h-4 animate-spin" />
            ) : (
              <RefreshCw className="w-4 h-4" />
            )}
            Regenerate Token
          </Button>
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList className="mb-6">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="import">Import VMs</TabsTrigger>
        </TabsList>
        
        <TabsContent value="overview">
          {/* Quick Stats */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total VMs</span>
            <Database className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{node.vmCount}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Running</span>
            <span className="w-3 h-3 bg-success rounded-full animate-pulse" />
          </div>
          <p className="text-3xl font-black text-success">{node.runningVMs}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Stopped</span>
            <span className="w-3 h-3 bg-danger rounded-full" />
          </div>
          <p className="text-3xl font-black text-danger">{node.vmCount - node.runningVMs}</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Last Seen</span>
            <Clock className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-lg font-black text-black">{node.lastSeen}</p>
        </div>
      </div>

      {/* Resource Usage */}
      {node.status === "online" && (
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6">
          <ResourceGauge value={node.health.cpu} label="CPU Usage" icon={Cpu} color="bg-primary" />
          <ResourceGauge value={node.health.memory} label="Memory Usage" icon={MemoryStick} color="bg-secondary" />
          <ResourceGauge value={node.health.disk} label="Disk Usage" icon={HardDrive} color="bg-accent" />
        </div>
      )}

      {/* Network Stats */}
      {node.status === "online" && (
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
            Network Throughput
          </h2>
          <div className="grid grid-cols-2 gap-8">
            <div className="flex items-center gap-4">
              <div className="w-12 h-12 bg-success flex items-center justify-center border-2 border-black">
                <Network className="w-6 h-6" />
              </div>
              <div>
                <p className="text-xs font-black uppercase text-gray-500">Inbound</p>
                <p className="text-2xl font-black text-success">{node.health.networkIn} MB/s</p>
              </div>
            </div>
            <div className="flex items-center gap-4">
              <div className="w-12 h-12 bg-primary flex items-center justify-center border-2 border-black">
                <Network className="w-6 h-6" />
              </div>
              <div>
                <p className="text-xs font-black uppercase text-gray-500">Outbound</p>
                <p className="text-2xl font-black text-primary">{node.health.networkOut} MB/s</p>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Token Section */}
      <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
        <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
          Node Token
        </h2>
        <div className="flex items-center gap-4">
          <div className="flex-1 bg-gray-100 border-2 border-black p-3">
            <code className="font-mono text-sm font-medium">{node.token}</code>
          </div>
          <Button variant="ghost" onClick={copyToken} className="border-2 border-black gap-2">
            {copied ? <Check className="w-4 h-4 text-success" /> : <Copy className="w-4 h-4" />}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
        <p className="text-xs text-gray-500 mt-2">
          Use this token to register this node with the control panel.
        </p>
      </div>

      {/* Status Alerts */}
      {node.status === "maintenance" && (
        <div className="bg-warning/20 border-4 border-warning p-6 mb-6">
          <div className="flex items-center gap-3">
            <AlertTriangle className="w-6 h-6 text-warning" />
            <div>
              <p className="font-black uppercase text-warning">Node in Maintenance Mode</p>
              <p className="text-sm font-medium">VM operations are disabled while in maintenance mode.</p>
            </div>
          </div>
        </div>
      )}
      
      {node.status === "offline" && (
        <div className="bg-danger/10 border-4 border-danger p-6 mb-6">
          <div className="flex items-center gap-3">
            <XCircle className="w-6 h-6 text-danger" />
            <div>
              <p className="font-black uppercase text-danger">Node Offline</p>
              <p className="text-sm font-medium">Cannot connect to this node. Check network connectivity and node status.</p>
            </div>
          </div>
        </div>
      )}

      {/* Running VMs */}
      <div className="bg-white border-4 border-black shadow-neo">
        <div className="p-4 border-b-4 border-black bg-gray-50">
          <h2 className="text-lg font-black uppercase tracking-tight text-black">
            Virtual Machines ({node.vms.length})
          </h2>
        </div>
        
        {node.vms.length === 0 ? (
          <div className="p-12 text-center">
            <Database className="w-12 h-12 text-gray-300 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase">No VMs on this node</p>
          </div>
        ) : (
          <div className="divide-y-2 divide-black">
            {node.vms.map((vm) => (
              <div key={vm.id} className="p-4 flex items-center justify-between hover:bg-gray-50">
                <div className="flex items-center gap-4">
                  <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                    <Server className="w-5 h-5" />
                  </div>
                  <div>
                    <p className="font-black text-black">{vm.name}</p>
                    <p className="text-xs text-gray-500 font-medium">{vm.hostname} • {vm.ip}</p>
                  </div>
                </div>
                
                <div className="flex items-center gap-6">
                  {/* Resources */}
                  <div className="flex items-center gap-3">
                    <div className="flex items-center gap-1">
                      <div className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-[10px] font-black">
                        {vm.cpuCores}
                      </div>
                      <span className="text-xs text-gray-500">CPU</span>
                    </div>
                    <div className="flex items-center gap-1">
                      <div className="w-6 h-6 bg-secondary flex items-center justify-center border border-black text-[10px] font-black">
                        {vm.ramGB}
                      </div>
                      <span className="text-xs text-gray-500">GB</span>
                    </div>
                  </div>
                  
                  {/* Status */}
                  <VMStatusBadge status={vm.status} />
                  
                  {/* Actions */}
                  <div className="flex items-center gap-1">
                    <Button
                      variant="success"
                      size="sm"
                      onClick={() => handleVMAction(vm.id, "start")}
                      disabled={vm.status === "running" || node.status !== "online" || !!actionLoading}
                      className="h-8 w-8 p-0"
                      title="Start"
                    >
                      {actionLoading === `${vm.id}-start` ? (
                        <Loader2 className="w-4 h-4 animate-spin" />
                      ) : (
                        <Play className="w-4 h-4" />
                      )}
                    </Button>
                    
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => handleVMAction(vm.id, "stop")}
                      disabled={vm.status === "stopped" || node.status !== "online" || !!actionLoading}
                      className="h-8 w-8 p-0"
                      title="Stop"
                    >
                      {actionLoading === `${vm.id}-stop` ? (
                        <Loader2 className="w-4 h-4 animate-spin" />
                      ) : (
                        <Square className="w-4 h-4" />
                      )}
                    </Button>
                    
                    <Button
                      variant="warning"
                      size="sm"
                      onClick={() => handleVMAction(vm.id, "restart")}
                      disabled={vm.status !== "running" || node.status !== "online" || !!actionLoading}
                      className="h-8 w-8 p-0"
                      title="Restart"
                    >
                      {actionLoading === `${vm.id}-restart` ? (
                        <Loader2 className="w-4 h-4 animate-spin" />
                      ) : (
                        <RotateCcw className="w-4 h-4" />
                      )}
                    </Button>
                    
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={vm.status !== "running" || node.status !== "online" || !!actionLoading}
                      className="h-8 w-8 p-0"
                      title="Console"
                    >
                      <Terminal className="w-4 h-4" />
                    </Button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
        </TabsContent>
        
        <TabsContent value="import">
          <div className="bg-white border-4 border-black shadow-neo">
            <div className="p-4 border-b-4 border-black bg-gray-50 flex items-center justify-between">
              <div>
                <h2 className="text-lg font-black uppercase tracking-tight text-black">
                  Import Virtual Machines
                </h2>
                <p className="text-xs text-gray-500 font-medium">
                  Scan this node for existing Virtualizor VMs to import
                </p>
              </div>
              <Button 
                onClick={handleScan}
                disabled={scanStatus === "scanning" || node.status !== "online"}
                className="gap-2"
              >
                {scanStatus === "scanning" ? (
                  <Loader2 className="w-4 h-4 animate-spin" />
                ) : (
                  <Search className="w-4 h-4" />
                )}
                Scan for VMs
              </Button>
            </div>

            {/* Scan Status */}
            <div className="p-6 border-b-4 border-black">
              {scanStatus === "idle" && (
                <div className="text-center py-8">
                  <FolderOpen className="w-12 h-12 text-gray-300 mx-auto mb-4" />
                  <p className="text-gray-500 font-bold uppercase">Click "Scan for VMs" to search for import candidates</p>
                </div>
              )}
              
              {scanStatus === "scanning" && (
                <div className="text-center py-8">
                  <Loader2 className="w-12 h-12 text-primary mx-auto mb-4 animate-spin" />
                  <p className="text-black font-black uppercase text-lg">Scanning...</p>
                  <p className="text-gray-500 font-medium">Searching for Virtualizor VMs on this node</p>
                </div>
              )}
              
              {scanStatus === "complete" && (
                <div className="flex items-center gap-2 mb-4">
                  <CheckCircle className="w-5 h-5 text-success" />
                  <span className="font-bold text-black">Found {importCandidates.length} VMs</span>
                </div>
              )}
            </div>

            {/* Import Candidates Table */}
            {scanStatus === "complete" && importCandidates.length > 0 && (
              <>
                <div className="overflow-x-auto">
                  <table className="w-full">
                    <thead className="bg-gray-100 border-b-4 border-black">
                      <tr>
                        <th className="p-4 text-left">
                          <Checkbox
                            checked={selectedVMs.size === importCandidates.filter(c => c.diskExists && !c.hasUuidConflict).length && selectedVMs.size > 0}
                            onCheckedChange={handleSelectAll}
                          />
                        </th>
                        <th className="p-4 text-left text-xs font-black uppercase">VM Name</th>
                        <th className="p-4 text-left text-xs font-black uppercase">Specs</th>
                        <th className="p-4 text-left text-xs font-black uppercase">Disk Path</th>
                        <th className="p-4 text-left text-xs font-black uppercase">Warnings</th>
                        <th className="p-4 text-left text-xs font-black uppercase">Status</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y-2 divide-black">
                      {importCandidates.map((candidate) => (
                        <tr 
                          key={candidate.id} 
                          className={`hover:bg-gray-50 ${!candidate.diskExists || candidate.hasUuidConflict ? "bg-gray-50" : ""}`}
                        >
                          <td className="p-4">
                            <Checkbox
                              checked={selectedVMs.has(candidate.id)}
                              onCheckedChange={() => handleToggleVM(candidate.id)}
                              disabled={!candidate.diskExists || candidate.hasUuidConflict}
                            />
                          </td>
                          <td className="p-4">
                            <div className="flex items-center gap-3">
                              <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                                <Server className="w-5 h-5" />
                              </div>
                              <div>
                                <p className="font-black text-black">{candidate.name}</p>
                                <p className="text-xs text-gray-500 font-medium">{candidate.hostname}</p>
                              </div>
                            </div>
                          </td>
                          <td className="p-4">
                            <div className="flex items-center gap-3">
                              <div className="flex items-center gap-1">
                                <div className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-[10px] font-black">
                                  {candidate.vcpu}
                                </div>
                                <span className="text-xs text-gray-500">vCPU</span>
                              </div>
                              <div className="flex items-center gap-1">
                                <div className="w-6 h-6 bg-secondary flex items-center justify-center border border-black text-[10px] font-black">
                                  {candidate.ramGB}
                                </div>
                                <span className="text-xs text-gray-500">GB</span>
                              </div>
                              <div className="flex items-center gap-1">
                                <div className="w-6 h-6 bg-accent flex items-center justify-center border border-black text-[10px] font-black">
                                  {candidate.diskSizeGB}
                                </div>
                                <span className="text-xs text-gray-500">GB</span>
                              </div>
                            </div>
                          </td>
                          <td className="p-4">
                            <code className="text-xs font-mono bg-gray-100 px-2 py-1 border border-black">
                              {candidate.diskPath}
                            </code>
                          </td>
                          <td className="p-4">
                            <div className="flex flex-col gap-1">
                              {!candidate.diskExists && (
                                <div className="flex items-center gap-1 text-danger text-xs font-bold uppercase">
                                  <FileWarning className="w-3 h-3" />
                                  Disk not found
                                </div>
                              )}
                              {candidate.hasUuidConflict && (
                                <div className="flex items-center gap-1 text-warning text-xs font-bold uppercase">
                                  <AlertTriangle className="w-3 h-3" />
                                  UUID conflict: {candidate.existingVM}
                                </div>
                              )}
                            </div>
                          </td>
                          <td className="p-4">
                            {candidate.status === "pending" && (
                              <span className="inline-flex items-center gap-1 px-2 py-1 text-[10px] font-black uppercase border border-black bg-gray-100">
                                <HelpCircle className="w-3 h-3" />
                                Pending
                              </span>
                            )}
                            {candidate.status === "importing" && (
                              <span className="inline-flex items-center gap-1 px-2 py-1 text-[10px] font-black uppercase border border-black bg-primary text-white">
                                <Loader2 className="w-3 h-3 animate-spin" />
                                Importing
                              </span>
                            )}
                            {candidate.status === "completed" && (
                              <span className="inline-flex items-center gap-1 px-2 py-1 text-[10px] font-black uppercase border border-black bg-success text-black">
                                <CheckCircle className="w-3 h-3" />
                                Completed
                              </span>
                            )}
                            {candidate.status === "error" && (
                              <span className="inline-flex items-center gap-1 px-2 py-1 text-[10px] font-black uppercase border border-black bg-danger text-white">
                                <XCircle className="w-3 h-3" />
                                Error
                              </span>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>

                {/* Import Actions */}
                <div className="p-4 border-t-4 border-black bg-gray-50 flex items-center justify-between">
                  <div className="text-sm font-medium text-gray-500">
                    {selectedVMs.size} VM{selectedVMs.size !== 1 ? "s" : ""} selected
                  </div>
                  <Button
                    onClick={handleImport}
                    disabled={selectedVMs.size === 0 || isImporting}
                    className="gap-2"
                  >
                    {isImporting ? (
                      <>
                        <Loader2 className="w-4 h-4 animate-spin" />
                        Importing... {importProgress}%
                      </>
                    ) : (
                      <>
                        <Upload className="w-4 h-4" />
                        Import Selected VMs
                      </>
                    )}
                  </Button>
                </div>

                {/* Progress Bar */}
                {isImporting && (
                  <div className="p-4 border-t-4 border-black">
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-xs font-black uppercase text-gray-500">Import Progress</span>
                      <span className="text-lg font-black text-primary">{importProgress}%</span>
                    </div>
                    <div className="h-4 bg-gray-200 border-2 border-black">
                      <div 
                        className="h-full bg-primary transition-all duration-300"
                        style={{ width: `${importProgress}%` }}
                      />
                    </div>
                  </div>
                )}

                {/* Results Summary */}
                {importResults && (
                  <div className="p-6 border-t-4 border-black bg-gray-50">
                    <h3 className="text-lg font-black uppercase mb-4">Import Results</h3>
                    <div className="grid grid-cols-2 gap-4">
                      <div className="bg-success/20 border-4 border-success p-4">
                        <div className="flex items-center gap-2 mb-2">
                          <CheckCircle className="w-5 h-5 text-success" />
                          <span className="text-xs font-black uppercase text-success">Successful</span>
                        </div>
                        <p className="text-3xl font-black text-success">{importResults.success}</p>
                      </div>
                      <div className="bg-danger/20 border-4 border-danger p-4">
                        <div className="flex items-center gap-2 mb-2">
                          <XCircle className="w-5 h-5 text-danger" />
                          <span className="text-xs font-black uppercase text-danger">Failed</span>
                        </div>
                        <p className="text-3xl font-black text-danger">{importResults.failed}</p>
                      </div>
                    </div>
                  </div>
                )}
              </>
            )}

            {/* Empty State */}
            {scanStatus === "complete" && importCandidates.length === 0 && (
              <div className="p-12 text-center">
                <Search className="w-12 h-12 text-gray-300 mx-auto mb-4" />
                <p className="text-gray-500 font-bold uppercase">No import candidates found</p>
                <p className="text-xs text-gray-400 mt-2">This node doesn't have any Virtualizor VMs to import</p>
              </div>
            )}
          </div>
        </TabsContent>
      </Tabs>

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