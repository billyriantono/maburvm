"use client"

import { useState, useEffect } from "react"
import Link from "next/link"
import {
  ArrowLeft,
  Plus,
  Loader2,
  Copy,
  Check,
  AlertCircle,
  CheckCircle,
  Terminal,
  AlertTriangle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCreateNode } from "@/lib/hooks/use-nodes"

// Toast notification
function Toast({ message, type, onClose }: { message: string, type: "success" | "error", onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 px-4 py-3 rounded-lg border shadow-md ${
      type === "success"
        ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900"
        : "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900"
    }`}>
      <p className="font-medium text-sm">{message}</p>
    </div>
  )
}



export default function AddNodePage() {
  const createNode = useCreateNode()

  // Form state
  const [name, setName] = useState("")
  const [ipAddress, setIpAddress] = useState("")
  const [token, setToken] = useState("")
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ name?: string; ip?: string }>({})
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [copied, setCopied] = useState(false)
  const [showToken, setShowToken] = useState(false)

  // Validate form
  const validate = () => {
    const newErrors: { name?: string; ip?: string } = {}

    if (!name.trim()) {
      newErrors.name = "Node name is required"
    } else if (!/^[a-z0-9-]+$/.test(name)) {
      newErrors.name = "Only lowercase letters, numbers, and hyphens allowed"
    }

    if (!ipAddress.trim()) {
      newErrors.ip = "IP address is required"
    } else if (!/^(?:\d{1,3}\.){3}\d{1,3}$/.test(ipAddress)) {
      newErrors.ip = "Invalid IP address format"
    }

    setErrors(newErrors)
    return Object.keys(newErrors).length === 0
  }

  // Handle form submission
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()

    if (!validate()) return

    setLoading(true)

    try {
      const result = await createNode.mutateAsync({
        name,
        ip_address: ipAddress,
      })

      // Show token from backend response
      if (result && (result as any).token) {
        setToken((result as any).token)
        setShowToken(true)
      }

      setToast({ message: `Node ${name} created`, type: "success" })
    } catch (error: any) {
      const message = error?.response?.data?.message || error?.message || "Failed to create node"
      setToast({ message, type: "error" })
    } finally {
      setLoading(false)
    }
  }

  // Copy token to clipboard
  const copyToken = () => {
    navigator.clipboard.writeText(token)
    setCopied(true)
    setToast({ message: "Token copied to clipboard", type: "success" })
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="max-w-2xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/nodes">
          <Button variant="outline" size="icon">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div>
          <h1 className="text-2xl font-semibold text-foreground">
            Add Node
          </h1>
          <p className="text-muted-foreground text-sm">
            Register a new node with the panel
          </p>
        </div>
      </div>

      <form onSubmit={handleSubmit}>
        {/* Node Details */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
          <h2 className="text-lg font-semibold text-foreground mb-6">
            Node Details
          </h2>

          <div className="space-y-6">
            {/* Name */}
            <div>
              <label htmlFor="node-name" className="block text-sm font-medium text-foreground mb-2">
                Node Name <span className="text-red-600">*</span>
              </label>
              <Input
                id="node-name"
                type="text"
                placeholder="e.g., node-01"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={errors.name ? "border-red-500" : ""}
              />
              {errors.name && (
                <div className="flex items-center gap-1 mt-2 text-red-600">
                  <AlertCircle className="w-4 h-4" />
                  <span className="text-xs font-medium">{errors.name}</span>
                </div>
              )}
              <p className="text-xs text-muted-foreground mt-1">
                Use lowercase letters, numbers, and hyphens only
              </p>
            </div>

            {/* IP Address */}
            <div>
              <label htmlFor="node-ip" className="block text-sm font-medium text-foreground mb-2">
                IP Address <span className="text-red-600">*</span>
              </label>
              <Input
                id="node-ip"
                type="text"
                placeholder="e.g., 10.0.1.100"
                value={ipAddress}
                onChange={(e) => setIpAddress(e.target.value)}
                className={`font-mono ${errors.ip ? "border-red-500" : ""}`}
              />
              {errors.ip && (
                <div className="flex items-center gap-1 mt-2 text-red-600">
                  <AlertCircle className="w-4 h-4" />
                  <span className="text-xs font-medium">{errors.ip}</span>
                </div>
              )}
              <p className="text-xs text-muted-foreground mt-1">
                The IP address that this node will use to connect to the panel
              </p>
            </div>
          </div>
        </div>

        {/* Token Generation */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
          <h2 className="text-lg font-semibold text-foreground mb-2">
            Node Token
          </h2>
          <p className="text-sm text-muted-foreground mb-6">
            This token will be used by the node agent to authenticate with the control panel.
          </p>

          <div className="flex items-center gap-4">
            <div className="flex-1 bg-muted border rounded-md p-4">
              <div className="flex items-center justify-between">
                <code className="font-mono text-sm font-medium">
                  {showToken ? token : "••••••••••••••••••••"}
                </code>
                <button
                  type="button"
                  onClick={() => setShowToken(!showToken)}
                  className="text-xs font-medium text-muted-foreground hover:text-foreground"
                >
                  {showToken ? "Hide" : "Show"}
                </button>
              </div>
            </div>
          </div>

          <div className="flex items-center gap-3 mt-4">
            <Button type="button" variant="outline" onClick={copyToken} className="gap-2">
              {copied ? <Check className="w-4 h-4 text-emerald-600" /> : <Copy className="w-4 h-4" />}
              {copied ? "Copied!" : "Copy"}
            </Button>
          </div>

          <div className="mt-4 p-3 rounded-md bg-emerald-50 border border-emerald-200 dark:bg-emerald-950 dark:border-emerald-900">
            <div className="flex items-start gap-2">
              <CheckCircle className="w-5 h-5 text-emerald-600 shrink-0 mt-0.5" />
              <div>
                <p className="font-medium text-sm text-emerald-700 dark:text-emerald-300">Token generated automatically</p>
                <p className="text-xs text-muted-foreground">
                  You can regenerate the token if needed. Make sure to update it on the node agent after saving.
                </p>
              </div>
            </div>
          </div>
        </div>

        {/* Agent installation */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
          <h2 className="text-lg font-semibold text-foreground mb-2 flex items-center gap-2">
            <Terminal className="w-5 h-5" />Install the Agent
          </h2>
          <div className="p-4 rounded-md bg-amber-50 border border-amber-200 dark:bg-amber-950 dark:border-amber-900">
            <div className="flex items-start gap-3">
              <AlertTriangle className="w-5 h-5 text-amber-600 shrink-0 mt-0.5" />
              <div className="space-y-2">
                <p className="font-medium text-sm text-foreground">
                  Automatic agent installation is temporarily unavailable while node deployment security is being completed.
                </p>
                <p className="text-xs text-muted-foreground">
                  Deploying the agent on a hypervisor host manually is an administrator-only operational procedure. See the deployment documentation (<code className="bg-muted px-1 rounded border">docs/DEPLOYMENT.md</code>, agent deployment) for details.
                </p>
                <p className="text-xs text-muted-foreground">
                  Register the node here and keep its token — the agent is configured with it when the host is deployed.
                </p>
              </div>
            </div>
          </div>
          {showToken && token && (
            <div className="flex items-center gap-3 mt-4">
              <Link href="/nodes">
                <Button type="button" className="gap-2"><CheckCircle className="w-4 h-4" />Done — back to Nodes</Button>
              </Link>
            </div>
          )}
        </div>

        {/* Actions */}
        <div className="flex items-center justify-end gap-4">
          <Link href="/nodes">
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </Link>
          <Button type="submit" disabled={loading} className="gap-2">
            {loading ? (
              <>
                <Loader2 className="w-4 h-4 animate-spin" />
                Creating...
              </>
            ) : (
              <>
                <Plus className="w-4 h-4" />
                Create Node
              </>
            )}
          </Button>
        </div>
      </form>

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
