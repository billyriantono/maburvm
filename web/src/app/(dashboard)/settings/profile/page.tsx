"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import QRCode from "qrcode"
import { zodResolver } from "@hookform/resolvers/zod"
import { useForm } from "react-hook-form"
import { z } from "zod"
import {
  User as UserIcon,
  Mail,
  Lock,
  Shield,
  ShieldCheck,
  ShieldOff,
  Key,
  Copy,
  RefreshCw,
  Eye,
  EyeOff,
  CheckCircle,
  AlertTriangle,
  AlertCircle,
  Save,
  Loader2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useCurrentUser } from "@/lib/hooks/use-auth"

// Password change schema
const passwordSchema = z.object({
  oldPassword: z.string().min(1, "Current password is required"),
  newPassword: z
    .string()
    .min(8, "Password must be at least 8 characters")
    .regex(/[A-Z]/, "Password must contain at least one uppercase letter")
    .regex(/[a-z]/, "Password must contain at least one lowercase letter")
    .regex(/[0-9]/, "Password must contain at least one number")
    .regex(/[^A-Za-z0-9]/, "Password must contain at least one special character"),
  confirmPassword: z.string(),
}).refine((data) => data.newPassword === data.confirmPassword, {
  message: "Passwords don't match",
  path: ["confirmPassword"],
})

// TOTP verification schema
const totpSchema = z.object({
  code: z.string().length(6, "Code must be 6 digits").regex(/^\d+$/, "Code must be numeric"),
})

type PasswordFormData = z.infer<typeof passwordSchema>
type TOTPFormData = z.infer<typeof totpSchema>

// Generate backup codes
function generateBackupCodes(): string[] {
  const codes: string[] = []
  for (let i = 0; i < 10; i++) {
    const code = Array.from({ length: 8 }, () =>
      Math.random() > 0.5
        ? String.fromCharCode(65 + Math.floor(Math.random() * 26))
        : Math.floor(Math.random() * 10)
    ).join("")
    codes.push(code)
  }
  return codes
}

