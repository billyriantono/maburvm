'use client'

import { useState, useEffect, useMemo } from "react"
import {
  Activity,
  AlertTriangle,
  CheckCircle,
  Info,
  Server,
  Network,
  Database,
  Zap,
  AlertCircle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { useNodes, useNodeMetrics, useNodeMetricsHistory } from "@/lib/hooks/use-nodes"
import { useVMs } from "@/lib/hooks/use-vms"
import { Sparkline } from "@/components/ui/sparkline"
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
    online: { bg: "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-400 dark:border-emerald-900", label: "Online" },
    offline: { bg: "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-400 dark:border-red-900", label: "Offline" },
    warning: { bg: "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-400 dark:border-amber-900", label: "Warning" }
  }

  const { bg, label } = config[status]

  return (
    <span className={cn("inline-flex items-center px-2 py-0.5 text-[10px] font-medium rounded-full border", bg)}>
      {label}
    </span>
  )
}

// Resource bar component
function ResourceBar({ value, label }: { value: number; label: string }) {
  const getColorClass = () => {
    if (value >= 90) return "bg-red-500"
    if (value >= 70) return "bg-amber-500"
    if (value === 0) return "bg-muted-foreground/30"
    return "bg-emerald-500"
  }

  return (
    <div className="flex items-center gap-2">
      <span className="text-xs font-medium text-muted-foreground w-16">{label}</span>
      <div className="flex-1 h-2 rounded-full bg-muted overflow-hidden">
        <div
          className={cn("h-full rounded-full transition-all duration-500", getColorClass())}
          style={{ width: `${Math.min(value, 100)}%` }}
        />
      </div>
      <span className="text-xs font-semibold w-10 text-right">{Math.round(value)}%</span>
    </div>
  )
}

