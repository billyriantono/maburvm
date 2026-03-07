'use client'

import { useState, useEffect } from "react"
import { 
  Activity, 
  AlertTriangle, 
  CheckCircle, 
  Clock, 
  Info, 
  Server, 
  Cpu, 
  HardDrive,
  Network,
  Database,
  Zap
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

// Types
type NodeStatus = "online" | "offline" | "warning"
type AlertSeverity = "critical" | "warning" | "info"

interface NodeMetric {
  id: string
  name: string
  cpuUsage: number
  memUsage: number
  diskUsage: number
  networkIn: number
  networkOut: number
  uptime: number
  vmCount: number
  status: NodeStatus
}

interface Alert {
  id: string
  severity: AlertSeverity
  message: string
  node: string
  timestamp: string
  acknowledged: boolean
}

// Helper functions
function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i]
}

// Mock data
const initialNodeMetrics: NodeMetric[] = [
  {
    id: "1",
    name: "node-01",
    cpuUsage: 45,
    memUsage: 62,
    diskUsage: 38,
    networkIn: 125.5,
    networkOut: 89.2,
    uptime: 345600,
    vmCount: 12,
    status: "online"
  },
  {
    id: "2",
    name: "node-02",
    cpuUsage: 78,
    memUsage: 85,
    diskUsage: 55,
    networkIn: 234.1,
    networkOut: 156.8,
    uptime: 172800,
    vmCount: 8,
    status: "warning"
  },
  {
    id: "3",
    name: "node-03",
    cpuUsage: 32,
    memUsage: 48,
    diskUsage: 67,
    networkIn: 89.3,
    networkOut: 67.1,
    uptime: 518400,
    vmCount: 15,
    status: "online"
  },
  {
    id: "4",
    name: "node-04",
    cpuUsage: 0,
    memUsage: 0,
    diskUsage: 0,
    networkIn: 0,
    networkOut: 0,
    uptime: 0,
    vmCount: 0,
    status: "offline"
  },
  {
    id: "5",
    name: "node-05",
    cpuUsage: 58,
    memUsage: 71,
    diskUsage: 42,
    networkIn: 145.7,
    networkOut: 112.3,
    uptime: 259200,
    vmCount: 10,
    status: "online"
  }
]

const initialAlerts: Alert[] = [
  {
    id: "1",
    severity: "critical",
    message: "High CPU usage detected on node-02 (92%)",
    node: "node-02",
    timestamp: "2 min ago",
    acknowledged: false
  },
  {
    id: "2",
    severity: "warning",
    message: "Memory usage above 80% on node-02",
    node: "node-02",
    timestamp: "5 min ago",
    acknowledged: false
  },
  {
    id: "3",
    severity: "critical",
    message: "Node-04 is offline",
    node: "node-04",
    timestamp: "15 min ago",
    acknowledged: false
  },
  {
    id: "4",
    severity: "warning",
    message: "Disk usage approaching 70% on node-03",
    node: "node-03",
    timestamp: "30 min ago",
    acknowledged: true
  },
  {
    id: "5",
    severity: "info",
    message: "Scheduled maintenance window starting in 2 hours",
    node: "All",
    timestamp: "1 hour ago",
    acknowledged: true
  },
  {
    id: "6",
    severity: "info",
    message: "Backup completed successfully for node-01",
    node: "node-01",
    timestamp: "2 hours ago",
    acknowledged: true
  },
  {
    id: "7",
    severity: "warning",
    message: "Network latency spike detected on node-05",
    node: "node-05",
    timestamp: "3 hours ago",
    acknowledged: false
  },
  {
    id: "8",
    severity: "info",
    message: "New VM template available",
    node: "All",
    timestamp: "4 hours ago",
    acknowledged: true
  }
]

// Status badge component
function StatusBadge({ status }: { status: NodeStatus }) {
  const config = {
    online: { bg: "bg-success", text: "text-success", label: "Online" },
    offline: { bg: "bg-danger", text: "text-danger", label: "Offline" },
    warning: { bg: "bg-warning", text: "text-warning", label: "Warning" }
  }
  
  const { bg, text, label } = config[status]
  
  return (
    <span className={cn("inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black", bg, text)}>
      {label}
    </span>
  )
}

// Resource bar component
function ResourceBar({ value, label, showValue = true }: { value: number, label: string, showValue?: boolean }) {
  const getColorClass = () => {
    if (value >= 90) return "bg-danger"
    if (value >= 70) return "bg-warning"
    if (value === 0) return "bg-gray-300"
    return "bg-success"
  }
  
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs font-medium text-gray-500 w-16">{label}</span>
      <div className="flex-1 h-3 bg-gray-200 border border-black">
        <div 
          className={cn("h-full transition-all duration-500", getColorClass())} 
          style={{ width: `${Math.min(value, 100)}%` }}
        />
      </div>
      {showValue && <span className="text-xs font-black w-10 text-right">{value}%</span>}
    </div>
  )
}