export default function ProfileSettingsPage() {
  const router = useRouter()
  
  // Data hook
  const { data: user, isLoading: userLoading, error: userError } = useCurrentUser()
  
  const [showPasswordForm, setShowPasswordForm] = useState(false)
  const [showCurrentPassword, setShowCurrentPassword] = useState(false)
  const [showNewPassword, setShowNewPassword] = useState(false)
  const [showConfirmPassword, setShowConfirmPassword] = useState(false)
  const [isLoading, setIsLoading] = useState(false)

  // 2FA state
  const [twoFactorEnabled, setTwoFactorEnabled] = useState(false)
  const [showSetupDialog, setShowSetupDialog] = useState(false)
  const [showBackupCodes, setShowBackupCodes] = useState(false)
  const [showDisableDialog, setShowDisableDialog] = useState(false)
  const [setupStep, setSetupStep] = useState<"qr" | "verify" | "backup">("qr")
  const [qrCodeUrl, setQrCodeUrl] = useState<string>("")
  const [qrCodeImage, setQrCodeImage] = useState<string>("")
  const [backupCodes, setBackupCodes] = useState<string[]>([])
  const [isVerifying, setIsVerifying] = useState(false)

  // Sync 2FA state with user data
  useEffect(() => {
    if (user) {
      setTwoFactorEnabled(!!user.two_factor_secret)
    }
  }, [user])

  // Password form
  const passwordForm = useForm<PasswordFormData>({
    resolver: zodResolver(passwordSchema),
    defaultValues: {
      oldPassword: "",
      newPassword: "",
      confirmPassword: "",
    },
  })

  // TOTP verification form
  const totpForm = useForm<TOTPFormData>({
    resolver: zodResolver(totpSchema),
    defaultValues: {
      code: "",
    },
  })

  // Generate TOTP secret and QR code
  useEffect(() => {
    if (showSetupDialog && setupStep === "qr" && user) {
      // TODO: Replace with useSetup2FA() hook when 2FA setup endpoint is production-ready
      const secret = "JBSWY3DPEHPK3PXP"
      const otpauthUrl = `otpauth://totp/MaburVM:${user.email}?secret=${secret}&issuer=MaburVM`
      
      setQrCodeUrl(otpauthUrl)

      // Generate QR code as data URL
      QRCode.toDataURL(otpauthUrl, {
        width: 200,
        margin: 2,
        color: {
          dark: "#000000",
          light: "#FFFFFF",
        },
      }).then(setQrCodeImage).catch(console.error)
    }
  }, [showSetupDialog, setupStep, user])

  const handlePasswordChange = async (data: PasswordFormData) => {
    setIsLoading(true)
    // Simulate API call
    await new Promise((resolve) => setTimeout(resolve, 1000))
    
    toast.success("Password changed successfully!", {
      description: "Your password has been updated. Please use your new password next time you log in.",
    })
    
    setShowPasswordForm(false)
    passwordForm.reset()
    setIsLoading(false)
  }

  const handleStart2FASetup = () => {
    setShowSetupDialog(true)
    setSetupStep("qr")
    setQrCodeImage("")
  }

  const handleVerifySetup = async (data: TOTPFormData) => {
    setIsVerifying(true)
    
    // Simulate verification (accept any 6-digit code for demo)
    await new Promise((resolve) => setTimeout(resolve, 1000))
    
    // Generate backup codes
    const codes = generateBackupCodes()
    setBackupCodes(codes)
    setSetupStep("backup")
    
    toast.success("2FA enabled successfully!", {
      description: "Save your backup codes in a secure location.",
    })
    
    setTwoFactorEnabled(true)
    setIsVerifying(false)
  }

  const handleCopyBackupCodes = () => {
    navigator.clipboard.writeText(backupCodes.join("\n"))
    toast.success("Backup codes copied!", {
      description: "Save them in a secure location.",
    })
  }

  const handleRegenerateBackupCodes = () => {
    const codes = generateBackupCodes()
    setBackupCodes(codes)
    toast.info("New backup codes generated!", {
      description: "Previous backup codes are no longer valid.",
    })
  }

  const handleDisable2FA = async () => {
    setIsLoading(true)
    await new Promise((resolve) => setTimeout(resolve, 1000))
    
    setTwoFactorEnabled(false)
    setShowDisableDialog(false)
    setShowBackupCodes(false)
    setShowSetupDialog(false)
    
    toast.success("2FA disabled", {
      description: "Two-factor authentication has been disabled for your account.",
    })
    
    setIsLoading(false)
  }

  const handleCloseSetupDialog = () => {
    setShowSetupDialog(false)
    setSetupStep("qr")
    totpForm.reset()
    setBackupCodes([])
  }

  // Loading state
  if (userLoading) {
    return (
      <div className="max-w-4xl mx-auto">
        <div className="mb-8">
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">Profile Settings</h1>
          <Skeleton className="h-5 w-64 mt-1" />
        </div>
        <div className="space-y-6">
          <Skeleton className="h-48 border-4 border-black" />
          <Skeleton className="h-32 border-4 border-black" />
          <Skeleton className="h-24 border-4 border-black" />
        </div>
      </div>
    )
  }

  // Error state
  if (userError || !user) {
    return (
      <div className="max-w-4xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Failed to load profile</h2>
          <p className="text-gray-500 font-medium mb-6">{(userError as Error)?.message || "Could not load user profile."}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <h1 className="text-3xl font-black uppercase tracking-tight text-black">
          Profile Settings
        </h1>
        <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
          Manage your account settings and security
        </p>
      </div>

      <div className="space-y-6">
        {/* Profile Information Card */}
        <Card>
          <CardHeader className="border-b-2 border-black">
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
                <label htmlFor="email" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                  Email Address
                </label>
                <div className="relative">
                  <Input
                    id="email"
                    defaultValue={user.email}
                    className="bg-gray-50 pr-10"
                    disabled
                  />
                  <div className="absolute right-3 top-1/2 -translate-y-1/2">
                    <Badge variant="secondary" className="text-[10px]">
                      <Mail className="w-3 h-3 mr-1" />
                      Verified
                    </Badge>
                  </div>
                </div>
              </div>

              {/* Role */}
              <div>
                <label htmlFor="role" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                  Role
                </label>
                <Input
                  id="role"
                  defaultValue={user.role === "admin" ? "Administrator" : "Client"}
                  className="bg-gray-50"
                  disabled
                />
              </div>

              {/* Account Status */}
              <div>
                <span className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                  Account Status
                </span>
                <div className="flex items-center gap-2">
                  <Badge variant="success" className="text-sm">
                    <CheckCircle className="w-3 h-3 mr-1" />
                    Active
                  </Badge>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Change Password Card */}
        <Card>
          <CardHeader className="border-b-2 border-black">
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
                  <p className="font-medium text-black">
                    Keep your account secure with a strong password
                  </p>
                  <p className="text-sm text-gray-500 mt-1">
                    Last changed: Never (demo account)
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
                  <label htmlFor="oldPassword" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
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
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-black"
                    >
                      {showCurrentPassword ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  {passwordForm.formState.errors.oldPassword && (
                    <p className="text-danger text-sm font-bold mt-1">
                      {passwordForm.formState.errors.oldPassword.message}
                    </p>
                  )}
                </div>

                {/* New Password */}
                <div>
                  <label htmlFor="newPassword" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
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
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-black"
                    >
                      {showNewPassword ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  {passwordForm.formState.errors.newPassword && (
                    <p className="text-danger text-sm font-bold mt-1">
                      {passwordForm.formState.errors.newPassword.message}
                    </p>
                  )}
                  {/* Password requirements */}
                  <div className="mt-2 p-3 bg-gray-50 border-2 border-black">
                    <p className="text-xs font-bold uppercase mb-2">Password requirements:</p>
                    <ul className="text-xs space-y-1">
                      <li className={passwordForm.watch("newPassword")?.length >= 8 ? "text-success" : "text-gray-400"}>
                        {passwordForm.watch("newPassword")?.length >= 8 ? "✓" : "○"} At least 8 characters
                      </li>
                      <li className={/[A-Z]/.test(passwordForm.watch("newPassword") || "") ? "text-success" : "text-gray-400"}>
                        {/[A-Z]/.test(passwordForm.watch("newPassword") || "") ? "✓" : "○"} One uppercase letter
                      </li>
                      <li className={/[a-z]/.test(passwordForm.watch("newPassword") || "") ? "text-success" : "text-gray-400"}>
                        {/[a-z]/.test(passwordForm.watch("newPassword") || "") ? "✓" : "○"} One lowercase letter
                      </li>
                      <li className={/[0-9]/.test(passwordForm.watch("newPassword") || "") ? "text-success" : "text-gray-400"}>
                        {/[0-9]/.test(passwordForm.watch("newPassword") || "") ? "✓" : "○"} One number
                      </li>
                      <li className={/[^A-Za-z0-9]/.test(passwordForm.watch("newPassword") || "") ? "text-success" : "text-gray-400"}>
                        {/[^A-Za-z0-9]/.test(passwordForm.watch("newPassword") || "") ? "✓" : "○"} One special character
                      </li>
                    </ul>
                  </div>
                </div>

                {/* Confirm Password */}
                <div>
                  <label htmlFor="confirmPassword" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
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
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-black"
                    >
                      {showConfirmPassword ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  {passwordForm.formState.errors.confirmPassword && (
                    <p className="text-danger text-sm font-bold mt-1">
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

        {/* Two-Factor Authentication Card */}
        <Card>
          <CardHeader className="border-b-2 border-black">
            <CardTitle className="flex items-center gap-2">
              <Shield className="w-5 h-5" />
              Two-Factor Authentication
            </CardTitle>
            <CardDescription>Add an extra layer of security to your account</CardDescription>
          </CardHeader>
          <CardContent className="p-6">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-4">
                <div
                  className={`w-12 h-12 flex items-center justify-center border-2 border-black ${
                    twoFactorEnabled ? "bg-success" : "bg-gray-200"
                  }`}
                >
                  {twoFactorEnabled ? (
                    <ShieldCheck className="w-6 h-6" />
                  ) : (
                    <ShieldOff className="w-6 h-6 text-gray-500" />
                  )}
                </div>
                <div>
                  <p className="font-bold text-black">
                    {twoFactorEnabled ? "2FA is enabled" : "2FA is disabled"}
                  </p>
                  <p className="text-sm text-gray-500">
                    {twoFactorEnabled
                      ? "Your account is protected with an authenticator app"
                      : "Protect your account with an authenticator app"}
                  </p>
                </div>
              </div>
              <div className="flex items-center gap-2">
                {twoFactorEnabled ? (
                  <>
                    <Button variant="secondary" onClick={() => setShowBackupCodes(true)}>
                      <Key className="w-4 h-4 mr-2" />
                      Backup Codes
                    </Button>
                    <Button variant="destructive" onClick={() => setShowDisableDialog(true)}>
                      <ShieldOff className="w-4 h-4 mr-2" />
                      Disable
                    </Button>
                  </>
                ) : (
                  <Button onClick={handleStart2FASetup}>
                    <ShieldCheck className="w-4 h-4 mr-2" />
                    Enable 2FA
                  </Button>
                )}
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* 2FA Setup Dialog */}
      <Dialog open={showSetupDialog} onOpenChange={(open) => !open && handleCloseSetupDialog()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <ShieldCheck className="w-5 h-5" />
              {setupStep === "qr" && "Set Up Two-Factor Authentication"}
              {setupStep === "verify" && "Verify Your Authenticator"}
              {setupStep === "backup" && "Backup Codes"}
            </DialogTitle>
            <DialogDescription>
              {setupStep === "qr" && "Scan this QR code with your authenticator app"}
              {setupStep === "verify" && "Enter the 6-digit code from your authenticator app"}
              {setupStep === "backup" && "Save these backup codes in a secure location"}
            </DialogDescription>
          </DialogHeader>

          {setupStep === "qr" && (
            <div className="flex flex-col items-center space-y-4">
              <div className="p-4 bg-white border-2 border-black">
                {qrCodeImage ? (
                  // eslint-disable-next-line @next/next/no-img-element, jsx-a11y/alt-text
                  <img src={qrCodeImage} alt="QR Code" className="w-48 h-48" />
                ) : (
                  <div className="w-48 h-48 flex items-center justify-center">
                    <Loader2 className="w-8 h-8 animate-spin" />
                  </div>
                )}
              </div>
              <div className="text-center">
                <p className="text-sm font-medium">Manual entry code:</p>
                <code className="text-xs bg-gray-100 px-2 py-1 border border-black">JBSWY3DPEHPK3PXP</code>
              </div>
              <Button onClick={() => setSetupStep("verify")} className="w-full">
                Next: Verify Code
              </Button>
            </div>
          )}

          {setupStep === "verify" && (
            <form onSubmit={totpForm.handleSubmit(handleVerifySetup)} className="space-y-4">
              <div>
                <Input
                  type="text"
                  placeholder="000000"
                  maxLength={6}
                  className="text-center text-2xl font-mono tracking-widest"
                  {...totpForm.register("code")}
                />
                {totpForm.formState.errors.code && (
                  <p className="text-danger text-sm font-bold mt-1">
                    {totpForm.formState.errors.code.message}
                  </p>
                )}
              </div>
              <div className="flex items-center gap-2 p-3 bg-warning border-2 border-black">
                <AlertTriangle className="w-4 h-4" />
                <p className="text-xs font-bold">
                  For demo: Enter any 6-digit code
                </p>
              </div>
              <Button type="submit" className="w-full" disabled={isVerifying}>
                {isVerifying ? (
                  <>
                    <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                    Verifying...
                  </>
                ) : (
                  <>
                    <ShieldCheck className="w-4 h-4 mr-2" />
                    Verify & Enable 2FA
                  </>
                )}
              </Button>
            </form>
          )}

          {setupStep === "backup" && (
            <div className="space-y-4">
              <div className="p-4 bg-gray-50 border-2 border-black">
                <div className="grid grid-cols-2 gap-2">
                  {backupCodes.map((code) => (
                    <code
                      key={code}
                      className="text-xs font-mono bg-white px-2 py-1 border border-black text-center"
                    >
                      {code}
                    </code>
                  ))}
                </div>
              </div>
              <div className="flex items-center gap-2 p-3 bg-danger text-white border-2 border-black">
                <AlertTriangle className="w-4 h-4" />
                <p className="text-xs font-bold">
                  Save these codes! You won&apos;t see them again.
                </p>
              </div>
              <div className="flex gap-2">
                <Button variant="secondary" onClick={handleCopyBackupCodes} className="flex-1">
                  <Copy className="w-4 h-4 mr-2" />
                  Copy Codes
                </Button>
                <Button variant="secondary" onClick={handleRegenerateBackupCodes} className="flex-1">
                  <RefreshCw className="w-4 h-4 mr-2" />
                  Regenerate
                </Button>
              </div>
              <Button onClick={handleCloseSetupDialog} className="w-full">
                Done
              </Button>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* Backup Codes Display Dialog */}
      <Dialog open={showBackupCodes} onOpenChange={setShowBackupCodes}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Key className="w-5 h-5" />
              Your Backup Codes
            </DialogTitle>
            <DialogDescription>
              Use these codes to access your account if you lose your authenticator
            </DialogDescription>
          </DialogHeader>
          <div className="p-4 bg-gray-50 border-2 border-black">
            <div className="grid grid-cols-2 gap-2">
              {backupCodes.length > 0 ? (
                backupCodes.map((code) => (
                  <code
                    key={code}
                    className="text-xs font-mono bg-white px-2 py-1 border border-black text-center"
                  >
                    {code}
                  </code>
                ))
              ) : (
                <div className="col-span-2 text-center py-4">
                  <p className="text-sm text-gray-500">No backup codes available</p>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="mt-2"
                    onClick={handleStart2FASetup}
                  >
                    Generate New Codes
                  </Button>
                </div>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowBackupCodes(false)}>
              Close
            </Button>
            {backupCodes.length > 0 && (
              <Button variant="secondary" onClick={handleCopyBackupCodes}>
                <Copy className="w-4 h-4 mr-2" />
                Copy All
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Disable 2FA Dialog */}
      <Dialog open={showDisableDialog} onOpenChange={setShowDisableDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="w-5 h-5 text-danger" />
              Disable Two-Factor Authentication?
            </DialogTitle>
            <DialogDescription>
              This will make your account less secure. You will only need your password to log in.
            </DialogDescription>
          </DialogHeader>
          <div className="p-4 bg-warning border-2 border-black">
            <p className="text-sm font-bold">
              Are you sure you want to disable 2FA?
            </p>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowDisableDialog(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleDisable2FA} disabled={isLoading}>
              {isLoading ? (
                <>
                  <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                  Disabling...
                </>
              ) : (
                <>
                  <ShieldOff className="w-4 h-4 mr-2" />
                  Disable 2FA
                </>
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}