"use client"

import { useState, useEffect } from "react"
import { useSystemVersion, useUpdateAgent } from "@/lib/hooks/use-version"
import { OperationProgress, type ProgressStep } from "@/components/operation-progress"
import { useSystemSettings, useSaveSystemSettings, useTestEmail } from "@/lib/hooks/use-settings"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { toast } from "sonner"
import { useFlash } from "@/lib/use-feedback"
import { zodResolver } from "@hookform/resolvers/zod"
import { useForm } from "react-hook-form"
import { z } from "zod"
import {
  Shield,
  Database,
  Key,
  Mail,
  Users,
  Save,
  Check,
  Loader2,
  Eye,
  EyeOff,
  RotateCcw,

  Link,
  Lock,
  Server,
  LifeBuoy,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { DNSProviderSettings } from "@/components/dns-provider-settings"
import { BackupDestinationSettings } from "@/components/backup-destination-settings"
import { BackupPolicySettings } from "@/components/backup-policy-settings"
import { TransferQuotaSettings } from "@/components/transfer-quota-settings"
import { BillingDeliveries } from "@/components/billing-deliveries"
import { RescueImageSettings } from "@/components/rescue-image-settings"
import { PanelConfigSettings } from "@/components/panel-config-settings"
import { EmailLog } from "@/components/email-log"
import { StatusBadge } from "@/components/status-badge"

// The password policy (LoadPasswordPolicy in internal/panel/service/user.go)
// and the sign-in policy (LoadSignInPolicy in internal/panel/middleware/sign_in_policy.go).
const securitySchema = z.object({
  minPasswordLength: z.number().int().min(8, "Minimum 8 characters").max(128, "Maximum 128 characters"),
  requireUppercase: z.boolean(),
  requireNumbers: z.boolean(),
  requireSpecial: z.boolean(),
  lockoutAttempts: z
    .number()
    .int()
    .min(0)
    .max(100, "At most 100")
    .refine((v) => v === 0 || v >= 3, "0 turns lockout off; otherwise at least 3"),
  lockoutMinutes: z.number().int().min(1, "At least 1 minute").max(1440, "At most 1440 minutes (a day)"),
  requireStaff2fa: z.boolean(),
  sessionHours: z.number().int().min(1, "At least 1 hour").max(720, "At most 720 hours (30 days)"),
  // The sign-in check (SignInPolicy.Captcha* in sign_in_policy.go). A blank
  // secret keeps the saved one; the server never sends it back.
  captchaProvider: z.enum(["", "turnstile", "hcaptcha"]),
  captchaSiteKey: z.string().max(200),
  captchaSecret: z.string().max(200),
  captchaAfterFailures: z.number().int().min(0, "0 or more").max(100, "At most 100"),
})

// API settings schema
const apiSchema = z.object({
  webhookUrl: z.string().url("Invalid URL").or(z.literal("")),
  hmacSecret: z.string().min(32, "HMAC secret must be at least 32 characters").or(z.literal("")),
  enableApi: z.boolean(),
  abuseipdbToken: z.string().or(z.literal("")),
  // 0 turns the limit off. Mirrors DefaultAPIRequestsPerMinute in internal/panel/middleware.
  apiRequestsPerMinute: z.number().int().min(0, "0 or more").max(100000),
})

// Per-account limits. Both are kept optional-with-minimum rather than allowing
// zero: a zero would silently switch the feature off for every customer, which
// is never what someone editing a "limit" field intends.
const quotaSchema = z.object({
  vpcMaxPerUser: z.number().min(1).max(100),
  floatingIpMaxPerUser: z.number().min(1).max(100),
  portForwardMaxPerVm: z.number().min(1).max(1000),
  firewallRuleMaxPerVm: z.number().int().min(1).max(1000),
  // What customers may do without the operator or the billing system.
  clientOrderingEnabled: z.boolean(),
  clientCloneEnabled: z.boolean(),
  clientExtraDisksEnabled: z.boolean(),
  clientDeleteBilledEnabled: z.boolean(),
  // Starting quota of a new customer account. 0 = no limit, as on the user page.
  newClientMaxVms: z.number().int().min(0).max(10000),
  newClientMaxVcpu: z.number().int().min(0).max(100000),
  newClientMaxRamMb: z.number().int().min(0).max(100000000),
  newClientMaxDiskGb: z.number().int().min(0).max(100000000),
  // Overcommit ratios allow 0, unlike the limits above: for these, 0 is a
  // meaningful setting ("do not enforce") rather than an accidental lockout.
  ramOvercommitRatio: z.number().min(0).max(20),
  diskAllocationRatio: z.number().min(0).max(20),
  vcpuAllocationRatio: z.number().min(0).max(100),
})

// Email settings schema
const emailSchema = z.object({
  smtpHost: z.string().min(1, "SMTP host is required"),
  smtpPort: z.number().min(1).max(65535),
  smtpUser: z.string().min(1, "SMTP user is required"),
  smtpPassword: z.string(), // blank keeps the saved one; the server never sends it back
  smtpFrom: z.string().email("Invalid email").or(z.literal("")),
  smtpFromName: z.string().min(1, "From name is required"),
  enableTls: z.boolean(),
})

type SecurityFormData = z.infer<typeof securitySchema>
type APIFormData = z.infer<typeof apiSchema>
type QuotaFormData = z.infer<typeof quotaSchema>
type EmailFormData = z.infer<typeof emailSchema>

// Must mirror the built-in values of LoadPasswordPolicy and DefaultSignInPolicy.
const defaultSecuritySettings = {
  minPasswordLength: 12,
  requireUppercase: true,
  requireNumbers: true,
  requireSpecial: true,
  lockoutAttempts: 10,
  lockoutMinutes: 15,
  requireStaff2fa: false,
  sessionHours: 24,
  captchaProvider: "" as "" | "turnstile" | "hcaptcha",
  captchaSiteKey: "",
  captchaSecret: "",
  captchaAfterFailures: 3,
}

const defaultApiSettings = {
  webhookUrl: "",
  hmacSecret: "",
  enableApi: true,
  abuseipdbToken: "",
  apiRequestsPerMinute: 1200,
}

const defaultQuotaSettings = {
  vpcMaxPerUser: 5,
  floatingIpMaxPerUser: 3,
  portForwardMaxPerVm: 20,
  firewallRuleMaxPerVm: 100,
  // Must mirror SelfServiceAllowed and the DefaultNewClient* values in
  // internal/panel/service/settings.go.
  clientOrderingEnabled: true,
  clientCloneEnabled: true,
  clientExtraDisksEnabled: true,
  // Mirrors ClientMayDeleteBilledVMs: off until an operator turns it on.
  clientDeleteBilledEnabled: false,
  newClientMaxVms: 2,
  newClientMaxVcpu: 4,
  newClientMaxRamMb: 8192,
  newClientMaxDiskGb: 100,
  // Must mirror DefaultRAMOvercommitRatio / DefaultDiskAllocationRatio in
  // internal/panel/service/settings.go: 0 = not enforced.
  ramOvercommitRatio: 0,
  diskAllocationRatio: 0,
  vcpuAllocationRatio: 0,
}

const defaultEmailSettings = {
  smtpHost: "",
  smtpPort: 587,
  smtpUser: "",
  smtpPassword: "",
  smtpFrom: "",
  smtpFromName: "",
  enableTls: true,
}

// BuildPanel shows the panel's build and each node's agent build.
//
// Both, because they are deployed separately: the panel can be current while a
// node still runs an older agent — sometimes on purpose, since a node with a
// long export in flight is left alone until it finishes. A single version number
// would hide exactly the difference an operator is checking for.
// One agent update, as far as the panel can see it. There are two things it
// can observe: its own request to the node finishing (the node has checked the
// new agent and scheduled the swap), and the node then reporting the new build.
interface AgentRun {
  name: string
  from: string
  phase: "sending" | "restarting" | "done" | "failed"
  startedAt: string
  acceptedAt?: number
  /** What the node said, or why it failed. */
  note?: string
}

// How long a node gets to come back on the new agent before the update is
// called failed: the swap, the restart and its own 30 second health check.
// The panel keeps watching after this page closes and raises an alert if not.
const AGENT_RETURN_MS = 120_000
// The node rolls back an agent that is not still up 30 seconds after its
// restart, so a new build seen sooner is not yet a finished update.
const AGENT_SETTLE_MS = 45_000

function agentSteps(run: AgentRun): ProgressStep[] {
  const sent = run.phase !== "sending"
  const failedSending = run.phase === "failed" && !run.acceptedAt
  return [
    {
      label: "Send the new agent to the node, which checks it and test-runs it",
      state: failedSending ? "failed" : sent ? "done" : "active",
      detail: failedSending ? run.note : sent ? run.note : "This can take a minute: the agent is a large file.",
    },
    {
      label: "The node swaps the agent in, restarts it and makes sure it stays up",
      state: !sent || failedSending ? "pending" : run.phase === "done" ? "done" : run.phase === "failed" ? "failed" : "active",
      detail:
        run.phase === "restarting"
          ? "The node keeps the new agent only if it is still up 30 seconds after the restart, and otherwise goes back to the one it had. Checked every 5 seconds."
          : run.phase === "failed" && run.acceptedAt
            ? run.note
            : undefined,
    },
  ]
}

function BuildPanel() {
  const { data, isLoading, refetch } = useSystemVersion()
  const updateAgent = useUpdateAgent()
  const [runs, setRuns] = useState<Record<string, AgentRun>>({})
  const patch = (id: string, change: Partial<AgentRun>) => setRuns((r) => (r[id] ? { ...r, [id]: { ...r[id], ...change } } : r))

  const runUpdate = (nodeId: string, name: string, from: string) => {
    setRuns((r) => ({ ...r, [nodeId]: { name, from, phase: "sending", startedAt: new Date().toISOString() } }))
    updateAgent.mutate(nodeId, {
      onSuccess: (res) => patch(nodeId, { phase: "restarting", acceptedAt: Date.now(), note: res.message }),
      onError: (e) => patch(nodeId, { phase: "failed", note: e.message }),
    })
  }

  // While a node is restarting, keep asking every node for its build.
  const waiting = Object.values(runs).some((r) => r.phase === "restarting")
  useEffect(() => {
    if (!waiting) return
    const t = setInterval(() => refetch(), 5000)
    return () => clearInterval(t)
  }, [waiting, refetch])

  // Each answer either settles a waiting update or leaves it waiting.
  useEffect(() => {
    if (!data) return
    for (const [id, run] of Object.entries(runs)) {
      if (run.phase !== "restarting" || !run.acceptedAt) continue
      const node = (data.nodes ?? []).find((n) => n.node_id === id)
      if (node && !node.error && node.matches_panel && Date.now() - run.acceptedAt > AGENT_SETTLE_MS) {
        patch(id, { phase: "done" })
        toast.success(`${run.name} now runs ${data.panel.short_sha}`)
      } else if (Date.now() - run.acceptedAt > AGENT_RETURN_MS) {
        patch(id, {
          phase: "failed",
          note:
            node && !node.error
              ? `The node is still on ${node.short_sha || "its previous agent"}: it went back because the new agent did not stay up. On the node, "journalctl -u 'maburvm-agent-update-*'" has the reason.`
              : "The node has not answered for two minutes since the restart. Check on the node that maburvm-agent is running.",
        })
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, runs])

  if (isLoading) {
    return (
      <div className="flex items-center gap-3 rounded-md border bg-muted p-4">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        <span className="text-sm text-muted-foreground">Reading build information</span>
      </div>
    )
  }
  if (!data) return null

  const panel = data.panel
  const nodes = data.nodes ?? []
  const built = panel.build_time && !Number.isNaN(Date.parse(panel.build_time)) ? new Date(panel.build_time).toLocaleString() : ""

  return (
    <div className="space-y-4 rounded-md border bg-muted p-4">
      <div>
        <p className="text-sm font-medium">Panel build</p>
        <p className="mt-1 text-xs text-muted-foreground">
          {panel.stamped ? (
            <>
              <span className="font-mono text-foreground">{panel.version && panel.version !== panel.short_sha ? `${panel.version} (${panel.short_sha})` : panel.short_sha}</span>
              {built ? `, built ${built}` : ""}
            </>
          ) : (
            // An unstamped build is not "up to date": it is unidentified, and
            // saying so is the difference between a check and a guess.
            <span className="text-warning">Not stamped. This image was built without a revision, so agents cannot be compared with it.</span>
          )}
        </p>
      </div>

      {nodes.length > 0 && (
        <div>
          <p className="text-xs font-medium text-muted-foreground">Node agents</p>
          <ul className="mt-1 divide-y divide-hair">
            {nodes.map((n) => {
              const run = runs[n.node_id]
              const busy = run?.phase === "sending" || run?.phase === "restarting"
              return (
                <li key={n.node_id} className="py-2.5">
                  <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 text-sm">
                    <span className="min-w-0 truncate font-medium">{n.node_name}</span>
                    <span className="flex flex-wrap items-center gap-x-4 gap-y-2">
                      {n.error && !busy ? (
                        <span className="flex items-center gap-2" title={n.error}>
                          <span className="h-2 w-2 rounded-full bg-destructive" aria-hidden="true" />
                          Not answering
                        </span>
                      ) : (
                        <>
                          <span className="font-mono text-xs">{n.short_sha || "not stamped"}</span>
                          {panel.stamped && !busy && (
                            <span className="flex items-center gap-2 text-xs">
                              <span className={`h-2 w-2 rounded-full ${n.matches_panel ? "bg-success" : "bg-warning"}`} aria-hidden="true" />
                              {n.matches_panel ? "Same as the panel" : "Not the panel's build"}
                            </span>
                          )}
                          {panel.stamped && !n.matches_panel && !n.error && (
                            <Button size="sm" variant="outline" disabled={busy} onClick={() => runUpdate(n.node_id, n.node_name, n.short_sha)}>
                              {busy ? "Updating" : `Update to ${panel.short_sha}`}
                            </Button>
                          )}
                        </>
                      )}
                    </span>
                  </div>
                  {run && (
                    <OperationProgress
                      className="mt-3 p-4"
                      title={
                        run.phase === "done"
                          ? `${run.name} now runs ${panel.short_sha}`
                          : run.phase === "failed"
                            ? `${run.name} was not updated`
                            : `Updating ${run.name} from ${run.from || "its current agent"} to ${panel.short_sha}`
                      }
                      steps={agentSteps(run)}
                      startedAt={run.startedAt}
                    >
                      {!busy && (
                        <Button
                          size="sm"
                          variant="outline"
                          className="mt-3"
                          onClick={() => setRuns((r) => Object.fromEntries(Object.entries(r).filter(([id]) => id !== n.node_id)))}
                        >
                          Close
                        </Button>
                      )}
                    </OperationProgress>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
      )}
    </div>
  )
}

export default function SystemSettingsPage() {
  const [isSaving, setIsSaving] = useState<string | null>(null)
  const [showHmacSecret, setShowHmacSecret] = useState(false)
  const { data: sysSettings, isLoading: settingsLoading, error: settingsError, refetch: refetchSettings } = useSystemSettings()
  const saveSettings = useSaveSystemSettings()
  const testEmail = useTestEmail()
  const { data: me } = useCurrentUser()

  // Security form
  const securityForm = useForm<SecurityFormData>({
    resolver: zodResolver(securitySchema),
    defaultValues: defaultSecuritySettings,
  })

  // API form
  const quotaForm = useForm<QuotaFormData>({
    resolver: zodResolver(quotaSchema),
    defaultValues: defaultQuotaSettings,
  })

  const apiForm = useForm<APIFormData>({
    resolver: zodResolver(apiSchema),
    defaultValues: defaultApiSettings,
  })

  // Email form
  const emailForm = useForm<EmailFormData>({
    resolver: zodResolver(emailSchema),
    defaultValues: defaultEmailSettings,
  })

  // Populate forms from persisted settings once loaded.
  useEffect(() => {
    if (!sysSettings) return
    if (sysSettings.security) securityForm.reset({ ...defaultSecuritySettings, ...sysSettings.security })
    if (sysSettings.api) apiForm.reset({ ...defaultApiSettings, ...sysSettings.api })
    if (sysSettings.quotas) quotaForm.reset({ ...defaultQuotaSettings, ...sysSettings.quotas })
    if (sysSettings.email) emailForm.reset({ ...defaultEmailSettings, ...sysSettings.email })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sysSettings])

  const saved = useFlash(1500)
  // What sign-in actually does now, from the saved settings, not the form.
  const savedSecurity = sysSettings?.security ?? {}
  const captchaOn = !!savedSecurity.captchaProvider && !!savedSecurity.captchaSiteKey && !!savedSecurity.captchaSecretSet

  const saveSection = async (section: string, data: unknown, label: string, description: string) => {
    setIsSaving(section)
    try {
      await saveSettings.mutateAsync({ [section]: data })
      toast.success(`${label} saved!`, { description })
      saved.trigger(section)
    } catch (err) {
      toast.error(`Failed to save ${label.toLowerCase()}`, { description: (err as Error).message })
    } finally {
      setIsSaving(null)
    }
  }

  // Keys this form does not show stay as they are.
  const handleSaveSecurity = (data: SecurityFormData) =>
    saveSection(
      "security",
      { ...sysSettings?.security, ...data },
      "Security settings",
      data.lockoutAttempts === 0
        ? "Accounts are never locked after failed sign-ins"
        : `An account locks for ${data.lockoutMinutes} min after ${data.lockoutAttempts} failed sign-ins`,
    )

  const handleSaveApi = (data: APIFormData) =>
    saveSection("api", data, "API settings", data.enableApi ? "API keys are accepted" : "API keys are refused")

  const handleSaveEmail = (data: EmailFormData) =>
    saveSection("email", data, "Email settings", `SMTP configured for ${data.smtpHost}`)

  const handleTestEmail = async () => {
    const v = emailForm.getValues()
    // NOTE: do NOT touch isSaving here — that state drives the "Save Settings"
    // button's "Saving..." label, which made a test send look like a save.
    // testEmail.isPending drives the Test Email button's own spinner instead.
    try {
      const res = await testEmail.mutateAsync({
        smtpHost: v.smtpHost,
        smtpPort: v.smtpPort,
        smtpUser: v.smtpUser,
        smtpPassword: v.smtpPassword,
        smtpFrom: v.smtpFrom,
        smtpFromName: v.smtpFromName,
        enableTls: v.enableTls,
      })
      toast.success("Test email sent", { description: res.message })
    } catch (err) {
      toast.error("Test send failed", { description: (err as Error).message })
    }
  }

  const handleResetSettings = (type: string) => {
    const resets: Record<string, () => void> = {
      Security: () => securityForm.reset(defaultSecuritySettings),
      API: () => apiForm.reset(defaultApiSettings),
      Quotas: () => quotaForm.reset(defaultQuotaSettings),
      Email: () => emailForm.reset(defaultEmailSettings),
    }
    toast.info(`Reset ${type} settings to defaults?`, {
      description: "Resets the fields in this section. Click Save to persist.",
      action: {
        label: "Reset",
        onClick: () => {
          resets[type]?.()
          toast.success(`${type} fields reset to defaults`)
        },
      },
    })
  }

  if (settingsLoading) {
    return (
      <div className="max-w-4xl mx-auto flex items-center gap-3 p-4" role="status">
        <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
        <span className="text-sm text-muted-foreground">Loading system settings...</span>
      </div>
    )
  }

  if (settingsError) {
    return (
      <div className="max-w-4xl mx-auto">
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-12 text-center">
          <h2 className="text-xl font-semibold mb-2">Failed to load system settings</h2>
          <p className="text-muted-foreground mb-6">{(settingsError as Error).message}</p>
          <Button onClick={() => refetchSettings()}>Retry</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-foreground">
              System Settings
            </h1>
            <p className="text-muted-foreground text-sm mt-1">
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
        <TabsList>
          <TabsTrigger value="general" className="text-xs">
            <Server className="w-4 h-4 mr-2" />
            Version
          </TabsTrigger>
          <TabsTrigger value="panel" className="text-xs">
            <Link className="w-4 h-4 mr-2" />
            Panel
          </TabsTrigger>
          <TabsTrigger value="security" className="text-xs">
            <Shield className="w-4 h-4 mr-2" />
            Security
          </TabsTrigger>
          <TabsTrigger value="backup" className="text-xs">
            <Database className="w-4 h-4 mr-2" />
            Backup
          </TabsTrigger>
          <TabsTrigger value="api" className="text-xs">
            <Key className="w-4 h-4 mr-2" />
            API
          </TabsTrigger>
          <TabsTrigger value="quotas" className="text-xs">
            <Users className="w-4 h-4 mr-2" />
            Quotas
          </TabsTrigger>
          <TabsTrigger value="email" className="text-xs">
            <Mail className="w-4 h-4 mr-2" />
            Email
          </TabsTrigger>
          <TabsTrigger value="rescue" className="text-xs">
            <LifeBuoy className="w-4 h-4 mr-2" />
            Rescue
          </TabsTrigger>
        </TabsList>

        {/* Which build is actually running, on the panel and on each node.
            The panel's name and logo are set under Settings, Branding. */}
        <TabsContent value="general">
          <Card>
            <CardHeader className="border-b">
              <CardTitle className="flex items-center gap-2">
                <Server className="w-5 h-5" />
                Version
              </CardTitle>
              <CardDescription>
                The build the panel and each node&apos;s agent are running. The panel&apos;s name and
                logo are set under Branding.
              </CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <BuildPanel />
            </CardContent>
          </Card>
        </TabsContent>

        {/* Address, billing webhook, SSO and metrics: env values made editable. */}
        <TabsContent value="panel">
          <PanelConfigSettings />
        </TabsContent>

        {/* Password policy and sign-in policy: one settings row, one form. */}
        <TabsContent value="security">
          <Card>
            <CardHeader className="border-b">
              <CardTitle className="flex items-center gap-2">
                <Shield className="w-5 h-5" />
                Passwords and sign-in
              </CardTitle>
              <CardDescription>
                What a new password must contain, and how sign-in is protected. Password rules apply to
                passwords set from now on; passwords already set keep working.
              </CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={securityForm.handleSubmit(handleSaveSecurity)} className="space-y-6">
                <div className="max-w-xs">
                  <label htmlFor="minPasswordLength" className="block text-sm font-medium text-muted-foreground mb-2">
                    Minimum length
                  </label>
                  <Input
                    id="minPasswordLength"
                    type="number"
                    {...securityForm.register("minPasswordLength", { valueAsNumber: true })}
                    min={8}
                    max={128}
                  />
                  <p className="text-xs text-muted-foreground mt-1.5">Between 8 and 128 characters.</p>
                  {securityForm.formState.errors.minPasswordLength && (
                    <p className="text-destructive text-sm mt-1">
                      {securityForm.formState.errors.minPasswordLength.message}
                    </p>
                  )}
                </div>

                <div className="p-4 rounded-md border bg-muted">
                  <p className="text-sm font-medium mb-1">A password must also contain</p>
                  <p className="text-xs text-muted-foreground mb-4">A lowercase letter is always required.</p>
                  <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input
                        type="checkbox"
                        {...securityForm.register("requireUppercase")}
                        className="w-5 h-5 accent-primary"
                      />
                      <span className="text-sm font-medium">An uppercase letter</span>
                    </label>
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input
                        type="checkbox"
                        {...securityForm.register("requireNumbers")}
                        className="w-5 h-5 accent-primary"
                      />
                      <span className="text-sm font-medium">A digit</span>
                    </label>
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input
                        type="checkbox"
                        {...securityForm.register("requireSpecial")}
                        className="w-5 h-5 accent-primary"
                      />
                      <span className="text-sm font-medium">A symbol</span>
                    </label>
                  </div>
                </div>

                <div className="space-y-5 pt-6 border-t">
                  <h3 className="text-sm font-semibold">Sign-in</h3>
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                      <label htmlFor="lockoutAttempts" className="block text-sm font-medium text-muted-foreground mb-2">
                        Failed sign-ins before an account locks
                      </label>
                      <Input id="lockoutAttempts" type="number" min={0} max={100} {...securityForm.register("lockoutAttempts", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground mt-1.5">
                        Wrong passwords or two-factor codes in a row. 0 never locks. An administrator can unlock an
                        account from its user page, and resetting the password unlocks it.
                      </p>
                      {securityForm.formState.errors.lockoutAttempts && (
                        <p className="text-destructive text-sm mt-1">{securityForm.formState.errors.lockoutAttempts.message}</p>
                      )}
                    </div>
                    <div>
                      <label htmlFor="lockoutMinutes" className="block text-sm font-medium text-muted-foreground mb-2">
                        Locked for (minutes)
                      </label>
                      <Input
                        id="lockoutMinutes"
                        type="number"
                        min={1}
                        max={1440}
                        {...securityForm.register("lockoutMinutes", { valueAsNumber: true })}
                      />
                      <p className="text-xs text-muted-foreground mt-1.5">The account unlocks by itself after this.</p>
                      {securityForm.formState.errors.lockoutMinutes && (
                        <p className="text-destructive text-sm mt-1">{securityForm.formState.errors.lockoutMinutes.message}</p>
                      )}
                    </div>
                    <div>
                      <label htmlFor="sessionHours" className="block text-sm font-medium text-muted-foreground mb-2">
                        Session length (hours)
                      </label>
                      <Input id="sessionHours" type="number" min={1} max={720} {...securityForm.register("sessionHours", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground mt-1.5">
                        How long a sign-in lasts before the person signs in again. Applies to sign-ins from now on.
                      </p>
                      {securityForm.formState.errors.sessionHours && (
                        <p className="text-destructive text-sm mt-1">{securityForm.formState.errors.sessionHours.message}</p>
                      )}
                    </div>
                  </div>
                  <label className="flex items-start gap-3 cursor-pointer">
                    <input type="checkbox" {...securityForm.register("requireStaff2fa")} className="mt-0.5 w-5 h-5 accent-primary" />
                    <span>
                      <span className="text-sm font-medium">Require two-factor authentication for administrators</span>
                      <span className="block text-xs text-muted-foreground mt-0.5">
                        An administrator without it reaches only their profile, where they turn it on, until they do.
                        Customers and API keys are not affected.
                      </span>
                      {securityForm.watch("requireStaff2fa") && me && !me.two_factor_enabled && (
                        <span className="block text-xs font-medium text-foreground mt-1">
                          You have not turned it on yourself: once saved, you are asked to before anything else.
                        </span>
                      )}
                    </span>
                  </label>
                </div>

                <div className="space-y-4 pt-6 border-t">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <h3 className="text-sm font-semibold">Sign-in check (captcha)</h3>
                    <span className="flex items-center gap-2 text-xs">
                      <StatusBadge status={captchaOn ? "on" : "off"} />
                      <span className="text-muted-foreground">
                        {captchaOn
                          ? (Number(savedSecurity.captchaAfterFailures ?? 3)
                              ? `Asked after ${Number(savedSecurity.captchaAfterFailures ?? 3)} failed attempts`
                              : "Asked on every sign-in and reset")
                          : "Needs a provider, its site key and its secret key"}
                      </span>
                    </span>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    After that many failed sign-ins or password reset requests from one address, or for one email, in 15
                    minutes, the sign-in and reset pages ask for the provider&apos;s check before anything else. Keys come from
                    the provider&apos;s dashboard. If the provider cannot be reached, those attempts wait until it can.
                  </p>
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                      <label htmlFor="captchaProvider" className="block text-sm font-medium text-muted-foreground mb-2">Provider</label>
                      <select
                        id="captchaProvider"
                        {...securityForm.register("captchaProvider")}
                        className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                      >
                        <option value="">Off</option>
                        <option value="turnstile">Cloudflare Turnstile</option>
                        <option value="hcaptcha">hCaptcha</option>
                      </select>
                    </div>
                    <div>
                      <label htmlFor="captchaAfterFailures" className="block text-sm font-medium text-muted-foreground mb-2">
                        Ask after this many failed attempts
                      </label>
                      <Input id="captchaAfterFailures" type="number" min={0} max={100}
                        {...securityForm.register("captchaAfterFailures", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground mt-1.5">0 asks on every sign-in, so nobody can sign in with a password while the provider is down.</p>
                      {securityForm.formState.errors.captchaAfterFailures && (
                        <p className="text-destructive text-sm mt-1">{securityForm.formState.errors.captchaAfterFailures.message}</p>
                      )}
                    </div>
                    <div>
                      <label htmlFor="captchaSiteKey" className="block text-sm font-medium text-muted-foreground mb-2">Site key</label>
                      <Input id="captchaSiteKey" autoComplete="off" {...securityForm.register("captchaSiteKey")} />
                    </div>
                    <div>
                      <label htmlFor="captchaSecret" className="block text-sm font-medium text-muted-foreground mb-2">Secret key</label>
                      <Input id="captchaSecret" type="password" autoComplete="new-password"
                        placeholder={savedSecurity.captchaSecretSet ? "Saved. Leave blank to keep it." : ""}
                        {...securityForm.register("captchaSecret")} />
                      <p className="text-xs text-muted-foreground mt-1.5">Stored encrypted and never shown again.</p>
                    </div>
                  </div>
                </div>

                <div className="flex items-center justify-between pt-4 border-t">
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
                        {saved.active("security") ? <Check className="w-4 h-4 mr-2" aria-hidden="true" /> : <Save className="w-4 h-4 mr-2" aria-hidden="true" />}
                        {saved.active("security") ? "Saved" : "Save Settings"}
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>

        {/* Where backups are kept, and when they run. Schedules and retention are set per VM. */}
        <TabsContent value="backup">
          <BackupDestinationSettings />
          <BackupPolicySettings />
        </TabsContent>

        {/* API Settings */}
        <TabsContent value="quotas">
          <Card>
            <CardHeader className="border-b">
              <CardTitle className="flex items-center gap-2">
                <Users className="w-5 h-5" />
                Per-account limits
              </CardTitle>
              <CardDescription>
                How much a single customer may take of the resources that are finite on a node.
                Changes apply immediately — no restart.
              </CardDescription>
            </CardHeader>
            <CardContent className="pt-6">
              <form
                onSubmit={quotaForm.handleSubmit((data) =>
                  saveSection("quotas", data, "Quota settings", "New limits apply to the next request"),
                )}
                className="space-y-6"
              >
                <div className="grid gap-6 md:grid-cols-2">
                  <div className="space-y-2">
                    <label htmlFor="vpcMaxPerUser" className="block text-sm font-medium text-muted-foreground mb-2">
                      Private networks per account
                    </label>
                    <Input
                      id="vpcMaxPerUser"
                      type="number"
                      {...quotaForm.register("vpcMaxPerUser", { valueAsNumber: true })}
                    />
                    <p className="text-xs text-muted-foreground">
                      Each one costs a network namespace, a bridge and three virtual links on the
                      node, so this is a real resource limit rather than a policy one.
                    </p>
                    {quotaForm.formState.errors.vpcMaxPerUser && (
                      <p className="text-xs text-destructive">
                        {quotaForm.formState.errors.vpcMaxPerUser.message}
                      </p>
                    )}
                  </div>
                  <div className="space-y-2">
                    <label htmlFor="floatingIpMaxPerUser" className="block text-sm font-medium text-muted-foreground mb-2">
                      Floating IPs per account
                    </label>
                    <Input
                      id="floatingIpMaxPerUser"
                      type="number"
                      {...quotaForm.register("floatingIpMaxPerUser", { valueAsNumber: true })}
                    />
                    <p className="text-xs text-muted-foreground">
                      Caps self-service ordering. Public addresses are finite, so without a cap one
                      account can drain a node&apos;s block.
                    </p>
                    {quotaForm.formState.errors.floatingIpMaxPerUser && (
                      <p className="text-xs text-destructive">
                        {quotaForm.formState.errors.floatingIpMaxPerUser.message}
                      </p>
                    )}
                  </div>
                  <div className="space-y-2">
                    <label htmlFor="portForwardMaxPerVm" className="block text-sm font-medium text-muted-foreground mb-2">
                      Port forwards per VM
                    </label>
                    <Input
                      id="portForwardMaxPerVm"
                      type="number"
                      {...quotaForm.register("portForwardMaxPerVm", { valueAsNumber: true })}
                    />
                    <p className="text-xs text-muted-foreground">
                      Each forward takes a port on the node&apos;s public address away from every
                      other VM on that node. VMs already above a lowered limit keep what they have.
                    </p>
                    {quotaForm.formState.errors.portForwardMaxPerVm && (
                      <p className="text-xs text-destructive">
                        {quotaForm.formState.errors.portForwardMaxPerVm.message}
                      </p>
                    )}
                  </div>
                  <div className="space-y-2">
                    <label htmlFor="firewallRuleMaxPerVm" className="block text-sm font-medium text-muted-foreground mb-2">
                      Firewall rules per VM
                    </label>
                    <Input
                      id="firewallRuleMaxPerVm"
                      type="number"
                      {...quotaForm.register("firewallRuleMaxPerVm", { valueAsNumber: true })}
                    />
                    <p className="text-xs text-muted-foreground">
                      Every VM&apos;s rules share one chain on its node, so each rule slows new
                      connections a little for every VM there. VMs already above a lowered limit keep
                      what they have.
                    </p>
                    {quotaForm.formState.errors.firewallRuleMaxPerVm && (
                      <p className="text-xs text-destructive">
                        {quotaForm.formState.errors.firewallRuleMaxPerVm.message}
                      </p>
                    )}
                  </div>
                </div>

                <div className="pt-2 border-t">
                  <h3 className="text-sm font-medium mb-1">What customers may do on their own</h3>
                  <p className="text-xs text-muted-foreground mb-4">
                    The panel does not charge for anything. A customer who orders, clones or adds a disk
                    here gets it without your billing system knowing. Turn off what your billing system
                    should be selling instead; orders it places are not affected.
                  </p>
                  <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input type="checkbox" {...quotaForm.register("clientOrderingEnabled")} className="w-5 h-5 accent-primary" />
                      <span className="text-sm font-medium">Order servers</span>
                    </label>
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input type="checkbox" {...quotaForm.register("clientCloneEnabled")} className="w-5 h-5 accent-primary" />
                      <span className="text-sm font-medium">Clone their servers</span>
                    </label>
                    <label className="flex items-center gap-3 cursor-pointer">
                      <input type="checkbox" {...quotaForm.register("clientExtraDisksEnabled")} className="w-5 h-5 accent-primary" />
                      <span className="text-sm font-medium">Add extra disks</span>
                    </label>
                  </div>
                  <label className="mt-4 flex items-start gap-3 cursor-pointer">
                    <input type="checkbox" {...quotaForm.register("clientDeleteBilledEnabled")} className="mt-0.5 w-5 h-5 accent-primary" />
                    <span>
                      <span className="text-sm font-medium">Delete servers that billing manages</span>
                      <span className="block text-xs text-muted-foreground">
                        Off: a customer cannot delete a server that has a billing service reference, because
                        the service would go on being charged. They cancel it in your billing system, which
                        removes it. Servers with no reference can always be deleted by their owner.
                      </span>
                    </span>
                  </label>
                </div>

                <div className="pt-2 border-t">
                  <h3 className="text-sm font-medium mb-1">Starting quota for new customer accounts</h3>
                  <p className="text-xs text-muted-foreground mb-4">
                    Given to each customer account when it is created, and the ceiling on what that
                    customer can add on their own. Change one account&apos;s quota on its user page.
                    Existing accounts keep what they have. Orders from your billing system are not
                    held back by it. 0 means no limit.
                  </p>
                  <div className="grid gap-6 md:grid-cols-2">
                    <div className="space-y-2">
                      <label htmlFor="newClientMaxVms" className="block text-sm font-medium text-muted-foreground mb-2">
                        Servers
                      </label>
                      <Input id="newClientMaxVms" type="number" {...quotaForm.register("newClientMaxVms", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground">How many servers the account may hold at once.</p>
                      {quotaForm.formState.errors.newClientMaxVms && (
                        <p className="text-xs text-destructive">{quotaForm.formState.errors.newClientMaxVms.message}</p>
                      )}
                    </div>
                    <div className="space-y-2">
                      <label htmlFor="newClientMaxVcpu" className="block text-sm font-medium text-muted-foreground mb-2">
                        vCPUs
                      </label>
                      <Input id="newClientMaxVcpu" type="number" {...quotaForm.register("newClientMaxVcpu", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground">Across all of the account&apos;s servers.</p>
                      {quotaForm.formState.errors.newClientMaxVcpu && (
                        <p className="text-xs text-destructive">{quotaForm.formState.errors.newClientMaxVcpu.message}</p>
                      )}
                    </div>
                    <div className="space-y-2">
                      <label htmlFor="newClientMaxRamMb" className="block text-sm font-medium text-muted-foreground mb-2">
                        Memory (MB)
                      </label>
                      <Input id="newClientMaxRamMb" type="number" {...quotaForm.register("newClientMaxRamMb", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground">Across all of the account&apos;s servers.</p>
                      {quotaForm.formState.errors.newClientMaxRamMb && (
                        <p className="text-xs text-destructive">{quotaForm.formState.errors.newClientMaxRamMb.message}</p>
                      )}
                    </div>
                    <div className="space-y-2">
                      <label htmlFor="newClientMaxDiskGb" className="block text-sm font-medium text-muted-foreground mb-2">
                        Disk (GB)
                      </label>
                      <Input id="newClientMaxDiskGb" type="number" {...quotaForm.register("newClientMaxDiskGb", { valueAsNumber: true })} />
                      <p className="text-xs text-muted-foreground">System disks and extra disks together.</p>
                      {quotaForm.formState.errors.newClientMaxDiskGb && (
                        <p className="text-xs text-destructive">{quotaForm.formState.errors.newClientMaxDiskGb.message}</p>
                      )}
                    </div>
                  </div>
                </div>

                <div className="pt-2 border-t">
                  <h3 className="text-sm font-medium mb-1">Node overcommit</h3>
                  <p className="text-xs text-muted-foreground mb-4">
                    How far a node may be allocated beyond what it physically has. These are
                    per-node ceilings, not per-account ones — they decide where a VM can be placed,
                    not what a customer is entitled to.
                  </p>
                  <div className="grid gap-6 md:grid-cols-2">
                    <div className="space-y-2">
                      <label htmlFor="ramOvercommitRatio" className="block text-sm font-medium text-muted-foreground mb-2">
                        RAM overcommit ratio
                      </label>
                      <Input
                        id="ramOvercommitRatio"
                        type="number"
                        step="0.1"
                        {...quotaForm.register("ramOvercommitRatio", { valueAsNumber: true })}
                      />
                      <p className="text-xs text-muted-foreground">
                        0 disables the check entirely, which is the default and what the panel did
                        before this existed. At 1.5, a 128 GB node accepts 192 GB of allocated RAM.
                        Be deliberate raising this: a host that runs out of RAM does not degrade, the
                        kernel kills a running VM.
                      </p>
                      {quotaForm.formState.errors.ramOvercommitRatio && (
                        <p className="text-xs text-destructive">
                          {quotaForm.formState.errors.ramOvercommitRatio.message}
                        </p>
                      )}
                    </div>
                    <div className="space-y-2">
                      <label htmlFor="diskAllocationRatio" className="block text-sm font-medium text-muted-foreground mb-2">
                        Disk overcommit ratio
                      </label>
                      <Input
                        id="diskAllocationRatio"
                        type="number"
                        step="0.1"
                        {...quotaForm.register("diskAllocationRatio", { valueAsNumber: true })}
                      />
                      <p className="text-xs text-muted-foreground">
                        How many times a storage pool&apos;s capacity may be promised to VM disks. 0
                        means no limit, the default. At 2, a 1 TB pool accepts 2 TB of disks. Disks
                        are thin, so a pool fills as guests write, and a full pool pauses every VM
                        on it. Set this above what the Storage page shows as allocated on each
                        pool, or new orders there are refused. A new disk must always fit in real
                        free space with 20 GB to spare.
                      </p>
                      {quotaForm.formState.errors.diskAllocationRatio && (
                        <p className="text-xs text-destructive">
                          {quotaForm.formState.errors.diskAllocationRatio.message}
                        </p>
                      )}
                    </div>
                    <div className="space-y-2">
                      <label htmlFor="vcpuAllocationRatio" className="block text-sm font-medium text-muted-foreground mb-2">
                        vCPU overcommit ratio
                      </label>
                      <Input
                        id="vcpuAllocationRatio"
                        type="number"
                        step="0.5"
                        {...quotaForm.register("vcpuAllocationRatio", { valueAsNumber: true })}
                      />
                      <p className="text-xs text-muted-foreground">
                        How many vCPUs a node may carry per CPU thread it has. 0 means no limit, the
                        default. At 4, a node with 32 threads accepts 128 vCPUs. A node can have its
                        own ratio on its page, which wins over this one. Set this above what each
                        node already carries, or new orders there are refused.
                      </p>
                      {quotaForm.formState.errors.vcpuAllocationRatio && (
                        <p className="text-xs text-destructive">
                          {quotaForm.formState.errors.vcpuAllocationRatio.message}
                        </p>
                      )}
                    </div>
                  </div>
                </div>

                <div className="flex justify-end">
                  <Button type="submit" disabled={isSaving === "quotas"}>
                    {isSaving === "quotas" ? "Saving..." : saved.active("quotas") ? (
                      <>
                        <Check className="w-4 h-4 mr-2" aria-hidden="true" />
                        Saved
                      </>
                    ) : "Save limits"}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
          <TransferQuotaSettings />
        </TabsContent>

        <TabsContent value="api">
          <Card>
            <CardHeader className="border-b">
              <CardTitle className="flex items-center gap-2">
                <Key className="w-5 h-5" />
                API Settings
              </CardTitle>
              <CardDescription>API keys, and the events the panel sends to your billing system</CardDescription>
            </CardHeader>
            <CardContent className="p-6">
              <form onSubmit={apiForm.handleSubmit(handleSaveApi)} className="space-y-6">
                {/* Enable API */}
                <div className="p-4 rounded-md border bg-muted">
                  <label className="flex items-center gap-3 cursor-pointer">
                    <input
                      type="checkbox"
                      {...apiForm.register("enableApi")}
                      className="w-5 h-5 accent-primary"
                    />
                    <div>
                      <span className="font-medium">Accept API keys</span>
                      <p className="text-xs text-muted-foreground">
                        When this is off, every request made with an API key is refused, including
                        the MCP endpoint. Signing in through the browser is not affected. Keys are
                        kept, and work again when this is turned back on.
                      </p>
                    </div>
                  </label>
                </div>

                <div className="max-w-sm">
                  <label htmlFor="apiRequestsPerMinute" className="block text-sm font-medium text-muted-foreground mb-2">
                    Requests per minute, per account or key
                  </label>
                  <Input
                    id="apiRequestsPerMinute"
                    type="number"
                    min={0}
                    {...apiForm.register("apiRequestsPerMinute", { valueAsNumber: true })}
                  />
                  <p className="text-[11px] text-muted-foreground mt-1 font-medium">
                    Counted for each signed-in person and each API key, the panel&apos;s own pages
                    included. Past it, requests are refused for a few seconds with a note saying when
                    to try again. Your billing system&apos;s webhook is not counted. 0 turns the
                    limit off.
                  </p>
                  {apiForm.formState.errors.apiRequestsPerMinute && (
                    <p className="text-destructive text-sm mt-1">{apiForm.formState.errors.apiRequestsPerMinute.message}</p>
                  )}
                </div>

                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                  {/* Webhook URL */}
                  <div>
                    <label htmlFor="webhookUrl" className="block text-sm font-medium text-muted-foreground mb-2">
                      Webhook URL
                    </label>
                    <div className="relative">
                      <Link className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                      <Input
                        id="webhookUrl"
                        {...apiForm.register("webhookUrl")}
                        placeholder="https://your-billing.com/webhooks/maburvm"
                        className="pl-10"
                      />
                    </div>
                    <p className="text-[11px] text-muted-foreground mt-1 font-medium">Events for your billing system (a server created, suspended or deleted, a failed installation, floating IP counts, transfer overage) are sent here, signed with the HMAC secret below, or with the billing webhook secret when none is saved. Takes effect at once. The list under this form shows each event and whether it arrived.</p>
                    {apiForm.formState.errors.webhookUrl && (
                      <p className="text-destructive text-sm mt-1">
                        {apiForm.formState.errors.webhookUrl.message}
                      </p>
                    )}
                  </div>

                  {/* AbuseIPDB */}
                  <div>
                    <label htmlFor="abuseipdbToken" className="block text-sm font-medium text-muted-foreground mb-2">
                      AbuseIPDB API key
                    </label>
                    <Input
                      id="abuseipdbToken"
                      type="password"
                      {...apiForm.register("abuseipdbToken")}
                      placeholder="Optional. Leave blank to keep the saved key"
                    />
                    {/* Optional on purpose: the blocklist checks need no
                        credentials and carry IP Reputation on their own. The key
                        only adds a confidence score, and its free tier is 1,000
                        checks a day — enough for a few hundred addresses. */}
                    <p className="text-[11px] text-muted-foreground mt-1 font-medium">
                      Optional. Adds a confidence score and report count to IP Reputation; the
                      blocklist checks work without it. Free tier allows 1,000 checks a day.
                    </p>
                  </div>

                </div>

                {/* HMAC Secret */}
                <div>
                  <label htmlFor="hmacSecret" className="block text-sm font-medium text-muted-foreground mb-2">
                    HMAC Secret
                  </label>
                  <div className="relative">
                    <Input
                      id="hmacSecret"
                      type={showHmacSecret ? "text" : "password"}
                      {...apiForm.register("hmacSecret")}
                      placeholder="Leave blank to keep the saved secret"
                      className="pr-10"
                    />
                    <button
                      type="button"
                      onClick={() => setShowHmacSecret(!showHmacSecret)}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    >
                      {showHmacSecret ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                  <p className="text-xs text-muted-foreground mt-1">
                    Signs the events sent to the webhook URL, so the receiver can check they came
                    from this panel. At least 32 characters.
                  </p>
                </div>

                <div className="flex items-center justify-between pt-4 border-t">
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
                        {saved.active("api") ? <Check className="w-4 h-4 mr-2" aria-hidden="true" /> : <Save className="w-4 h-4 mr-2" aria-hidden="true" />}
                        {saved.active("api") ? "Saved" : "Save Settings"}
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
          <BillingDeliveries />
          <DNSProviderSettings />
        </TabsContent>

        {/* Email Settings */}
        <TabsContent value="email">
          <Card>
            <CardHeader className="border-b">
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
                    <label htmlFor="smtpHost" className="block text-sm font-medium text-muted-foreground mb-2">
                      SMTP Host
                    </label>
                    <div className="relative">
                      <Server className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                      <Input
                        id="smtpHost"
                        {...emailForm.register("smtpHost")}
                        placeholder="smtp.example.com"
                        className="pl-10"
                      />
                    </div>
                    {emailForm.formState.errors.smtpHost && (
                      <p className="text-destructive text-sm mt-1">
                        {emailForm.formState.errors.smtpHost.message}
                      </p>
                    )}
                  </div>

                  {/* SMTP Port */}
                  <div>
                    <label htmlFor="smtpPort" className="block text-sm font-medium text-muted-foreground mb-2">
                      SMTP Port
                    </label>
                    <Input
                      id="smtpPort"
                      type="number"
                      {...emailForm.register("smtpPort", { valueAsNumber: true })}
                      min={1}
                      max={65535}
                    />
                  </div>

                  {/* SMTP User */}
                  <div>
                    <label htmlFor="smtpUser" className="block text-sm font-medium text-muted-foreground mb-2">
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
                    <label htmlFor="smtpPassword" className="block text-sm font-medium text-muted-foreground mb-2">
                      SMTP Password
                    </label>
                    <div className="relative">
                      <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                      <Input
                        id="smtpPassword"
                        type="password"
                        {...emailForm.register("smtpPassword")}
                        placeholder="Leave blank to keep the saved password"
                        className="pl-10"
                      />
                    </div>
                  </div>

                  {/* From Email */}
                  <div>
                    <label htmlFor="smtpFrom" className="block text-sm font-medium text-muted-foreground mb-2">
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
                    <label htmlFor="smtpFromName" className="block text-sm font-medium text-muted-foreground mb-2">
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
                <div className="p-4 rounded-md border bg-muted">
                  <label className="flex items-center gap-3 cursor-pointer">
                    <input
                      type="checkbox"
                      {...emailForm.register("enableTls")}
                      className="w-5 h-5 accent-primary"
                    />
                    <div>
                      <span className="font-medium">Require TLS</span>
                      <p className="text-xs text-muted-foreground">
                        Send only over an encrypted connection: STARTTLS, or TLS from the start on
                        port 465. If the mail server offers neither, nothing is sent and the test
                        email says so. When this is off, mail is still encrypted when the server
                        offers it, and sent in the clear when it does not.
                      </p>
                    </div>
                  </label>
                </div>

                <div className="flex items-center justify-between pt-4 border-t">
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
                      disabled={testEmail.isPending}
                    >
                      {testEmail.isPending ? (
                        <>
                          <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                          Sending...
                        </>
                      ) : (
                        <>
                          <Mail className="w-4 h-4 mr-2" />
                          Test Email
                        </>
                      )}
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
                        {saved.active("email") ? <Check className="w-4 h-4 mr-2" aria-hidden="true" /> : <Save className="w-4 h-4 mr-2" aria-hidden="true" />}
                        {saved.active("email") ? "Saved" : "Save Settings"}
                      </>
                    )}
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
          <EmailLog />
        </TabsContent>

        <TabsContent value="rescue">
          <RescueImageSettings />
        </TabsContent>
      </Tabs>
    </div>
  )
}