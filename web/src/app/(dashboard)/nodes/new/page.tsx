"use client"

import { useState, useEffect } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { 
  Server, 
  ArrowLeft,
  Plus,
  Loader2,
  Copy,
  Check,
  AlertCircle,
  CheckCircle,
  RefreshCw,
  Network,
  HardDrive,
  Cpu
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

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

// Generate random token
function generateToken(): string {
  const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
  let token = "tok_"
  for (let i = 0; i < 14; i++) {
    token += chars.charAt(Math.floor(Math.random() * chars.length))
  }
  return token
}

export default function AddNodePage() {
  const router = useRouter()
  
  // Form state
  const [name, setName] = useState("")
  const [ipAddress, setIpAddress] = useState("")
  const [token, setToken] = useState(generateToken())
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
    
    // Simulate API call
    await new Promise(resolve => setTimeout(resolve, 1500))
    
    // In production, this would call the API to create the node
    setToast({ message: `Node ${name} created successfully`, type: "success" })
    
    // Redirect to nodes list after a short delay
    setTimeout(() => {
      router.push("/nodes")
    }, 1000)
  }
  
  // Copy token to clipboard
  const copyToken = () => {
    navigator.clipboard.writeText(token)
    setCopied(true)
    setToast({ message: "Token copied to clipboard", type: "success" })
    setTimeout(() => setCopied(false), 2000)
  }
  
  // Regenerate token
  const regenerateToken = () => {
    setToken(generateToken())
    setToast({ message: "Token regenerated", type: "success" })
  }
  
  return (
    <div className="max-w-2xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/nodes">
          <Button variant="ghost" size="icon" className="border-2 border-black">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            Add Node
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
            Register a new node with the panel
          </p>
        </div>
      </div>

      <form onSubmit={handleSubmit}>
        {/* Node Details */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-6">
            Node Details
          </h2>
          
          <div className="space-y-6">
            {/* Name */}
            <div>
              <label htmlFor="node-name" className="block text-sm font-black uppercase text-gray-500 mb-2">
                Node Name <span className="text-danger">*</span>
              </label>
              <Input
                id="node-name"
                type="text"
                placeholder="e.g., node-01"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={`border-2 border-black ${errors.name ? "border-danger" : ""}`}
              />
              {errors.name && (
                <div className="flex items-center gap-1 mt-2 text-danger">
                  <AlertCircle className="w-4 h-4" />
                  <span className="text-xs font-bold">{errors.name}</span>
                </div>
              )}
              <p className="text-xs text-gray-500 mt-1">
                Use lowercase letters, numbers, and hyphens only
              </p>
            </div>
            
            {/* IP Address */}
            <div>
              <label htmlFor="node-ip" className="block text-sm font-black uppercase text-gray-500 mb-2">
                IP Address <span className="text-danger">*</span>
              </label>
              <Input
                id="node-ip"
                type="text"
                placeholder="e.g., 10.0.1.100"
                value={ipAddress}
                onChange={(e) => setIpAddress(e.target.value)}
                className={`border-2 border-black font-mono ${errors.ip ? "border-danger" : ""}`}
              />
              {errors.ip && (
                <div className="flex items-center gap-1 mt-2 text-danger">
                  <AlertCircle className="w-4 h-4" />
                  <span className="text-xs font-bold">{errors.ip}</span>
                </div>
              )}
              <p className="text-xs text-gray-500 mt-1">
                The IP address that this node will use to connect to the panel
              </p>
            </div>
          </div>
        </div>

        {/* Token Generation */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-2">
            Node Token
          </h2>
          <p className="text-sm text-gray-500 mb-6">
            This token will be used by the node agent to authenticate with the control panel.
          </p>
          
          <div className="flex items-center gap-4">
            <div className="flex-1 bg-gray-100 border-2 border-black p-4">
              <div className="flex items-center justify-between">
                <code className="font-mono text-lg font-bold">
                  {showToken ? token : "••••••••••••••••••••"}
                </code>
                <button
                  type="button"
                  onClick={() => setShowToken(!showToken)}
                  className="text-xs font-bold uppercase text-gray-500 hover:text-black"
                >
                  {showToken ? "Hide" : "Show"}
                </button>
              </div>
            </div>
          </div>
          
          <div className="flex items-center gap-3 mt-4">
            <Button type="button" variant="ghost" onClick={copyToken} className="border-2 border-black gap-2">
              {copied ? <Check className="w-4 h-4 text-success" /> : <Copy className="w-4 h-4" />}
              {copied ? "Copied!" : "Copy"}
            </Button>
            <Button type="button" variant="ghost" onClick={regenerateToken} className="border-2 border-black gap-2">
              <RefreshCw className="w-4 h-4" />
              Regenerate
            </Button>
          </div>
          
          <div className="mt-4 p-3 bg-success/20 border-2 border-success">
            <div className="flex items-start gap-2">
              <CheckCircle className="w-5 h-5 text-success shrink-0 mt-0.5" />
              <div>
                <p className="font-bold text-sm text-success">Token generated automatically</p>
                <p className="text-xs text-gray-600">
                  You can regenerate the token if needed. Make sure to update it on the node agent after saving.
                </p>
              </div>
            </div>
          </div>
        </div>

        {/* Installation Instructions */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
            Next Steps
          </h2>
          
          <div className="space-y-4">
            <div className="flex items-start gap-3">
              <div className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-xs font-black shrink-0">
                1
              </div>
              <div>
                <p className="font-bold">Download and install the node agent</p>
                <p className="text-sm text-gray-500">The agent binary is available in the releases section</p>
              </div>
            </div>
            
            <div className="flex items-start gap-3">
              <div className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-xs font-black shrink-0">
                2
              </div>
              <div>
                <p className="font-bold">Configure the agent with the token</p>
                <p className="text-sm text-gray-500">Set the PANEL_URL and NODE_TOKEN environment variables or config file</p>
              </div>
            </div>
            
            <div className="flex items-start gap-3">
              <div className="w-6 h-6 bg-primary flex items-center justify-center border border-black text-xs font-black shrink-0">
                3
              </div>
              <div>
                <p className="font-bold">Start the agent</p>
                <p className="text-sm text-gray-500">The node will automatically register with the panel and appear online</p>
              </div>
            </div>
          </div>
          
          <div className="mt-6 p-4 bg-gray-100 border-2 border-black">
            <p className="text-xs font-bold uppercase text-gray-500 mb-2">Example configuration:</p>
            <pre className="font-mono text-xs text-black overflow-x-auto">
{`# Environment variables
PANEL_URL=http://10.0.0.1:8080
NODE_TOKEN=${token}

# Or use config file
cat > /etc/maburvm/agent.yaml << EOF
panel_url: http://10.0.0.1:8080
node_token: ${token}
EOF`}
            </pre>
          </div>
        </div>

        {/* Actions */}
        <div className="flex items-center justify-end gap-4">
          <Link href="/nodes">
            <Button type="button" variant="ghost" className="border-2 border-black">
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