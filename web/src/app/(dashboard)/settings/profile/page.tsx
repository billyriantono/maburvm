"use client"

import { useState } from "react"
import { toast } from "sonner"
import { zodResolver } from "@hookform/resolvers/zod"
import { useForm } from "react-hook-form"
import { z } from "zod"
import {
  User as UserIcon,
  Lock,
  Eye,
  EyeOff,
  AlertCircle,
  Save,
  Loader2,
  Gauge,
  Cpu,
  MemoryStick,
  HardDrive,
  Server,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { TwoFactorSettings } from "@/components/two-factor-settings"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useChangePassword } from "@/lib/hooks/use-settings"
import { useMyQuota } from "@/lib/hooks/use-quota"

// The panel checks a new password against the operator's policy (Settings,
// System, Security) and says which rule it breaks; the form does not guess it.
const passwordSchema = z.object({
  oldPassword: z.string().min(1, "Current password is required"),
  newPassword: z.string().min(1, "Enter a new password"),
  confirmPassword: z.string(),
}).refine((data) => data.newPassword === data.confirmPassword, {
  message: "Passwords don't match",
  path: ["confirmPassword"],
})

type PasswordFormData = z.infer<typeof passwordSchema>

// QuotaMeter renders one resource dimension as a labelled usage bar.
// A limit of 0 means unlimited (shown as ∞ with an empty track).
function QuotaMeter({
  icon: Icon,
  label,
  used,
  limit,
  unit = "",
}: {
  icon: typeof Cpu
  label: string
  used: number
  limit: number
  unit?: string
}) {
  const unlimited = !limit || limit <= 0
  const pct = unlimited ? 0 : Math.min(100, Math.round((used / limit) * 100))
  const over = !unlimited && used > limit
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <span className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
          <Icon className="w-4 h-4" />
          {label}
        </span>
        <span className="text-sm font-mono font-medium text-foreground">
          {used.toLocaleString()}{unit} / {unlimited ? "∞" : `${limit.toLocaleString()}${unit}`}
        </span>
      </div>
      <div className="h-2 w-full rounded-full bg-muted overflow-hidden">
        <div
          className={over ? "h-full bg-destructive transition-[width] duration-500 ease-out" : "h-full bg-emerald-600 transition-[width] duration-500 ease-out"}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

export default function ProfileSettingsPage() {
  const { data: user, isLoading: userLoading, error: userError, refetch: refetchUser } = useCurrentUser()
  const { data: quota } = useMyQuota()
  const changePassword = useChangePassword()

  const [showPasswordForm, setShowPasswordForm] = useState(false)
  const [showCurrentPassword, setShowCurrentPassword] = useState(false)
  const [showNewPassword, setShowNewPassword] = useState(false)
  const [showConfirmPassword, setShowConfirmPassword] = useState(false)
  const [isLoading, setIsLoading] = useState(false)

  // Password form
  const passwordForm = useForm<PasswordFormData>({
    resolver: zodResolver(passwordSchema),
    defaultValues: {
      oldPassword: "",
      newPassword: "",
      confirmPassword: "",
    },
  })

  const handlePasswordChange = async (data: PasswordFormData) => {
    setIsLoading(true)
    try {
      await changePassword.mutateAsync({
        current_password: data.oldPassword,
        new_password: data.newPassword,
      })
      toast.success("Password changed", {
        description: "Other sessions were signed out. Sign in again with the new password.",
      })
      setShowPasswordForm(false)
      passwordForm.reset()
    } catch (err) {
      toast.error("Failed to change password", {
        description: (err as Error).message || "Check your current password and try again.",
      })
    } finally {
      setIsLoading(false)
    }
  }

  // Loading state
  if (userLoading) {
    return (
      <div className="max-w-4xl mx-auto">
        <div className="mb-8">
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">Profile Settings</h1>
          <Skeleton className="h-5 w-64 mt-1" />
        </div>
        <div className="space-y-6">
          <Skeleton className="h-48" />
          <Skeleton className="h-32" />
          <Skeleton className="h-24" />
        </div>
      </div>
    )
  }

  // Error state
  if (userError || !user) {
    return (
      <div className="max-w-4xl mx-auto">
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-12 text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" />
          <h2 className="text-xl font-semibold mb-2">Failed to load profile</h2>
          <p className="text-muted-foreground mb-6">{(userError as Error)?.message || "Could not load user profile."}</p>
          <Button onClick={() => refetchUser()}>Retry</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">
          Profile Settings
        </h1>
        <p className="text-muted-foreground text-sm mt-1">
          Manage your account settings and security
        </p>
      </div>

      <div className="space-y-6">
        {/* Profile Information Card */}
        <Card>
          <CardHeader className="border-b">
            <CardTitle className="flex items-center gap-2">
              <UserIcon className="w-5 h-5" />
              Profile Information
            </CardTitle>
            <CardDescription>Your account details</CardDescription>
          </CardHeader>
          <CardContent className="p-6 space-y-4">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
              {/* Email */}
              <div>
                <label htmlFor="email" className="block text-sm font-medium text-muted-foreground mb-2">
                  Email Address
                </label>
                <div className="relative">
                  <Input
                    id="email"
                    defaultValue={user.email}
                    className="bg-muted pr-10"
                    disabled
                  />
                </div>
              </div>

              {/* Role */}
              <div>
                <label htmlFor="role" className="block text-sm font-medium text-muted-foreground mb-2">
                  Role
                </label>
                <Input
                  id="role"
                  defaultValue={user.role === "admin" ? "Administrator" : "Client"}
                  className="bg-muted"
                  disabled
                />
              </div>
            </div>
          </CardContent>
        </Card>

        <TwoFactorSettings />

        {/* Resource Quota Card */}
        {quota?.usage && quota?.quota && (
          <Card>
            <CardHeader className="border-b">
              <CardTitle className="flex items-center gap-2">
                <Gauge className="w-5 h-5" />
                Resource Quota
              </CardTitle>
              <CardDescription>Your usage against account limits (∞ = unlimited)</CardDescription>
            </CardHeader>
            <CardContent className="p-6 space-y-4">
              <QuotaMeter icon={Server} label="Virtual Machines" used={quota.usage.vms} limit={quota.quota.max_vms} />
              <QuotaMeter icon={Cpu} label="vCPU" used={quota.usage.vcpu} limit={quota.quota.max_vcpu} />
              <QuotaMeter icon={MemoryStick} label="Memory" used={quota.usage.ram_mb} limit={quota.quota.max_ram_mb} unit=" MB" />
              <QuotaMeter icon={HardDrive} label="Disk" used={quota.usage.disk_gb} limit={quota.quota.max_disk_gb} unit=" GB" />
            </CardContent>
          </Card>
        )}

        {/* Change Password Card */}
        <Card>
          <CardHeader className="border-b">
            <CardTitle className="flex items-center gap-2">
              <Lock className="w-5 h-5" />
              Change Password
            </CardTitle>
            <CardDescription>Update your account password</CardDescription>
          </CardHeader>
          <CardContent className="p-6">
            {!showPasswordForm ? (
              <div className="flex items-center justify-between">
                <div>
                  <p className="font-medium text-foreground">
                    Keep your account secure with a strong password
                  </p>
                </div>
                <Button onClick={() => setShowPasswordForm(true)}>
                  <Lock className="w-4 h-4 mr-2" />
                  Change Password
                </Button>
              </div>
            ) : (
              <form onSubmit={passwordForm.handleSubmit(handlePasswordChange)} className="space-y-4">
                {/* Current Password */}
                <div>
                  <label htmlFor="oldPassword" className="block text-sm font-medium text-muted-foreground mb-2">
                    Current Password
                  </label>
                  <div className="relative">
                    <Input
                      id="oldPassword"
                      type={showCurrentPassword ? "text" : "password"}
                      placeholder="Enter current password"
                      {...passwordForm.register("oldPassword")}
                      className="pr-10"
                    />
                    <button
                      type="button"
                      onClick={() => setShowCurrentPassword(!showCurrentPassword)}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    >
                      {showCurrentPassword ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  {passwordForm.formState.errors.oldPassword && (
                    <p className="text-destructive text-sm mt-1">
                      {passwordForm.formState.errors.oldPassword.message}
                    </p>
                  )}
                </div>

                {/* New Password */}
                <div>
                  <label htmlFor="newPassword" className="block text-sm font-medium text-muted-foreground mb-2">
                    New Password
                  </label>
                  <div className="relative">
                    <Input
                      id="newPassword"
                      type={showNewPassword ? "text" : "password"}
                      placeholder="Enter new password"
                      {...passwordForm.register("newPassword")}
                      className="pr-10"
                    />
                    <button
                      type="button"
                      onClick={() => setShowNewPassword(!showNewPassword)}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    >
                      {showNewPassword ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  {passwordForm.formState.errors.newPassword && (
                    <p className="text-destructive text-sm mt-1">
                      {passwordForm.formState.errors.newPassword.message}
                    </p>
                  )}
                  <p className="mt-2 text-xs text-muted-foreground">
                    The panel&apos;s password rules apply. If the new password breaks one, you are told which.
                  </p>
                </div>

                {/* Confirm Password */}
                <div>
                  <label htmlFor="confirmPassword" className="block text-sm font-medium text-muted-foreground mb-2">
                    Confirm New Password
                  </label>
                  <div className="relative">
                    <Input
                      id="confirmPassword"
                      type={showConfirmPassword ? "text" : "password"}
                      placeholder="Confirm new password"
                      {...passwordForm.register("confirmPassword")}
                      className="pr-10"
                    />
                    <button
                      type="button"
                      onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    >
                      {showConfirmPassword ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  {passwordForm.formState.errors.confirmPassword && (
                    <p className="text-destructive text-sm mt-1">
                      {passwordForm.formState.errors.confirmPassword.message}
                    </p>
                  )}
                </div>

                {/* Actions */}
                <div className="flex items-center gap-3 pt-4">
                  <Button type="submit" disabled={isLoading}>
                    {isLoading ? (
                      <>
                        <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                        Saving...
                      </>
                    ) : (
                      <>
                        <Save className="w-4 h-4 mr-2" />
                        Update Password
                      </>
                    )}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => {
                      setShowPasswordForm(false)
                      passwordForm.reset()
                    }}
                  >
                    Cancel
                  </Button>
                </div>
              </form>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}