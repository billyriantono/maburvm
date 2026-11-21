"use client"

import { useState } from "react"
import Link from "next/link"
import { ArrowLeft, Plus, Loader2, AlertCircle } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { NodeInstallCommand } from "@/components/node-install-command"
import { useCreateNode } from "@/lib/hooks/use-nodes"

export default function AddNodePage() {
  const createNode = useCreateNode()

  // Form state
  const [name, setName] = useState("")
  const [ipAddress, setIpAddress] = useState("")
  const [created, setCreated] = useState<{ id: string; name: string } | null>(null)
  const [errors, setErrors] = useState<{ name?: string; ip?: string }>({})

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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validate()) return
    try {
      const node = await createNode.mutateAsync({ name, ip_address: ipAddress })
      setCreated({ id: node.id, name: node.name })
      toast.success(`Node ${node.name} created`)
    } catch (error) {
      toast.error((error as Error)?.message || "Failed to create node")
    }
  }

  return (
    <div className="max-w-2xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/nodes">
          <Button variant="outline" size="icon" aria-label="Back to nodes">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Add node</h1>
          <p className="text-muted-foreground text-sm">Register a new machine, then install the agent on it with one command</p>
        </div>
      </div>

      {created ? (
        <>
          <NodeInstallCommand nodeId={created.id} installerAllowed createOnMount />
          <div className="flex flex-wrap items-center justify-end gap-3">
            <p className="mr-auto text-sm text-muted-foreground">
              Installing by hand instead? The node&apos;s page shows its agent token.
            </p>
            <Link href={`/nodes/${created.id}`}>
              <Button>Go to {created.name}</Button>
            </Link>
          </div>
        </>
      ) : (
        <form onSubmit={handleSubmit}>
          <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
            <h2 className="text-lg font-semibold text-foreground mb-6">Node details</h2>

            <div className="space-y-6">
              <div>
                <label htmlFor="node-name" className="block text-sm font-medium text-foreground mb-2">
                  Node name <span className="text-red-600">*</span>
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
                <p className="text-xs text-muted-foreground mt-1">Use lowercase letters, numbers, and hyphens only</p>
              </div>

              <div>
                <label htmlFor="node-ip" className="block text-sm font-medium text-foreground mb-2">
                  IP address <span className="text-red-600">*</span>
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
                <p className="text-xs text-muted-foreground mt-1">The address the panel reaches this node&apos;s agent on (port 50051)</p>
              </div>
            </div>
          </div>

          <p className="mb-6 text-sm text-muted-foreground">
            Next you get a single-use command that installs the agent on the new machine. It is for new machines only: to
            update an existing node&apos;s agent, use Settings, System.
          </p>

          <div className="flex items-center justify-end gap-4">
            <Link href="/nodes">
              <Button type="button" variant="outline">Cancel</Button>
            </Link>
            <Button type="submit" disabled={createNode.isPending} className="gap-2">
              {createNode.isPending ? (
                <>
                  <Loader2 className="w-4 h-4 animate-spin" />
                  Creating...
                </>
              ) : (
                <>
                  <Plus className="w-4 h-4" />
                  Create node
                </>
              )}
            </Button>
          </div>
        </form>
      )}
    </div>
  )
}
