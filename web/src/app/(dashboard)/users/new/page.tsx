"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { ArrowLeft, Save, Eye, EyeOff, Mail, Lock, User, Shield } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { IPWhitelistEditor } from "@/components/ip-whitelist-editor"
import { useCreateUser } from "@/lib/hooks/use-users"

export default function NewUserPage() {
  const router = useRouter()
  const createUser = useCreateUser()
  const [showPassword, setShowPassword] = useState(false)
  const [formData, setFormData] = useState({
    name: "",
    email: "",
    password: "",
    confirmPassword: "",
    role: "client" as "admin" | "client",
    ipWhitelist: [] as string[],
    sendWelcomeEmail: true,
  })
  const [errors, setErrors] = useState<Record<string, string>>({})

  const validateForm = () => {
    const newErrors: Record<string, string> = {}

    if (!formData.name.trim()) {
      newErrors.name = "Name is required"
    }

    if (!formData.email.trim()) {
      newErrors.email = "Email is required"
    } else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(formData.email)) {
      newErrors.email = "Invalid email format"
    }

    if (!formData.password) {
      newErrors.password = "Password is required"
    } else if (formData.password.length < 8) {
      newErrors.password = "Password must be at least 8 characters"
    }

    if (formData.password !== formData.confirmPassword) {
      newErrors.confirmPassword = "Passwords do not match"
    }

    setErrors(newErrors)
    return Object.keys(newErrors).length === 0
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateForm()) return
    try {
      await createUser.mutateAsync({
        name: formData.name,
        email: formData.email,
        password: formData.password,
        role: formData.role,
        ip_whitelist: formData.ipWhitelist,
        send_welcome_email: formData.sendWelcomeEmail,
      })
      router.push("/users")
    } catch (err) {
      setErrors({ submit: (err as Error).message || "Failed to create user" })
    }
  }

  return (
    <div className="max-w-3xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <Link
          href="/users"
          className="flex items-center gap-2 text-sm font-bold uppercase text-gray-500 hover:text-black mb-4"
        >
          <ArrowLeft className="w-4 h-4" />
          Back to Users
        </Link>
        <h1 className="text-3xl font-black uppercase tracking-tight text-black">
          Add New User
        </h1>
        <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
          Create a new user account
        </p>
      </div>

      <form onSubmit={handleSubmit}>
        <div className="space-y-6">
          {/* Basic Information */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">Basic Information</CardTitle>
              <CardDescription>Enter the user&apos;s basic details</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="name">Full Name</Label>
                <div className="relative">
                  <User className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
                  <Input
                    id="name"
                    type="text"
                    placeholder="John Doe"
                    value={formData.name}
                    onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                    className="pl-10"
                  />
                </div>
                {errors.name && <p className="text-xs text-danger font-bold">{errors.name}</p>}
              </div>

              <div className="space-y-2">
                <Label htmlFor="email">Email Address</Label>
                <div className="relative">
                  <Mail className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
                  <Input
                    id="email"
                    type="email"
                    placeholder="john@company.com"
                    value={formData.email}
                    onChange={(e) => setFormData({ ...formData, email: e.target.value })}
                    className="pl-10"
                  />
                </div>
                {errors.email && <p className="text-xs text-danger font-bold">{errors.email}</p>}
              </div>
            </CardContent>
          </Card>

          {/* Security */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">Security</CardTitle>
              <CardDescription>Set password and role</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="password">Password</Label>
                <div className="relative">
                  <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
                  <Input
                    id="password"
                    type={showPassword ? "text" : "password"}
                    placeholder="Enter password"
                    value={formData.password}
                    onChange={(e) => setFormData({ ...formData, password: e.target.value })}
                    className="pl-10 pr-10"
                  />
                  <button
                    type="button"
                    onClick={() => setShowPassword(!showPassword)}
                    className="absolute right-3 top-1/2 -translate-y-1/2"
                  >
                    {showPassword ? (
                      <EyeOff className="w-4 h-4 text-gray-600" />
                    ) : (
                      <Eye className="w-4 h-4 text-gray-600" />
                    )}
                  </button>
                </div>
                {errors.password && <p className="text-xs text-danger font-bold">{errors.password}</p>}
              </div>

              <div className="space-y-2">
                <Label htmlFor="confirmPassword">Confirm Password</Label>
                <div className="relative">
                  <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
                  <Input
                    id="confirmPassword"
                    type={showPassword ? "text" : "password"}
                    placeholder="Confirm password"
                    value={formData.confirmPassword}
                    onChange={(e) => setFormData({ ...formData, confirmPassword: e.target.value })}
                    className="pl-10"
                  />
                </div>
                {errors.confirmPassword && (
                  <p className="text-xs text-danger font-bold">{errors.confirmPassword}</p>
                )}
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
                    <SelectValue placeholder="Select role" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="client">
                      <div className="flex items-center gap-2">
                        <User className="w-4 h-4" />
                        Client - Can manage VMs
                      </div>
                    </SelectItem>
                    <SelectItem value="admin">
                      <div className="flex items-center gap-2">
                        <Shield className="w-4 h-4" />
                        Admin - Full access
                      </div>
                    </SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs text-gray-500">
                  Admins can manage users, nodes, and all resources. Clients can only manage their own VMs.
                </p>
              </div>
            </CardContent>
          </Card>

          {/* IP Whitelist */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">IP Whitelist</CardTitle>
              <CardDescription>Restrict access to specific IP addresses (optional)</CardDescription>
            </CardHeader>
            <CardContent>
              <IPWhitelistEditor
                value={formData.ipWhitelist}
                onChange={(ips) => setFormData({ ...formData, ipWhitelist: ips })}
              />
            </CardContent>
          </Card>

          {/* Options */}
          <Card>
            <CardHeader>
              <CardTitle className="text-lg">Options</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="flex items-center gap-2">
                <Checkbox
                  id="sendWelcome"
                  checked={formData.sendWelcomeEmail}
                  onCheckedChange={(checked) =>
                    setFormData({ ...formData, sendWelcomeEmail: checked as boolean })
                  }
                />
                <Label htmlFor="sendWelcome" className="font-normal">
                  Send welcome email with login instructions
                </Label>
              </div>
            </CardContent>
          </Card>

          {/* Actions */}
          {errors.submit && (
            <p className="text-sm font-bold text-danger text-right">{errors.submit}</p>
          )}
          <div className="flex items-center justify-end gap-4">
            <Link href="/users">
              <Button variant="outline" type="button">
                Cancel
              </Button>
            </Link>
            <Button type="submit" disabled={createUser.isPending}>
              <Save className="w-4 h-4 mr-2" />
              {createUser.isPending ? "Creating..." : "Create User"}
            </Button>
          </div>
        </div>
      </form>
    </div>
  )
}