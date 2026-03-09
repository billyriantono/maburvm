"use client"

import { useState } from "react"
import { toast } from "sonner"
import { zodResolver } from "@hookform/resolvers/zod"
import { useForm } from "react-hook-form"
import { z } from "zod"
import {
  Settings as SettingsIcon,
  Globe,
  Shield,
  Database,
  Key,
  Mail,
  Save,
  Loader2,
  Eye,
  EyeOff,
  RotateCcw,
  CheckCircle,
  AlertTriangle,
  Info,
  Clock,
  Calendar,
  Timer,
  Link,
  Lock,
  Server,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"

// General settings schema
const generalSchema = z.object({
  panelName: z.string().min(1, "Panel name is required"),
  timezone: z.string().min(1, "Timezone is required"),
})

// Security settings schema
const securitySchema = z.object({
  sessionTimeout: z.number().min(5, "Minimum 5 minutes").max(1440, "Maximum 1440 minutes"),
  minPasswordLength: z.number().min(8, "Minimum 8 characters").max(128, "Maximum 128 characters"),
  requireUppercase: z.boolean(),
  requireNumbers: z.boolean(),
  requireSpecial: z.boolean(),
  maxLoginAttempts: z.number().min(3).max(10),
  lockoutDuration: z.number().min(1).max(60),
})

// Backup settings schema
const backupSchema = z.object({
  defaultRetention: z.number().min(1, "Minimum 1 day").max(365, "Maximum 365 days"),
  schedule: z.string().min(1, "Schedule is required"),
  autoCleanup: z.boolean(),
})

// API settings schema
const apiSchema = z.object({
  webhookUrl: z.string().url("Invalid URL").or(z.literal("")),
  hmacSecret: z.string().min(32, "HMAC secret must be at least 32 characters").or(z.literal("")),
  enableApi: z.boolean(),
  rateLimit: z.number().min(10).max(1000),
})

// Email settings schema
const emailSchema = z.object({
  smtpHost: z.string().min(1, "SMTP host is required"),
  smtpPort: z.number().min(1).max(65535),
  smtpUser: z.string().min(1, "SMTP user is required"),
  smtpPassword: z.string().min(1, "SMTP password is required"),
  smtpFrom: z.string().email("Invalid email").or(z.literal("")),
  smtpFromName: z.string().min(1, "From name is required"),
  enableTls: z.boolean(),
})

type GeneralFormData = z.infer<typeof generalSchema>
type SecurityFormData = z.infer<typeof securitySchema>
type BackupFormData = z.infer<typeof backupSchema>
type APIFormData = z.infer<typeof apiSchema>
type EmailFormData = z.infer<typeof emailSchema>

// Default form values — will be populated from API when system settings endpoint is available
const defaultGeneralSettings = {
  panelName: "MaburVM",
  timezone: "UTC",
}

const defaultSecuritySettings = {
  sessionTimeout: 60,
  minPasswordLength: 12,
  requireUppercase: true,
  requireNumbers: true,
  requireSpecial: true,
  maxLoginAttempts: 5,
  lockoutDuration: 15,
}

const defaultBackupSettings = {
  defaultRetention: 30,
  schedule: "0 2 * * *",
  autoCleanup: true,
}

const defaultApiSettings = {
  webhookUrl: "",
  hmacSecret: "your-hmac-secret-key-here-min-32-chars",
  enableApi: true,
  rateLimit: 100,
}

const defaultEmailSettings = {
  smtpHost: "smtp.example.com",
  smtpPort: 587,
  smtpUser: "notifications@maburvm.local",
  smtpPassword: "********",
  smtpFrom: "notifications@maburvm.local",
  smtpFromName: "MaburVM Notifications",
  enableTls: true,
}

// Timezone options
const timezones = [
  { value: "UTC", label: "UTC" },
  { value: "America/New_York", label: "Eastern Time (US)" },
  { value: "America/Chicago", label: "Central Time (US)" },
  { value: "America/Denver", label: "Mountain Time (US)" },
  { value: "America/Los_Angeles", label: "Pacific Time (US)" },
  { value: "Europe/London", label: "London" },
  { value: "Europe/Paris", label: "Paris" },
  { value: "Asia/Tokyo", label: "Tokyo" },
  { value: "Asia/Shanghai", label: "Shanghai" },
  { value: "Australia/Sydney", label: "Sydney" },
]

// Backup schedule presets
const schedulePresets = [
  { value: "0 2 * * *", label: "Daily at 2:00 AM" },
  { value: "0 2 * * 0", label: "Weekly on Sunday at 2:00 AM" },
  { value: "0 2 1 * *", label: "Monthly on the 1st at 2:00 AM" },
  { value: "0 */6 * * *", label: "Every 6 hours" },
  { value: "0 */12 * * *", label: "Every 12 hours" },
]

export default function SystemSettingsPage() {
  const [isSaving, setIsSaving] = useState<string | null>(null)
  const [showHmacSecret, setShowHmacSecret] = useState(false)

  // General form
  const generalForm = useForm<GeneralFormData>({
    resolver: zodResolver(generalSchema),
    defaultValues: defaultGeneralSettings,
  })

  // Security form
  const securityForm = useForm<SecurityFormData>({
    resolver: zodResolver(securitySchema),
    defaultValues: defaultSecuritySettings,
  })

  // Backup form
  const backupForm = useForm<BackupFormData>({
    resolver: zodResolver(backupSchema),
    defaultValues: defaultBackupSettings,
  })

  // API form
  const apiForm = useForm<APIFormData>({
    resolver: zodResolver(apiSchema),
    defaultValues: defaultApiSettings,
  })

  // Email form
  const emailForm = useForm<EmailFormData>({
    resolver: zodResolver(emailSchema),
    defaultValues: defaultEmailSettings,
  })

  const handleSaveGeneral = async (data: GeneralFormData) => {
    setIsSaving("general")
    await new Promise((resolve) => setTimeout(resolve, 1000))
    toast.success("General settings saved!", {
      description: `Panel name: ${data.panelName}, Timezone: ${data.timezone}`,
    })
    setIsSaving(null)
  }

  const handleSaveSecurity = async (data: SecurityFormData) => {
    setIsSaving("security")
    await new Promise((resolve) => setTimeout(resolve, 1000))
    toast.success("Security settings saved!", {
      description: `Session timeout: ${data.sessionTimeout} minutes`,
    })
    setIsSaving(null)
  }

  const handleSaveBackup = async (data: BackupFormData) => {
    setIsSaving("backup")
    await new Promise((resolve) => setTimeout(resolve, 1000))
    toast.success("Backup settings saved!", {
      description: `Retention: ${data.defaultRetention} days`,
    })
    setIsSaving(null)
  }

  const handleSaveApi = async (data: APIFormData) => {
    setIsSaving("api")
    await new Promise((resolve) => setTimeout(resolve, 1000))
    toast.success("API settings saved!", {
      description: data.enableApi ? "API access is enabled" : "API access is disabled",
    })
    setIsSaving(null)
  }

  const handleSaveEmail = async (data: EmailFormData) => {
    setIsSaving("email")
    await new Promise((resolve) => setTimeout(resolve, 1000))
    toast.success("Email settings saved!", {
      description: `SMTP configured for ${data.smtpHost}`,
    })
    setIsSaving(null)
  }

  const handleTestEmail = async () => {
    toast.info("Sending test email...", {
      description: "Please wait...",
    })
    await new Promise((resolve) => setTimeout(resolve, 1500))
    toast.success("Test email sent!", {
      description: "Check your inbox for the test email.",
    })
  }

  const handleResetSettings = (type: string) => {
    toast.info(`Reset ${type} settings to defaults?`, {
      description: "This will reset all settings in this section to defaults.",
      action: {
        label: "Reset",
        onClick: () => {
          toast.success(`${type} settings reset to defaults`)
        },
      },
    })
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black">
              System Settings
            </h1>
            <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
              Configure your virtualization panel
            </p>
          </div>
          <Badge variant="secondary" className="text-sm">
            <Shield className="w-3 h-3 mr-1" />
            Admin Only
          </Badge>
        </div>
      </div>

      {/* Settings Tabs */}
      <Tabs defaultValue="general" className="space-y-6">
        <TabsList className="grid w-full grid-cols-2 md:grid-cols-5 gap-2 bg-transparent p-0 border-none">
          <TabsTrigger value="general" className="font-bold uppercase text-xs">
            <Globe className="w-4 h-4 mr-2" />
            General
          </TabsTrigger>
          <TabsTrigger value="security" className="font-bold uppercase text-xs">
            <Shield className="w-4 h-4 mr-2" />
            Security
          </TabsTrigger>
          <TabsTrigger value="backup" className="font-bold uppercase text-xs">
            <Database className="w-4 h-4 mr-2" />
            Backup
          </TabsTrigger>
          <TabsTrigger value="api" className="font-bold uppercase text-xs">
            <Key className="w-4 h-4 mr-2" />
            API
          </TabsTrigger>
          <TabsTrigger value="email" className="font-bold uppercase text-xs">
            <Mail className="w-4 h-4 mr-2" />
            Email
          </TabsTrigger>
        </TabsList>

        {/* General Settings */}
        <TabsContent value="general">
          <Card>
            <CardHeader className="border-b-2 border-black">
              <CardTitle className="flex items-center gap-2">
                <Globe className="w-5 h-5" />
                General Settings
              </CardTitle>
              <CardDescription>Basic panel configuration</CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={generalForm.handleSubmit(handleSaveGeneral)} className="space-y-6">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                  {/* Panel Name */}
                  <div>
                    <label htmlFor="panelName" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Panel Name
                    </label>
                    <Input
                      id="panelName"
                      {...generalForm.register("panelName")}
                      placeholder="MaburVM"
                    />
                    {generalForm.formState.errors.panelName && (
                      <p className="text-danger text-sm font-bold mt-1">
                        {generalForm.formState.errors.panelName.message}
                      </p>
                    )}
                  </div>

                  {/* Timezone */}
                  <div>
                    <label htmlFor="timezone" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Timezone
                    </label>
                    <select
                      id="timezone"
                      {...generalForm.register("timezone")}
                      className="w-full px-4 py-3 bg-white border-2 border-black focus:outline-none focus:shadow-neo-hover focus:translate-x-[-2px] focus:translate-y-[-2px]"
                    >
                      {timezones.map((tz) => (
                        <option key={tz.value} value={tz.value}>
                          {tz.label}
                        </option>
                      ))}
                    </select>
                    {generalForm.formState.errors.timezone && (
                      <p className="text-danger text-sm font-bold mt-1">
                        {generalForm.formState.errors.timezone.message}
                      </p>
                    )}
                  </div>
                </div>

                {/* Info Box */}
                <div className="flex items-start gap-3 p-4 bg-secondary/20 border-2 border-black">
                  <Info className="w-5 h-5 text-secondary flex-shrink-0 mt-0.5" />
                  <div>
                    <p className="text-sm font-bold">Panel Information</p>
                    <p className="text-xs text-gray-600 mt-1">
                      Version: 1.0.0 | License: Enterprise | Nodes: 3
                    </p>
                  </div>
                </div>

                <div className="flex items-center justify-between pt-4 border-t-2 border-black">
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => handleResetSettings("General")}
                  >
                    <RotateCcw className="w-4 h-4 mr-2" />
                    Reset to Defaults
                  </Button>
                  <Button type="submit" disabled={isSaving === "general"}>
                    {isSaving === "general" ? (
                      <>
                        <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                        Saving...
                      </>
                    ) : (
                      <>
                        <Save className="w-4 h-4 mr-2" />
                        Save Settings
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>

        {/* Security Settings */}
        <TabsContent value="security">
          <Card>
            <CardHeader className="border-b-2 border-black">
              <CardTitle className="flex items-center gap-2">
                <Shield className="w-5 h-5" />
                Security Settings
              </CardTitle>
              <CardDescription>Configure security policies</CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={securityForm.handleSubmit(handleSaveSecurity)} className="space-y-6">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                  {/* Session Timeout */}
                  <div>
                    <label htmlFor="sessionTimeout" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Session Timeout (minutes)
                    </label>
                    <div className="relative">
                      <Timer className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                      <Input
                        id="sessionTimeout"
                        type="number"
                        {...securityForm.register("sessionTimeout")}
                        className="pl-10"
                        min={5}
                        max={1440}
                      />
                    </div>
                    {securityForm.formState.errors.sessionTimeout && (
                      <p className="text-danger text-sm font-bold mt-1">
                        {securityForm.formState.errors.sessionTimeout.message}
                      </p>
                    )}
                  </div>

                  {/* Min Password Length */}
                  <div>
                    <label htmlFor="minPasswordLength" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Minimum Password Length
                    </label>
                    <Input
                      id="minPasswordLength"
                      type="number"
                      {...securityForm.register("minPasswordLength")}
                      min={8}
                      max={128}
                    />
                    {securityForm.formState.errors.minPasswordLength && (
                      <p className="text-danger text-sm font-bold mt-1">
                        {securityForm.formState.errors.minPasswordLength.message}
                      </p>
                    )}
                  </div>

                  {/* Max Login Attempts */}
                  <div>
                    <label htmlFor="maxLoginAttempts" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Max Login Attempts
                    </label>
                    <Input
                      id="maxLoginAttempts"
                      type="number"
                      {...securityForm.register("maxLoginAttempts")}
                      min={3}
                      max={10}
                    />
                  </div>

                  {/* Lockout Duration */}
                  <div>
                    <label htmlFor="lockoutDuration" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Lockout Duration (minutes)
                    </label>
                    <Input
                      id="lockoutDuration"
                      type="number"
                      {...securityForm.register("lockoutDuration")}
                      min={1}
                      max={60}
                    />
                  </div>
                </div>

                {/* Password Policy Checkboxes */}
                <div className="p-4 bg-gray-50 border-2 border-black">
                  <p className="text-sm font-bold uppercase mb-4">Password Requirements</p>
                  <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input
                        type="checkbox"
                        {...securityForm.register("requireUppercase")}
                        className="w-5 h-5 accent-primary"
                      />
                      <span className="font-medium">Require Uppercase</span>
                    </label>
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input
                        type="checkbox"
                        {...securityForm.register("requireNumbers")}
                        className="w-5 h-5 accent-primary"
                      />
                      <span className="font-medium">Require Numbers</span>
                    </label>
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input
                        type="checkbox"
                        {...securityForm.register("requireSpecial")}
                        className="w-5 h-5 accent-primary"
                      />
                      <span className="font-medium">Require Special Characters</span>
                    </label>
                  </div>
                </div>

                <div className="flex items-center justify-between pt-4 border-t-2 border-black">
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => handleResetSettings("Security")}
                  >
                    <RotateCcw className="w-4 h-4 mr-2" />
                    Reset to Defaults
                  </Button>
                  <Button type="submit" disabled={isSaving === "security"}>
                    {isSaving === "security" ? (
                      <>
                        <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                        Saving...
                      </>
                    ) : (
                      <>
                        <Save className="w-4 h-4 mr-2" />
                        Save Settings
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>

        {/* Backup Settings */}
        <TabsContent value="backup">
          <Card>
            <CardHeader className="border-b-2 border-black">
              <CardTitle className="flex items-center gap-2">
                <Database className="w-5 h-5" />
                Backup Settings
              </CardTitle>
              <CardDescription>Configure automated backups</CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={backupForm.handleSubmit(handleSaveBackup)} className="space-y-6">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                  {/* Default Retention */}
                  <div>
                    <label htmlFor="defaultRetention" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Default Retention (days)
                    </label>
                    <div className="relative">
                      <Calendar className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                      <Input
                        id="defaultRetention"
                        type="number"
                        {...backupForm.register("defaultRetention")}
                        className="pl-10"
                        min={1}
                        max={365}
                      />
                    </div>
                  </div>

                  {/* Schedule */}
                  <div>
                    <label htmlFor="schedule" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Backup Schedule
                    </label>
                    <div className="relative">
                      <Clock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                      <select
                        id="schedule"
                        {...backupForm.register("schedule")}
                        className="w-full pl-10 px-4 py-3 bg-white border-2 border-black focus:outline-none focus:shadow-neo-hover focus:translate-x-[-2px] focus:translate-y-[-2px]"
                      >
                        {schedulePresets.map((preset) => (
                          <option key={preset.value} value={preset.value}>
                            {preset.label}
                          </option>
                        ))}
                      </select>
                    </div>
                  </div>
                </div>

                {/* Auto Cleanup */}
                <div className="p-4 bg-gray-50 border-2 border-black">
                  <label className="flex items-center gap-3 cursor-pointer">
                    <input
                      type="checkbox"
                      {...backupForm.register("autoCleanup")}
                      className="w-5 h-5 accent-primary"
                    />
                    <div>
                      <span className="font-bold">Auto Cleanup</span>
                      <p className="text-xs text-gray-500">
                        Automatically delete backups older than retention period
                      </p>
                    </div>
                  </label>
                </div>

                <div className="flex items-center justify-between pt-4 border-t-2 border-black">
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => handleResetSettings("Backup")}
                  >
                    <RotateCcw className="w-4 h-4 mr-2" />
                    Reset to Defaults
                  </Button>
                  <Button type="submit" disabled={isSaving === "backup"}>
                    {isSaving === "backup" ? (
                      <>
                        <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                        Saving...
                      </>
                    ) : (
                      <>
                        <Save className="w-4 h-4 mr-2" />
                        Save Settings
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>

        {/* API Settings */}
        <TabsContent value="api">
          <Card>
            <CardHeader className="border-b-2 border-black">
              <CardTitle className="flex items-center gap-2">
                <Key className="w-5 h-5" />
                API Settings
              </CardTitle>
              <CardDescription>Configure API access and webhooks</CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={apiForm.handleSubmit(handleSaveApi)} className="space-y-6">
                {/* Enable API */}
                <div className="p-4 bg-gray-50 border-2 border-black">
                  <label className="flex items-center gap-3 cursor-pointer">
                    <input
                      type="checkbox"
                      {...apiForm.register("enableApi")}
                      className="w-5 h-5 accent-primary"
                    />
                    <div>
                      <span className="font-bold">Enable API Access</span>
                      <p className="text-xs text-gray-500">
                        Allow external applications to access the API
                      </p>
                    </div>
                  </label>
                </div>

                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                  {/* Webhook URL */}
                  <div>
                    <label htmlFor="webhookUrl" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Webhook URL
                    </label>
                    <div className="relative">
                      <Link className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                      <Input
                        id="webhookUrl"
                        {...apiForm.register("webhookUrl")}
                        placeholder="https://your-webhook.com/endpoint"
                        className="pl-10"
                      />
                    </div>
                    {apiForm.formState.errors.webhookUrl && (
                      <p className="text-danger text-sm font-bold mt-1">
                        {apiForm.formState.errors.webhookUrl.message}
                      </p>
                    )}
                  </div>

                  {/* Rate Limit */}
                  <div>
                    <label htmlFor="rateLimit" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      Rate Limit (requests/min)
                    </label>
                    <Input
                      id="rateLimit"
                      type="number"
                      {...apiForm.register("rateLimit")}
                      min={10}
                      max={1000}
                    />
                  </div>
                </div>

                {/* HMAC Secret */}
                <div>
                  <label htmlFor="hmacSecret" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                    HMAC Secret
                  </label>
                  <div className="relative">
                    <Input
                      id="hmacSecret"
                      type={showHmacSecret ? "text" : "password"}
                      {...apiForm.register("hmacSecret")}
                      className="pr-10"
                    />
                    <button
                      type="button"
                      onClick={() => setShowHmacSecret(!showHmacSecret)}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-black"
                    >
                      {showHmacSecret ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  <p className="text-xs text-gray-500 mt-1">
                    Used to sign API requests. Keep this secret!
                  </p>
                </div>

                <div className="flex items-center justify-between pt-4 border-t-2 border-black">
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => handleResetSettings("API")}
                  >
                    <RotateCcw className="w-4 h-4 mr-2" />
                    Reset to Defaults
                  </Button>
                  <Button type="submit" disabled={isSaving === "api"}>
                    {isSaving === "api" ? (
                      <>
                        <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                        Saving...
                      </>
                    ) : (
                      <>
                        <Save className="w-4 h-4 mr-2" />
                        Save Settings
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>

        {/* Email Settings */}
        <TabsContent value="email">
          <Card>
            <CardHeader className="border-b-2 border-black">
              <CardTitle className="flex items-center gap-2">
                <Mail className="w-5 h-5" />
                Email Settings
              </CardTitle>
              <CardDescription>Configure SMTP for notifications</CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={emailForm.handleSubmit(handleSaveEmail)} className="space-y-6">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                  {/* SMTP Host */}
                  <div>
                    <label htmlFor="smtpHost" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      SMTP Host
                    </label>
                    <div className="relative">
                      <Server className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                      <Input
                        id="smtpHost"
                        {...emailForm.register("smtpHost")}
                        placeholder="smtp.example.com"
                        className="pl-10"
                      />
                    </div>
                    {emailForm.formState.errors.smtpHost && (
                      <p className="text-danger text-sm font-bold mt-1">
                        {emailForm.formState.errors.smtpHost.message}
                      </p>
                    )}
                  </div>

                  {/* SMTP Port */}
                  <div>
                    <label htmlFor="smtpPort" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      SMTP Port
                    </label>
                    <Input
                      id="smtpPort"
                      type="number"
                      {...emailForm.register("smtpPort")}
                      min={1}
                      max={65535}
                    />
                  </div>

                  {/* SMTP User */}
                  <div>
                    <label htmlFor="smtpUser" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      SMTP Username
                    </label>
                    <Input
                      id="smtpUser"
                      {...emailForm.register("smtpUser")}
                      placeholder="user@example.com"
                    />
                  </div>

                  {/* SMTP Password */}
                  <div>
                    <label htmlFor="smtpPassword" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      SMTP Password
                    </label>
                    <div className="relative">
                      <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                      <Input
                        id="smtpPassword"
                        type="password"
                        {...emailForm.register("smtpPassword")}
                        className="pl-10"
                      />
                    </div>
                  </div>

                  {/* From Email */}
                  <div>
                    <label htmlFor="smtpFrom" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      From Email
                    </label>
                    <Input
                      id="smtpFrom"
                      type="email"
                      {...emailForm.register("smtpFrom")}
                      placeholder="noreply@example.com"
                    />
                  </div>

                  {/* From Name */}
                  <div>
                    <label htmlFor="smtpFromName" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                      From Name
                    </label>
                    <Input
                      id="smtpFromName"
                      {...emailForm.register("smtpFromName")}
                      placeholder="MaburVM"
                    />
                  </div>
                </div>

                {/* Enable TLS */}
                <div className="p-4 bg-gray-50 border-2 border-black">
                  <label className="flex items-center gap-3 cursor-pointer">
                    <input
                      type="checkbox"
                      {...emailForm.register("enableTls")}
                      className="w-5 h-5 accent-primary"
                    />
                    <div>
                      <span className="font-bold">Enable TLS</span>
                      <p className="text-xs text-gray-500">
                        Use TLS encryption for SMTP connections
                      </p>
                    </div>
                  </label>
                </div>

                <div className="flex items-center justify-between pt-4 border-t-2 border-black">
                  <div className="flex items-center gap-2">
                    <Button
                      type="button"
                      variant="ghost"
                      onClick={() => handleResetSettings("Email")}
                    >
                      <RotateCcw className="w-4 h-4 mr-2" />
                      Reset
                    </Button>
                    <Button
                      type="button"
                      variant="secondary"
                      onClick={handleTestEmail}
                    >
                      <Mail className="w-4 h-4 mr-2" />
                      Test Email
                    </Button>
                  </div>
                  <Button type="submit" disabled={isSaving === "email"}>
                    {isSaving === "email" ? (
                      <>
                        <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                        Saving...
                      </>
                    ) : (
                      <>
                        <Save className="w-4 h-4 mr-2" />
                        Save Settings
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}