"use client"

import { useState, use } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { 
  ArrowLeft, 
  Edit, 
  Trash2, 
  Shield, 
  ShieldOff, 
  RotateCcw,
  Mail,
  Calendar,
  Monitor,
  Globe,
  CheckCircle,
  XCircle,
  Save,
  Eye,
  EyeOff
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { IPWhitelistEditor } from "@/components/ip-whitelist-editor"

// Mock user data
const mockUsers: Record<string, {
  id: string
  name: string
  email: string
  role: "admin" | "client"
  status: "active" | "suspended"
  twoFactorEnabled: boolean
  vmCount: number
  ipWhitelist: string[]
  createdAt: string
  vms: Array<{ id: string; name: string; status: string }>
}> = {
  "1": {
    id: "1",
    name: "Admin User",
    email: "admin@maburvm.local",
    role: "admin",
    status: "active",
    twoFactorEnabled: true,
    vmCount: 0,
    ipWhitelist: ["192.168.1.0/24", "10.0.0.0/8"],
    createdAt: "2024-01-15",
    vms: [],
  },
  "2": {
    id: "2",
    name: "John Developer",
    email: "john@company.com",
    role: "client",
    status: "active",
    twoFactorEnabled: true,
    vmCount: 5,
    ipWhitelist: ["192.168.1.100"],
    createdAt: "2024-02-20",
    vms: [
      { id: "vm1", name: "web-server-01", status: "running" },
      { id: "vm2", name: "api-server-01", status: "running" },
      { id: "vm3", name: "db-server-01", status: "stopped" },
      { id: "vm4", name: "worker-01", status: "running" },
      { id: "vm5", name: "cache-01", status: "running" },
    ],
  },
  "3": {
    id: "3",
    name: "Sarah Engineer",
    email: "sarah@company.com",
    role: "client",
    status: "active",
    twoFactorEnabled: false,
    vmCount: 8,
    ipWhitelist: [],
    createdAt: "2024-03-10",
    vms: [
      { id: "vm6", name: "dev-env", status: "running" },
      { id: "vm7", name: "test-server", status: "running" },
      { id: "vm8", name: "staging-01", status: "running" },
      { id: "vm9", name: "jenkins", status: "running" },
      { id: "vm10", name: "docker-host", status: "running" },
      { id: "vm11", name: "k8s-master", status: "running" },
      { id: "vm12", name: "k8s-worker-1", status: "running" },
      { id: "vm13", name: "k8s-worker-2", status: "stopped" },
    ],
  },
}

export default function UserDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const resolvedParams = use(params)
  const router = useRouter()
  const user = mockUsers[resolvedParams.id] || mockUsers["1"]

  const [isEditing, setIsEditing] = useState(false)
  const [showPassword, setShowPassword] = useState(false)
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [suspendDialogOpen, setSuspendDialogOpen] = useState(false)
  const [resetDialogOpen, setResetDialogOpen] = useState(false)
  const [formData, setFormData] = useState({
    name: user.name,
    email: user.email,
    role: user.role,
    ipWhitelist: user.ipWhitelist,
    twoFactorEnabled: user.twoFactorEnabled,
  })

  const handleSave = () => {
    // In production, send to API
    console.log("Saving user:", formData)
    setIsEditing(false)
  }

  const handleDelete = () => {
    setDeleteDialogOpen(false)
    router.push("/users")
  }

  const handleSuspend = () => {
    setSuspendDialogOpen(false)
  }

  const handleResetPassword = () => {
    setResetDialogOpen(false)
    alert("Password reset email sent!")
  }

  return (
    <div className="max-w-5xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <Link
          href="/users"
          className="flex items-center gap-2 text-sm font-bold uppercase text-gray-500 hover:text-black mb-4"
        >
          <ArrowLeft className="w-4 h-4" />
          Back to Users
        </Link>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-4">
            <div className="w-16 h-16 bg-primary flex items-center justify-center border-4 border-black shadow-neo">
              <span className="text-2xl font-black">{user.name.charAt(0).toUpperCase()}</span>
            </div>
            <div>
              <h1 className="text-3xl font-black uppercase tracking-tight text-black">
                {user.name}
              </h1>
              <p className="text-gray-500 font-medium">{user.email}</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {isEditing ? (
              <>
                <Button variant="outline" onClick={() => setIsEditing(false)}>
                  Cancel
                </Button>
                <Button onClick={handleSave}>
                  <Save className="w-4 h-4 mr-2" />
                  Save Changes
                </Button>
              </>
            ) : (
              <>
                <Button variant="outline" onClick={() => setIsEditing(true)}>
                  <Edit className="w-4 h-4 mr-2" />
                  Edit
                </Button>
                <Button
                  variant="ghost"
                  onClick={() => setSuspendDialogOpen(true)}
                >
                  {user.status === "active" ? (
                    <>
                      <ShieldOff className="w-4 h-4 mr-2" />
                      Suspend
                    </>
                  ) : (
                    <>
                      <Shield className="w-4 h-4 mr-2" />
                      Activate
                    </>
                  )}
                </Button>
                <Button variant="ghost" onClick={() => setResetDialogOpen(true)}>
                  <RotateCcw className="w-4 h-4 mr-2" />
                  Reset Password
                </Button>
                <Button variant="ghost" onClick={() => setDeleteDialogOpen(true)}>
                  <Trash2 className="w-4 h-4 text-danger" />
                </Button>
              </>
            )}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Left Column */}
        <div className="lg:col-span-2 space-y-6">
          {/* Profile Information */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">Profile Information</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              {isEditing ? (
                <>
                  <div className="space-y-2">
                    <Label htmlFor="name">Full Name</Label>
                    <Input
                      id="name"
                      value={formData.name}
                      onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="email">Email Address</Label>
                    <Input
                      id="email"
                      type="email"
                      value={formData.email}
                      onChange={(e) => setFormData({ ...formData, email: e.target.value })}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="role">Role</Label>
                    <Select
                      value={formData.role}
                      onValueChange={(value: "admin" | "client") =>
                        setFormData({ ...formData, role: value })
                      }
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="client">Client</SelectItem>
                        <SelectItem value="admin">Admin</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </>
              ) : (
                <>
                  <div className="flex items-center gap-3 pb-4 border-b-2 border-black">
                    <Mail className="w-5 h-5 text-gray-500" />
                    <div>
                      <p className="text-xs font-bold uppercase text-gray-500">Email</p>
                      <p className="font-medium">{user.email}</p>
                    </div>
                  </div>
                  <div className="flex items-center gap-3 pb-4 border-b-2 border-black">
                    <Calendar className="w-5 h-5 text-gray-500" />
                    <div>
                      <p className="text-xs font-bold uppercase text-gray-500">Created</p>
                      <p className="font-medium">{user.createdAt}</p>
                    </div>
                  </div>
                  <div className="flex items-center gap-3">
                    <Shield className="w-5 h-5 text-gray-500" />
                    <div>
                      <p className="text-xs font-bold uppercase text-gray-500">Role</p>
                      <Badge variant={user.role === "admin" ? "secondary" : "outline"}>
                        {user.role === "admin" ? "Admin" : "Client"}
                      </Badge>
                    </div>
                  </div>
                </>
              )}
            </CardContent>
          </Card>

          {/* IP Whitelist */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">IP Whitelist</CardTitle>
              <CardDescription>
                {isEditing ? "Edit allowed IP addresses" : "Allowed IP addresses for login"}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {isEditing ? (
                <IPWhitelistEditor
                  value={formData.ipWhitelist}
                  onChange={(ips) => setFormData({ ...formData, ipWhitelist: ips })}
                />
              ) : (
                <div className="space-y-2">
                  {user.ipWhitelist.length > 0 ? (
                    user.ipWhitelist.map((ip) => (
                      <div
                        key={ip}
                        className="flex items-center gap-2 p-2 bg-gray-50 border-2 border-black"
                      >
                        <Globe className="w-4 h-4 text-gray-500" />
                        <span className="font-mono text-sm">{ip}</span>
                      </div>
                    ))
                  ) : (
                    <p className="text-gray-500 text-sm">Any IP address allowed</p>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        {/* Right Column */}
        <div className="space-y-6">
          {/* Status */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">Status</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex items-center justify-between">
                <span className="text-sm font-bold uppercase">Account</span>
                <Badge variant={user.status === "active" ? "success" : "destructive"}>
                  {user.status === "active" ? "Active" : "Suspended"}
                </Badge>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm font-bold uppercase">2FA</span>
                {user.twoFactorEnabled ? (
                  <div className="flex items-center gap-1 text-success">
                    <CheckCircle className="w-4 h-4" />
                    <span className="text-xs font-bold uppercase">Enabled</span>
                  </div>
                ) : (
                  <div className="flex items-center gap-1 text-danger">
                    <XCircle className="w-4 h-4" />
                    <span className="text-xs font-bold uppercase">Disabled</span>
                  </div>
                )}
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm font-bold uppercase">VMs</span>
                <span className="font-bold">{user.vmCount}</span>
              </div>
            </CardContent>
          </Card>

          {/* Owned VMs */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">Owned VMs</CardTitle>
              <CardDescription>Virtual machines owned by this user</CardDescription>
            </CardHeader>
            <CardContent>
              {user.vms.length > 0 ? (
                <div className="space-y-2">
                  {user.vms.map((vm) => (
                    <div
                      key={vm.id}
                      className="flex items-center justify-between p-2 bg-gray-50 border-2 border-black"
                    >
                      <div className="flex items-center gap-2">
                        <Monitor className="w-4 h-4 text-gray-500" />
                        <span className="text-sm font-medium">{vm.name}</span>
                      </div>
                      <Badge
                        variant={vm.status === "running" ? "success" : "outline"}
                      >
                        {vm.status}
                      </Badge>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-gray-500 text-sm text-center py-4">
                  No VMs owned
                </p>
              )}
            </CardContent>
          </Card>
        </div>
      </div>

      {/* Delete Dialog */}
      <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete User</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete {user.name}? This action cannot be undone.
              {user.vmCount > 0 && (
                <p className="mt-2 text-danger font-bold">
                  Warning: This user owns {user.vmCount} VM(s).
                </p>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteDialogOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleDelete}>
              Delete User
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Suspend/Activate Dialog */}
      <Dialog open={suspendDialogOpen} onOpenChange={setSuspendDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {user.status === "active" ? "Suspend" : "Activate"} User
            </DialogTitle>
            <DialogDescription>
              Are you sure you want to {user.status === "active" ? "suspend" : "activate"}{" "}
              {user.name}?
              {user.status === "active"
                ? " They will not be able to log in."
                : " They will regain access to their account."}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSuspendDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              variant={user.status === "active" ? "destructive" : "success"}
              onClick={handleSuspend}
            >
              {user.status === "active" ? "Suspend" : "Activate"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Reset Password Dialog */}
      <Dialog open={resetDialogOpen} onOpenChange={setResetDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Reset Password</DialogTitle>
            <DialogDescription>
              Generate a temporary password for {user.name}? They will receive an email
              with instructions to set a new password.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setResetDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleResetPassword}>Reset Password</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}