// Node metrics card — fetches its own metrics
function NodeMetricsCard({ node }: { node: Node }) {
  const displayStatus = getNodeDisplayStatus(node)
  const isOnline = displayStatus !== "offline"
  const { data: metrics } = useNodeMetrics(isOnline ? node.id : "")
  const { data: history } = useNodeMetricsHistory(isOnline ? node.id : "", 60)

  const cpuUsage = metrics?.cpu_percent ?? 0
  const memUsage = metrics?.memory_used_percent ?? 0
  const diskUsage = metrics?.disk_used_percent ?? 0
  const networkIn = metrics?.network_rx_bytes_per_sec ?? 0
  const networkOut = metrics?.network_tx_bytes_per_sec ?? 0
  const vmCount = metrics?.running_vm_count ?? 0
  const cpuTrend = (history ?? []).map((s) => s.cpu_usage)
  const memTrend = (history ?? []).map((s) => s.memory_usage)
  
  return (
    <div className="rounded-lg border bg-card text-card-foreground shadow-sm">
      {/* Card Header */}
      <div className="border-b bg-muted/50 p-4 flex items-center justify-between rounded-t-lg">
        <div className="flex items-center gap-3">
          <Server className="w-5 h-5 text-muted-foreground" />
          <span className="text-lg font-semibold">{node.name}</span>
        </div>
        <StatusBadge status={displayStatus} />
      </div>

      {/* Card Body */}
      <div className="p-4">
        {!isOnline ? (
          <div className="py-6 text-center">
            <AlertCircle className="w-8 h-8 text-muted-foreground mx-auto mb-2" />
            <p className="text-sm text-muted-foreground font-medium">Node offline</p>
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
            
            {/* Trend (last 60m) */}
            <div className="mb-4 pb-4 border-b">
              <p className="text-[10px] font-medium text-muted-foreground mb-2">Trend · last 60m</p>
              <div className="grid grid-cols-2 gap-3">
                <div className="rounded-md border p-2">
                  <span className="text-[10px] font-medium text-muted-foreground">CPU</span>
                  <Sparkline data={cpuTrend} colorClass="text-primary" height={32} />
                </div>
                <div className="rounded-md border p-2">
                  <span className="text-[10px] font-medium text-muted-foreground">Memory</span>
                  <Sparkline data={memTrend} colorClass="text-primary" height={32} />
                </div>
              </div>
            </div>

            {/* Network I/O */}
            <div className="flex items-center gap-6 mb-4 pb-4 border-b">
              <div className="flex items-center gap-2">
                <Network className="w-4 h-4 text-emerald-600" />
                <span className="text-xs font-medium text-muted-foreground">IN</span>
                <span className="text-sm font-semibold">{formatBytes(networkIn)}/s</span>
              </div>
              <div className="flex items-center gap-2">
                <Network className="w-4 h-4 text-primary rotate-180" />
                <span className="text-xs font-medium text-muted-foreground">OUT</span>
                <span className="text-sm font-semibold">{formatBytes(networkOut)}/s</span>
              </div>
            </div>

            {/* VM Count & IP */}
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Database className="w-4 h-4 text-muted-foreground" />
                <span className="text-sm font-medium text-muted-foreground">VMs:</span>
                <span className="text-sm font-semibold">{vmCount}</span>
              </div>
              <div className="flex items-center gap-2">
                <Network className="w-4 h-4 text-muted-foreground" />
                <span className="text-sm font-mono text-muted-foreground">{node.ip_address}</span>
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
      "fixed bottom-4 right-4 z-50 px-6 py-4 rounded-lg border bg-card text-card-foreground shadow-md",
      type === "success" && "border-emerald-200 dark:border-emerald-900",
      type === "error" && "border-red-200 dark:border-red-900",
      type === "info" && "border-border"
    )}>
      <p className="font-medium text-sm">{message}</p>
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
        return { icon: AlertTriangle, bg: "bg-red-50 dark:bg-red-950", border: "border-red-200 dark:border-red-900", text: "text-red-700 dark:text-red-400", iconBg: "bg-red-50 dark:bg-red-950" }
      case "warning":
        return { icon: AlertTriangle, bg: "bg-amber-50 dark:bg-amber-950", border: "border-amber-200 dark:border-amber-900", text: "text-amber-700 dark:text-amber-400", iconBg: "bg-amber-50 dark:bg-amber-950" }
      case "info":
        return { icon: Info, bg: "bg-muted", border: "border-border", text: "text-muted-foreground", iconBg: "bg-muted" }
    }
  }
  
  // Loading
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="mb-6">
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-3">
            <Activity className="w-8 h-8" />
            Monitoring
          </h1>
          <Skeleton className="h-5 w-48 mt-1" />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-24 rounded-lg" />)}
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
          {[1,2].map(i => <Skeleton key={i} className="h-48 rounded-lg" />)}
        </div>
      </div>
    )
  }
  
  // Error
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="mb-6">
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-3">
            <Activity className="w-8 h-8" />
            Monitoring
          </h1>
        </div>
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-12 text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" />
          <h2 className="text-xl font-semibold mb-2">Failed to load monitoring data</h2>
          <p className="text-muted-foreground mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()}>Retry</Button>
        </div>
      </div>
    )
  }
  
  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <h1 className="text-3xl font-bold tracking-tight flex items-center gap-3">
          <Activity className="w-8 h-8" />
          Monitoring
        </h1>
        <p className="text-muted-foreground text-sm mt-1">
          Real-time cluster overview and alerts
        </p>
      </div>

      {/* Cluster Overview Stats */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Total Nodes</span>
            <Server className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-3xl font-bold">{totalNodes}</p>
          <p className="text-xs text-muted-foreground font-medium mt-1">{onlineNodes.length} online</p>
        </div>

        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Total VMs</span>
            <Database className="w-4 h-4 text-muted-foreground" />
          </div>
          <p className="text-3xl font-bold">{totalVMs}</p>
        </div>

        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Online</span>
            <CheckCircle className="w-4 h-4 text-emerald-600" />
          </div>
          <p className="text-3xl font-bold text-emerald-600">{onlineNodes.length}</p>
        </div>

        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-muted-foreground">Active Alerts</span>
            <Zap className="w-4 h-4 text-amber-500" />
          </div>
          <p className="text-3xl font-bold text-amber-600">{alerts.filter(a => !acknowledgedIds.has(a.id)).length}</p>
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
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-12 text-center mb-6">
          <Server className="w-12 h-12 text-muted-foreground mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">No nodes registered</p>
        </div>
      )}

      {/* Alerts Section */}
      <div className="rounded-lg border bg-card text-card-foreground shadow-sm">
        {/* Alerts Header */}
        <div className="border-b bg-muted/50 font-semibold p-4 flex items-center justify-between rounded-t-lg">
          <div className="flex items-center gap-2">
            <Zap className="w-5 h-5" />
            Alerts ({alerts.length})
          </div>

          {/* Filter Buttons */}
          <div className="flex items-center gap-2">
            {(["all", "critical", "warning", "info"] as const).map(filter => {
              return (
                <button
                  key={filter}
                  onClick={() => setSeverityFilter(filter)}
                  className={cn(
                    "px-3 py-1 text-xs font-medium capitalize rounded-md border transition-colors",
                    severityFilter === filter
                      ? "bg-primary text-primary-foreground border-primary"
                      : "bg-background text-muted-foreground hover:bg-muted"
                  )}
                >
                  {filter}
                </button>
              )
            })}
          </div>
        </div>
        
        {/* Alert Rows */}
        <div className="divide-y">
          {filteredAlerts.length === 0 ? (
            <div className="p-8 text-center">
              <CheckCircle className="w-12 h-12 text-emerald-600 mx-auto mb-2" />
              <p className="font-medium text-muted-foreground">No alerts to display</p>
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
                  <div className={cn("w-10 h-10 flex items-center justify-center rounded-md border", config.iconBg, config.border)}>
                    <Icon className={cn("w-5 h-5", config.text)} />
                  </div>

                  {/* Alert Content */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                      <span className={cn("text-[10px] font-medium capitalize rounded-full px-2 py-0.5 border", config.bg, config.text, config.border)}>
                        {alert.severity}
                      </span>
                      <span className="text-xs font-medium text-muted-foreground">{alert.timestamp}</span>
                    </div>
                    <p className="font-medium truncate">{alert.message}</p>
                    <p className="text-xs font-medium text-muted-foreground">Node: {alert.node}</p>
                  </div>

                  {/* Acknowledge Button */}
                  {!alert.acknowledged && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleAcknowledge(alert.id)}
                      className="gap-1 shrink-0"
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
