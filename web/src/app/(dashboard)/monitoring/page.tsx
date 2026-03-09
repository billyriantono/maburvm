'use client'

import { useState, useEffect, useMemo } from "react"
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
  Zap,
  Loader2,
  AlertCircle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { useNodes, useNodeMetrics } from "@/lib/hooks/use-nodes"
import { useVMs } from "@/lib/hooks/use-vms"
import type { Node, NodeMetrics as NodeMetricsType } from "@/types"

// Types
type AlertSeverity = "critical" | "warning" | "info"

interface DerivedAlert {
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

function getNodeDisplayStatus(node: Node): "online" | "offline" | "warning" {
  if (node.status === "offline") return "offline"
  if (node.status === "maintenance") return "warning"
  return "online"
}

// Status badge component
function StatusBadge({ status }: { status: "online" | "offline" | "warning" }) {
  const config = {
    online: { bg: "bg-success", label: "Online" },
    offline: { bg: "bg-danger", label: "Offline" },
    warning: { bg: "bg-warning", label: "Warning" }
  }
  
  const { bg, label } = config[status]
  
  return (
    <span className={cn("inline-flex items-center px-2 py-0.5 text-[10px] font-black uppercase tracking-wider border border-black", bg)}>
      {label}
    </span>
  )
}

// Resource bar component
function ResourceBar({ value, label }: { value: number; label: string }) {
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
      <span className="text-xs font-black w-10 text-right">{Math.round(value)}%</span>
    </div>
  )
}