// Toast component
function Toast({ message, type, onClose }: { message: string, type: "success" | "error" | "info", onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  
  return (
    <div className={cn(
      "fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo",
      type === "success" && "bg-success",
      type === "error" && "bg-danger text-white",
      type === "info" && "bg-primary"
    )}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

export default function MonitoringPage() {
  const [nodeMetrics, setNodeMetrics] = useState<NodeMetric[]>(initialNodeMetrics)
  const [alerts, setAlerts] = useState<Alert[]>(initialAlerts)
  const [severityFilter, setSeverityFilter] = useState<string>("all")
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" | "info" } | null>(null)
  
  // Auto-refresh simulation - randomize values every 5 seconds
  useEffect(() => {
    const interval = setInterval(() => {
      setNodeMetrics(prev => prev.map(node => {
        if (node.status === "offline") return node
        
        const cpuVariation = Math.random() * 10 - 5
        const memVariation = Math.random() * 6 - 3
        
        return {
          ...node,
          cpuUsage: Math.max(0, Math.min(100, node.cpuUsage + cpuVariation)),
          memUsage: Math.max(0, Math.min(100, node.memUsage + memVariation)),
          networkIn: Math.max(0, node.networkIn + (Math.random() * 20 - 10)),
          networkOut: Math.max(0, node.networkOut + (Math.random() * 15 - 7.5))
        }
      }))
    }, 5000)
    
    return () => clearInterval(interval)
  }, [])
  
  // Filter alerts by severity
  const filteredAlerts = severityFilter === "all" 
    ? alerts 
    : alerts.filter(alert => alert.severity === severityFilter)
  
  // Calculate stats
  const onlineNodes = nodeMetrics.filter(n => n.status === "online" || n.status === "warning")
  const totalVMs = nodeMetrics.reduce((sum, n) => sum + n.vmCount, 0)
  const avgCpu = onlineNodes.length > 0 
    ? Math.round(onlineNodes.reduce((sum, n) => sum + n.cpuUsage, 0) / onlineNodes.length)
    : 0
  const avgMem = onlineNodes.length > 0 
    ? Math.round(onlineNodes.reduce((sum, n) => sum + n.memUsage, 0) / onlineNodes.length)
    : 0
  
  // Handle acknowledge
  const handleAcknowledge = (alertId: string) => {
    setAlerts(prev => prev.map(alert => 
      alert.id === alertId ? { ...alert, acknowledged: true } : alert
    ))
    setToast({ message: "Alert acknowledged", type: "success" })
  }
  
  // Get severity icon and color
  const getSeverityConfig = (severity: AlertSeverity) => {
    switch (severity) {
      case "critical":
        return { icon: AlertTriangle, bg: "bg-danger/10", border: "border-danger", text: "text-danger", iconBg: "bg-danger" }
      case "warning":
        return { icon: AlertTriangle, bg: "bg-warning/20", border: "border-warning", text: "text-warning", iconBg: "bg-warning" }
      case "info":
        return { icon: Info, bg: "bg-primary/10", border: "border-primary", text: "text-primary", iconBg: "bg-primary" }
    }
  }
  
  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
          <Activity className="w-8 h-8" />
          Monitoring
        </h1>
        <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
          Real-time cluster overview and alerts
        </p>
      </div>
      
      {/* Cluster Overview Stats */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white border-4 border-black shadow-neo p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total Nodes</span>
            <Server className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{nodeMetrics.length}</p>
        </div>
        
        <div className="bg-white border-4 border-black shadow-neo p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total VMs</span>
            <Database className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black text-black">{totalVMs}</p>
        </div>
        
        <div className="bg-white border-4 border-black shadow-neo p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Avg CPU Usage</span>
            <Cpu className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black">{avgCpu}%</p>
        </div>
        
        <div className="bg-white border-4 border-black shadow-neo p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Avg Memory</span>
            <HardDrive className="w-4 h-4 text-gray-400" />
          </div>
          <p className="text-3xl font-black">{avgMem}%</p>
        </div>
      </div>
      
      {/* Node Metrics Cards */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
        {nodeMetrics.map((node) => (
          <div key={node.id} className="bg-white border-4 border-black shadow-neo">
            {/* Card Header */}
            <div className="border-b-4 border-black bg-gray-50 p-4 flex items-center justify-between">
              <div className="flex items-center gap-3">
                <Server className="w-5 h-5 text-gray-500" />
                <span className="text-xl font-black uppercase">{node.name}</span>
              </div>
              <StatusBadge status={node.status} />
            </div>
            
            {/* Card Body */}
            <div className="p-4">
              {/* Resource Bars */}
              <div className="space-y-3 mb-4">
                <ResourceBar value={node.cpuUsage} label="CPU" />
                <ResourceBar value={node.memUsage} label="Memory" />
                <ResourceBar value={node.diskUsage} label="Disk" />
              </div>
              
              {/* Network I/O */}
              <div className="flex items-center gap-6 mb-4 pb-4 border-b border-gray-200">
                <div className="flex items-center gap-2">
                  <Network className="w-4 h-4 text-success" />
                  <span className="text-xs font-medium text-gray-500">IN</span>
                  <span className="text-sm font-black">{node.networkIn.toFixed(1)} MB/s</span>
                </div>
                <div className="flex items-center gap-2">
                  <Network className="w-4 h-4 text-primary rotate-180" />
                  <span className="text-xs font-medium text-gray-500">OUT</span>
                  <span className="text-sm font-black">{node.networkOut.toFixed(1)} MB/s</span>
                </div>
              </div>
              
              {/* VM Count & Uptime */}
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <Database className="w-4 h-4 text-gray-400" />
                  <span className="text-sm font-medium text-gray-500">VMs:</span>
                  <span className="text-sm font-black">{node.vmCount}</span>
                </div>
                <div className="flex items-center gap-2">
                  <Clock className="w-4 h-4 text-gray-400" />
                  <span className="text-sm font-medium text-gray-500">Uptime:</span>
                  <span className="text-sm font-black">
                    {node.status === "offline" ? "—" : formatUptime(node.uptime)}
                  </span>
                </div>
              </div>
            </div>
          </div>
        ))}
      </div>
      
      {/* Alerts Section */}
      <div className="bg-white border-4 border-black shadow-neo">
        {/* Alerts Header */}
        <div className="bg-black text-white font-black uppercase p-4 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Zap className="w-5 h-5" />
            Recent Alerts
          </div>
          
          {/* Filter Buttons */}
          <div className="flex items-center gap-2">
            <button
              onClick={() => setSeverityFilter("all")}
              className={cn(
                "px-3 py-1 text-xs font-bold uppercase border-2 transition-all",
                severityFilter === "all" 
                  ? "bg-white text-black border-white" 
                  : "bg-transparent text-white border-white/30 hover:border-white"
              )}
            >
              All
            </button>
            <button
              onClick={() => setSeverityFilter("critical")}
              className={cn(
                "px-3 py-1 text-xs font-bold uppercase border-2 transition-all",
                severityFilter === "critical" 
                  ? "bg-danger text-white border-danger" 
                  : "bg-transparent text-white border-danger/30 hover:border-danger"
              )}
            >
              Critical
            </button>
            <button
              onClick={() => setSeverityFilter("warning")}
              className={cn(
                "px-3 py-1 text-xs font-bold uppercase border-2 transition-all",
                severityFilter === "warning" 
                  ? "bg-warning text-black border-warning" 
                  : "bg-transparent text-white border-warning/30 hover:border-warning"
              )}
            >
              Warning
            </button>
            <button
              onClick={() => setSeverityFilter("info")}
              className={cn(
                "px-3 py-1 text-xs font-bold uppercase border-2 transition-all",
                severityFilter === "info" 
                  ? "bg-primary text-black border-primary" 
                  : "bg-transparent text-white border-primary/30 hover:border-primary"
              )}
            >
              Info
            </button>
          </div>
        </div>
        
        {/* Alert Rows */}
        <div className="divide-y divide-gray-200">
          {filteredAlerts.length === 0 ? (
            <div className="p-8 text-center">
              <CheckCircle className="w-12 h-12 text-success mx-auto mb-2" />
              <p className="font-bold text-gray-500 uppercase">No alerts to display</p>
            </div>
          ) : (
            filteredAlerts.map((alert) => {
              const config = getSeverityConfig(alert.severity)
              const Icon = config.icon
              
              return (
                <div 
                  key={alert.id} 
                  className={cn(
                    "p-4 flex items-center gap-4",
                    alert.acknowledged && "opacity-50"
                  )}
                >
                  {/* Severity Icon */}
                  <div className={cn("w-10 h-10 flex items-center justify-center border-2 border-black", config.iconBg)}>
                    <Icon className={cn("w-5 h-5", config.text)} />
                  </div>
                  
                  {/* Alert Content */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                      <span className={cn("text-[10px] font-black uppercase px-1.5 py-0.5 border border-black", config.iconBg, config.text)}>
                        {alert.severity}
                      </span>
                      <span className="text-xs font-medium text-gray-500">{alert.timestamp}</span>
                    </div>
                    <p className="font-medium text-black truncate">{alert.message}</p>
                    <p className="text-xs font-medium text-gray-500">Node: {alert.node}</p>
                  </div>
                  
                  {/* Acknowledge Button */}
                  {!alert.acknowledged && (
                    <Button 
                      variant="ghost" 
                      size="sm"
                      onClick={() => handleAcknowledge(alert.id)}
                      className="border-2 border-black gap-1 shrink-0"
                    >
                      <CheckCircle className="w-3 h-3" />
                      ACK
                    </Button>
                  )}
                </div>
              )
            })
          )}
        </div>
      </div>
      
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