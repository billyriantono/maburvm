"use client"

import { useState } from "react"
import Link from "next/link"
import { 
  UserPlus, 
  Search, 
  MoreHorizontal, 
  Shield, 
  ShieldOff,
  Mail,
  Edit,
  Trash2,
  PowerCircle,
  RotateCcw,
  Eye,
  CheckCircle,
  XCircle
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"

// Mock user data
interface User {
  id: string
  name: string
  email: string
  role: "admin" | "client"
  status: "active" | "suspended"
  twoFactorEnabled: boolean
  vmCount: number
  ipWhitelist: string[]
  createdAt: string
}

const mockUsers: User[] = [
  {
    id: "1",
    name: "Admin User",
    email: "admin@maburvm.local",
    role: "admin",
    status: "active",
    twoFactorEnabled: true,
    vmCount: 0,
    ipWhitelist: ["192.168.1.0/24", "10.0.0.0/8"],
    createdAt: "2024-01-15",
  },
  {
    id: "2",
    name: "John Developer",
    email: "john@company.com",
    role: "client",
    status: "active",
    twoFactorEnabled: true,
    vmCount: 5,
    ipWhitelist: ["192.168.1.100"],
    createdAt: "2024-02-20",
  },
  {
    id: "3",
    name: "Sarah Engineer",
    email: "sarah@company.com",
    role: "client",
    status: "active",
    twoFactorEnabled: false,
    vmCount: 8,
    ipWhitelist: [],
    createdAt: "2024-03-10",
  },
  {
    id: "4",
    name: "Mike Tester",
    email: "mike@company.com",
    role: "client",
    status: "suspended",
    twoFactorEnabled: false,
    vmCount: 2,
    ipWhitelist: ["192.168.2.0/24"],
    createdAt: "2024-04-05",
  },
  {
    id: "5",
    name: "Alice DevOps",
    email: "alice@company.com",
    role: "client",
    status: "active",
    twoFactorEnabled: true,
    vmCount: 12,
    ipWhitelist: ["10.0.0.0/16", "172.16.0.0/12"],
    createdAt: "2024-05-01",
  },
]

export default function UsersPage() {
  const [searchQuery, setSearchQuery] = useState("")
  const [users, setUsers] = useState<User[]>(mockUsers)
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [userToDelete, setUserToDelete] = useState<User | null>(null)
  const [suspendDialogOpen, setSuspendDialogOpen] = useState(false)
  const [userToSuspend, setUserToSuspend] = useState<User | null>(null)
  const [resetDialogOpen, setResetDialogOpen] = useState(false)
  const [userToReset, setUserToReset] = useState<User | null>(null)

  const filteredUsers = users.filter(
    (user) =>
      user.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      user.email.toLowerCase().includes(searchQuery.toLowerCase())
  )

  const handleDeleteUser = (user: User) => {
    setUserToDelete(user)
    setDeleteDialogOpen(true)
  }

  const confirmDelete = () => {
    if (userToDelete) {
      setUsers(users.filter((u) => u.id !== userToDelete.id))
      setDeleteDialogOpen(false)
      setUserToDelete(null)
    }
  }

  const handleSuspendUser = (user: User) => {
    setUserToSuspend(user)
    setSuspendDialogOpen(true)
  }

  const confirmSuspend = () => {
    if (userToSuspend) {
      setUsers(
        users.map((u) =>
          u.id === userToSuspend.id
            ? { ...u, status: u.status === "active" ? "suspended" : "active" }
            : u
        )
      )
      setSuspendDialogOpen(false)
      setUserToSuspend(null)
    }
  }

  const handleResetPassword = (user: User) => {
    setUserToReset(user)
    setResetDialogOpen(true)
  }

  const confirmReset = () => {
    // In production, this would generate a temp password
    alert(`Password reset email sent to ${userToReset?.email}`)
    setResetDialogOpen(false)
    setUserToReset(null)
  }

  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between mb-8">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            Users
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            Manage user accounts and permissions
          </p>
        </div>
        <Link href="/users/new">
          <Button className="flex items-center gap-2">
            <UserPlus className="w-4 h-4" />
            Add User
          </Button>
        </Link>
      </div>

      {/* Search and Filters */}
      <Card className="mb-6">
        <CardContent className="p-4">
          <div className="flex items-center gap-4">
            <div className="relative flex-1">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
              <Input
                placeholder="Search users..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="pl-10"
              />
            </div>
            <div className="flex items-center gap-2">
              <Badge variant="default">{users.length} Total</Badge>
              <Badge variant="secondary">{users.filter((u) => u.status === "active").length} Active</Badge>
              <Badge variant="destructive">{users.filter((u) => u.status === "suspended").length} Suspended</Badge>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Users Table */}
      <Card>
        <CardHeader className="border-b-2 border-black">
          <CardTitle className="text-lg">All Users</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead className="bg-gray-50 border-b-2 border-black">
                <tr>
                  <th className="text-left px-6 py-3 text-xs font-black uppercase tracking-wider">
                    User
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-black uppercase tracking-wider">
                    Role
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-black uppercase tracking-wider">
                    Status
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-black uppercase tracking-wider">
                    2FA
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-black uppercase tracking-wider">
                    VMs
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-black uppercase tracking-wider">
                    IP Whitelist
                  </th>
                  <th className="text-right px-6 py-3 text-xs font-black uppercase tracking-wider">
                    Actions
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y-2 divide-black">
                {filteredUsers.map((user) => (
                  <tr key={user.id} className="hover:bg-gray-50">
                    <td className="px-6 py-4">
                      <div className="flex items-center gap-3">
                        <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                          <span className="text-sm font-black">
                            {user.name.charAt(0).toUpperCase()}
                          </span>
                        </div>
                        <div>
                          <Link
                            href={`/users/${user.id}`}
                            className="font-bold text-black hover:underline"
                          >
                            {user.name}
                          </Link>
                          <p className="text-sm text-gray-500">{user.email}</p>
                        </div>
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <Badge variant={user.role === "admin" ? "secondary" : "outline"}>
                        {user.role === "admin" ? "Admin" : "Client"}
                      </Badge>
                    </td>
                    <td className="px-6 py-4">
                      {user.status === "active" ? (
                        <Badge variant="success">Active</Badge>
                      ) : (
                        <Badge variant="destructive">Suspended</Badge>
                      )}
                    </td>
                    <td className="px-6 py-4">
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
                    </td>
                    <td className="px-6 py-4">
                      <span className="font-bold text-black">{user.vmCount}</span>
                    </td>
                    <td className="px-6 py-4">
                      {user.ipWhitelist.length > 0 ? (
                        <span className="text-sm font-medium">
                          {user.ipWhitelist.length} entries
                        </span>
                      ) : (
                        <span className="text-sm text-gray-400">Any</span>
                      )}
                    </td>
                    <td className="px-6 py-4">
                      <div className="flex items-center justify-end gap-2">
                        <Link href={`/users/${user.id}`}>
                          <Button variant="ghost" size="icon" className="h-8 w-8">
                            <Eye className="w-4 h-4" />
                          </Button>
                        </Link>
                        <Link href={`/users/${user.id}/edit`}>
                          <Button variant="ghost" size="icon" className="h-8 w-8">
                            <Edit className="w-4 h-4" />
                          </Button>
                        </Link>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="w-4 h-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem
                              onClick={() => handleSuspendUser(user)}
                              className="flex items-center gap-2"
                            >
                              {user.status === "active" ? (
                                <>
                                  <ShieldOff className="w-4 h-4" />
                                  Suspend
                                </>
                              ) : (
                                <>
                                  <Shield className="w-4 h-4" />
                                  Activate
                                </>
                              )}
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              onClick={() => handleResetPassword(user)}
                              className="flex items-center gap-2"
                            >
                              <RotateCcw className="w-4 h-4" />
                              Reset Password
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem
                              onClick={() => handleDeleteUser(user)}
                              className="flex items-center gap-2 text-danger focus:text-danger"
                            >
                              <Trash2 className="w-4 h-4" />
                              Delete
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      {/* Delete Confirmation Dialog */}
      <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete User</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete {userToDelete?.name}? This action cannot be undone.
              {userToDelete && userToDelete.vmCount > 0 && (
                <p className="mt-2 text-danger font-bold">
                  Warning: This user owns {userToDelete.vmCount} VM(s). Consider reassigning them first.
                </p>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteDialogOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={confirmDelete}>
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
              {userToSuspend?.status === "active" ? "Suspend" : "Activate"} User
            </DialogTitle>
            <DialogDescription>
              Are you sure you want to {userToSuspend?.status === "active" ? "suspend" : "activate"}{" "}
              {userToSuspend?.name}?
              {userToSuspend?.status === "active"
                ? " They will not be able to log in."
                : " They will regain access to their account."}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSuspendDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              variant={userToSuspend?.status === "active" ? "destructive" : "success"}
              onClick={confirmSuspend}
            >
              {userToSuspend?.status === "active" ? "Suspend" : "Activate"}
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
              Generate a temporary password for {userToReset?.name}? They will receive an email
              with instructions to set a new password.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setResetDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={confirmReset}>Reset Password</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}