// Node metrics card — fetches its own metrics
function NodeMetricsCard({ node }: { node: Node }) {
  const displayStatus = getNodeDisplayStatus(node)
  const isOnline = displayStatus !== "offline"
  const { data: metrics } = useNodeMetrics(isOnline ? node.id : "")
  
  const cpuUsage = metrics?.cpu_percent ?? 0
  const memUsage = metrics?.memory_used_percent ?? 0
  const diskUsage = metrics?.disk_used_percent ?? 0
  const networkIn = metrics?.network_rx_bytes_per_sec ?? 0
  const networkOut = metrics?.network_tx_bytes_per_sec ?? 0
  const vmCount = metrics?.running_vm_count ?? 0
  
  return (
    <div className="bg-white border-4 border-black shadow-neo">
      {/* Card Header */}
      <div className="border-b-4 border-black bg-gray-50 p-4 flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Server className="w-5 h-5 text-gray-500" />
          <span className="text-xl font-black uppercase">{node.name}</span>
        </div>
        <StatusBadge status={displayStatus} />
      </div>
      
      {/* Card Body */}
      <div className="p-4">
        {!isOnline ? (
          <div className="py-6 text-center">
            <AlertCircle className="w-8 h-8 text-gray-300 mx-auto mb-2" />
            <p className="text-sm text-gray-400 font-bold uppercase">Node offline</p>
          </div>
        ) : !metrics ? (
          <div className="space-y-3 mb-4">
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-full" />
          </div>
        ) : (
          <>
            {/* Resource Bars */}
            <div className="space-y-3 mb-4">
              <ResourceBar value={cpuUsage} label="CPU" />
              <ResourceBar value={memUsage} label="Memory" />
              <ResourceBar value={diskUsage} label="Disk" />
            </div>
            
            {/* Network I/O */}
            <div className="flex items-center gap-6 mb-4 pb-4 border-b border-gray-200">
              <div className="flex items-center gap-2">
                <Network className="w-4 h-4 text-success" />
                <span className="text-xs font-medium text-gray-500">IN</span>
                <span className="text-sm font-black">{formatBytes(networkIn)}/s</span>
              </div>
              <div className="flex items-center gap-2">
                <Network className="w-4 h-4 text-primary rotate-180" />
                <span className="text-xs font-medium text-gray-500">OUT</span>
                <span className="text-sm font-black">{formatBytes(networkOut)}/s</span>
              </div>
            </div>
            
            {/* VM Count & IP */}
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Database className="w-4 h-4 text-gray-400" />
                <span className="text-sm font-medium text-gray-500">VMs:</span>
                <span className="text-sm font-black">{vmCount}</span>
              </div>
              <div className="flex items-center gap-2">
                <Network className="w-4 h-4 text-gray-400" />
                <span className="text-sm font-mono text-gray-500">{node.ip_address}</span>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

// Derive alerts from node states
function deriveAlerts(nodes: Node[], metricsMap: Map<string, NodeMetricsType>): DerivedAlert[] {
  const alerts: DerivedAlert[] = []
  const now = new Date().toISOString()
  
  for (const node of nodes) {
    if (node.status === "offline") {
      alerts.push({
        id: `offline-${node.id}`,
        severity: "critical",
        message: `${node.name} is offline`,
        node: node.name,
        timestamp: "now",
        acknowledged: false,
      })
    }
    
    if (node.status === "maintenance") {
      alerts.push({
        id: `maint-${node.id}`,
        severity: "info",
        message: `${node.name} is in maintenance mode`,
        node: node.name,
        timestamp: "now",
        acknowledged: false,
      })
    }
    
    const metrics = metricsMap.get(node.id)
    if (metrics) {
      if (metrics.cpu_percent >= 90) {
        alerts.push({
          id: `cpu-crit-${node.id}`,
          severity: "critical",
          message: `High CPU usage on ${node.name} (${Math.round(metrics.cpu_percent)}%)`,
          node: node.name,
          timestamp: "now",
          acknowledged: false,
        })
      } else if (metrics.cpu_percent >= 80) {
        alerts.push({
          id: `cpu-warn-${node.id}`,
          severity: "warning",
          message: `CPU usage above 80% on ${node.name} (${Math.round(metrics.cpu_percent)}%)`,
          node: node.name,
          timestamp: "now",
          acknowledged: false,
        })
      }
      
      if (metrics.memory_used_percent >= 90) {
        alerts.push({
          id: `mem-crit-${node.id}`,
          severity: "critical",
          message: `High memory usage on ${node.name} (${Math.round(metrics.memory_used_percent)}%)`,
          node: node.name,
          timestamp: "now",
          acknowledged: false,
        })
      } else if (metrics.memory_used_percent >= 80) {
        alerts.push({
          id: `mem-warn-${node.id}`,
          severity: "warning",
          message: `Memory usage above 80% on ${node.name} (${Math.round(metrics.memory_used_percent)}%)`,
          node: node.name,
          timestamp: "now",
          acknowledged: false,
        })
      }
      
      if (metrics.disk_used_percent >= 90) {
        alerts.push({
          id: `disk-crit-${node.id}`,
          severity: "critical",
          message: `Disk usage critical on ${node.name} (${Math.round(metrics.disk_used_percent)}%)`,
          node: node.name,
          timestamp: "now",
          acknowledged: false,
        })
      } else if (metrics.disk_used_percent >= 70) {
        alerts.push({
          id: `disk-warn-${node.id}`,
          severity: "warning",
          message: `Disk usage approaching threshold on ${node.name} (${Math.round(metrics.disk_used_percent)}%)`,
          node: node.name,
          timestamp: "now",
          acknowledged: false,
        })
      }
    }
  }
  
  // Sort: critical first, then warning, then info
  const severityOrder: Record<string, number> = { critical: 0, warning: 1, info: 2 }
  alerts.sort((a, b) => severityOrder[a.severity] - severityOrder[b.severity])
  
  return alerts
}

// Toast component
function Toast({ message, type, onClose }: { message: string; type: "success" | "error" | "info"; onClose: () => void }) {
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
  const { data: nodes, isLoading, error, refetch } = useNodes()
  const { data: vmsData } = useVMs({ pageSize: 1 }) // Just to get total count
  const [severityFilter, setSeverityFilter] = useState<string>("all")
  const [acknowledgedIds, setAcknowledgedIds] = useState<Set<string>>(new Set())
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" | "info" } | null>(null)
  
  // Build alerts from node statuses (metrics-based alerts are generated per-node inside cards)
  const alerts = useMemo(() => {
    if (!nodes) return []
    return deriveAlerts(nodes, new Map())
  }, [nodes])
  
  // Filter alerts
  const filteredAlerts = useMemo(() => {
    let result = alerts.map(a => ({
      ...a,
      acknowledged: acknowledgedIds.has(a.id) || a.acknowledged,
    }))
    if (severityFilter !== "all") {
      result = result.filter(a => a.severity === severityFilter)
    }
    return result
  }, [alerts, severityFilter, acknowledgedIds])
  
  // Calculate stats from nodes
  const onlineNodes = nodes?.filter(n => n.status !== "offline") || []
  const totalNodes = nodes?.length ?? 0
  const totalVMs = vmsData?.total ?? 0
  
  // Handle acknowledge
  const handleAcknowledge = (alertId: string) => {
    setAcknowledgedIds(prev => {
      const next = new Set(prev)
      next.add(alertId)
      return next
    })
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
  
  // Loading
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="mb-6">
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
            <Activity className="w-8 h-8" />
            Monitoring
          </h1>
          <Skeleton className="h-5 w-48 mt-1" />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-24 border-4 border-black" />)}
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
          {[1,2].map(i => <Skeleton key={i} className="h-48 border-4 border-black" />)}
        </div>
      </div>
    )
  }
  
  // Error
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="mb-6">
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
            <Activity className="w-8 h-8" />
            Monitoring
          </h1>
        </div>
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Failed to load monitoring data</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()}>Retry</Button>
        </div>
      </div>
    )
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
          <p className="text-3xl font-black text-black">{totalNodes}</p>
          <p className="text-xs text-gray-500 font-medium mt-1">{onlineNodes.length} online</p>
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
            <span className="text-xs font-black uppercase text-gray-500">Online</span>
            <CheckCircle className="w-4 h-4 text-success" />
          </div>
          <p className="text-3xl font-black text-success">{onlineNodes.length}</p>
        </div>
        
        <div className="bg-white border-4 border-black shadow-neo p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Active Alerts</span>
            <Zap className="w-4 h-4 text-warning" />
          </div>
          <p className="text-3xl font-black text-warning">{alerts.filter(a => !acknowledgedIds.has(a.id)).length}</p>
        </div>
      </div>
      
      {/* Node Metrics Cards */}
      {nodes && nodes.length > 0 ? (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
          {nodes.map((node) => (
            <NodeMetricsCard key={node.id} node={node} />
          ))}
        </div>
      ) : (
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center mb-6">
          <Server className="w-12 h-12 text-gray-300 mx-auto mb-4" />
          <p className="text-gray-500 font-bold uppercase">No nodes registered</p>
        </div>
      )}
      
      {/* Alerts Section */}
      <div className="bg-white border-4 border-black shadow-neo">
        {/* Alerts Header */}
        <div className="bg-black text-white font-black uppercase p-4 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Zap className="w-5 h-5" />
            Alerts ({alerts.length})
          </div>
          
          {/* Filter Buttons */}
          <div className="flex items-center gap-2">
            {(["all", "critical", "warning", "info"] as const).map(filter => {
              const filterColors: Record<string, string> = {
                all: "border-white",
                critical: "border-danger",
                warning: "border-warning",
                info: "border-primary",
              }
              const activeBg: Record<string, string> = {
                all: "bg-white text-black",
                critical: "bg-danger text-white",
                warning: "bg-warning text-black",
                info: "bg-primary text-black",
              }
              return (
                <button
                  key={filter}
                  onClick={() => setSeverityFilter(filter)}
                  className={cn(
                    "px-3 py-1 text-xs font-bold uppercase border-2 transition-all",
                    severityFilter === filter 
                      ? cn(activeBg[filter], filterColors[filter])
                      : cn("bg-transparent text-white", `${filterColors[filter]}/30`, `hover:${filterColors[filter]}`)
                  )}
                >
                  {filter}
                </button>
              )
            })}
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
                    <Icon className={cn("w-5 h-5", alert.severity === "critical" || alert.severity === "warning" ? "text-black" : config.text)} />
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
