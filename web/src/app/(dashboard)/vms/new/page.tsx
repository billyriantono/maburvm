"use client"

import { useState, useMemo, useEffect, Suspense } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useForm, Controller } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { z } from "zod"
import { 
  ChevronRight, 
  ChevronLeft, 
  Check, 
  Server, 
  Cpu, 
  HardDrive, 
  Network, 
  Loader2,
  CheckCircle2,
  AlertCircle,
  User,
  Layers
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useUsers } from "@/lib/hooks/use-users"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useNodes } from "@/lib/hooks/use-nodes"
import { useIPPools } from "@/lib/hooks/use-ipam"
import { useNetworks } from "@/lib/hooks/use-networks"
import { useSSHKeys } from "@/lib/hooks/use-ssh-keys"
import { usePlans } from "@/lib/hooks/use-plans"
import { useRecipes } from "@/lib/hooks/use-recipes"
import { useCreateVM, useVM } from "@/lib/hooks/use-vms"
import { useImages } from "@/lib/hooks/use-images"
import { OSIcon } from "@/components/os-icon"

// Validation schemas for each step
const step1Schema = z.object({
  hostname: z.string()
    .min(1, "Hostname is required")
    .regex(/^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/, 
      "Invalid hostname format (e.g., vm.example.com or my-vm)"),
  userId: z.string().optional(),
})

const step2Schema = z.object({
  planId: z.string().optional(),
  cpuCores: z.number().min(1, "Minimum 1 CPU core").max(64, "Maximum 64 CPU cores"),
  ramGB: z.number().min(1, "Minimum 1 GB RAM").max(512, "Maximum 512 GB RAM"),
  diskGB: z.number().min(10, "Minimum 10 GB disk").max(2000, "Maximum 2 TB disk"),
  nodeId: z.string().optional(),
  cpuModel: z.string().optional(),
  userData: z.string().optional(),
})

// templateId is optional at the schema level: when creating from an image
// (?source_image_id=) no template is needed. handleNext enforces the selection
// manually for the fresh-install path.
const step3Schema = z.object({
  templateId: z.string().optional(),
})

const step4Schema = z.object({
  ipPoolId: z.string().optional(),
  ipAddress: z.string()
    .optional()
    .refine((val) => !val || /^(\d{1,3}\.){3}\d{1,3}$/.test(val), {
      message: "Invalid IP address format",
    })
    .refine((val) => {
      if (!val) return true
      const parts = val.split(".").map(Number)
      return parts.every((p) => p >= 0 && p <= 255)
    }, { message: "IP address octets must be 0-255" }),
  bandwidthMbps: z.number().min(1, "Minimum 1 Mbps").max(10000, "Maximum 10 Gbps"),
  vlanId: z.string().optional(),
  managedNetworkId: z.string().optional(),
})

const fullSchema = step1Schema.merge(step2Schema).merge(step3Schema).merge(step4Schema)

type FormData = z.infer<typeof fullSchema>

// Steps configuration
// useSearchParams requires a Suspense boundary in Next 15 client pages.
export default function NewVMPage() {
  return (
    <Suspense fallback={null}>
      <NewVMForm />
    </Suspense>
  )
}

const steps = [
  { id: 1, title: "Basic", description: "Hostname & Node", icon: Server },
  { id: 2, title: "Resources", description: "CPU, RAM, Disk", icon: Cpu },
  { id: 3, title: "OS Template", description: "Select OS", icon: HardDrive },
  { id: 4, title: "Network", description: "IP & Config", icon: Network },
  { id: 5, title: "Review", description: "Confirm & Create", icon: CheckCircle2 },
]

function NewVMForm() {
  const router = useRouter()
  const searchParams = useSearchParams()
  // Create-from-image: skips the OS template step; the server derives the OS
  // from the image and clones its disk.
  const sourceImageId = searchParams.get("source_image_id") ?? ""
  const { data: imagesData } = useImages()
  const sourceImage = sourceImageId ? imagesData?.find((img) => img.id === sourceImageId) : undefined
  const [currentStep, setCurrentStep] = useState(1)
  // SSH keys are opt-out: every saved key is injected unless unchecked. These are
  // the current admin's keys (the create endpoint resolves keys against the caller).
  const { data: sshKeys } = useSSHKeys()
  const [excludedKeys, setExcludedKeys] = useState<Set<string>>(new Set())
  const selectedKeyIds = (sshKeys ?? []).filter((k) => !excludedKeys.has(k.id)).map((k) => k.id)
  // Raw keys pasted at create time (one per line), injected in addition to saved keys.
  const [pastedKeys, setPastedKeys] = useState("")
  const pastedKeyList = pastedKeys.split("\n").map((s) => s.trim()).filter(Boolean)
  const [submitError, setSubmitError] = useState<string | null>(null)
  
  // API hooks
  const { data: usersData, isLoading: usersLoading } = useUsers({ pageSize: 100 })
  const { data: templatesData, isLoading: templatesLoading } = useTemplates()
  const { data: nodesData, isLoading: nodesLoading } = useNodes()
  const { data: poolsData, isLoading: poolsLoading } = useIPPools()
  const { data: managedNets } = useNetworks()
  const { data: plansData } = usePlans(true)
  const createVM = useCreateVM()

  const users = useMemo(() => usersData?.data || [], [usersData?.data])
  const templates = useMemo(() => templatesData || [], [templatesData])
  // Only templates with a real base image can provision a new VM. Imported
  // templates carry the "/imported" placeholder (their VMs already have disks),
  // so creating from them fails on the agent — hide them here.
  const installableTemplates = useMemo(
    () =>
      templates.filter((t) => {
        const path = (t.image_path || "").trim()
        return t.is_active && path !== "" && path !== "/imported"
      }),
    [templates]
  )
  const nodes = useMemo(() => nodesData || [], [nodesData])
  const activeNodes = useMemo(() => nodes.filter(n => n.status === "active"), [nodes])
  const pools = useMemo(() => poolsData || [], [poolsData])
  const plans = useMemo(() => plansData || [], [plansData])
  const { data: recipesData } = useRecipes()
  const recipes = useMemo(() => recipesData || [], [recipesData])
  const [selectedRecipeId, setSelectedRecipeId] = useState("")
  // After create we switch the page to an inline provisioning view: it shows the
  // one-time root password (if the server generated one — never retrievable
  // later) and polls the new VM's status until it's running.
  const [provisioning, setProvisioning] = useState<{ id: string; password?: string } | null>(null)
  const [pwCopied, setPwCopied] = useState(false)
  const provisioningVM = useVM(provisioning?.id ?? "", {
    enabled: !!provisioning,
    // Poll every 3s while provisioning; stop once the VM is running/stopped.
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === "running" || s === "stopped" || s === "error" ? false : 3000
    },
  } as Parameters<typeof useVM>[1])

  const {
    register,
    handleSubmit,
    control,
    watch,
    trigger,
    setValue,
    setError,
    formState: { errors },
  } = useForm<FormData>({
    resolver: zodResolver(fullSchema),
    defaultValues: {
      hostname: "",
      userId: "",
      planId: "",
      cpuCores: 2,
      ramGB: 4,
      diskGB: 50,
      templateId: "",
      nodeId: "",
      cpuModel: "",
      userData: "",
      ipPoolId: "",
      managedNetworkId: "",
      ipAddress: "",
      bandwidthMbps: 100,
      vlanId: "",
    },
    mode: "onChange",
  })

  const watchedValues = watch()

  // Only pools usable on the selected node — a pool bound to a different node would
  // allocate a non-routable IP (the backend rejects it). A pool with no node
  // binding works on any node; a node-bound pool only shows once its node is
  // selected (node is picked in step 1, before this step 4).
  const availablePools = useMemo(() => {
    const nodeId = watchedValues.nodeId
    return pools.filter((p) => {
      const bound = p.node_ids && p.node_ids.length > 0 ? p.node_ids : p.node_id ? [p.node_id] : []
      if (bound.length === 0) return true // unbound → usable on any node
      return !!nodeId && bound.includes(nodeId) // node-bound → only when that node is selected
    })
  }, [pools, watchedValues.nodeId])

  // Clear the IP pool if a node change made the current selection unavailable.
  useEffect(() => {
    if (watchedValues.ipPoolId && !availablePools.some((p) => p.id === watchedValues.ipPoolId)) {
      setValue("ipPoolId", "")
    }
  }, [availablePools, watchedValues.ipPoolId, setValue])

  const handleNext = async () => {
    let fieldsToValidate: (keyof FormData)[] = []
    
    switch (currentStep) {
      case 1:
        fieldsToValidate = ["hostname"]
        break
      case 2:
        fieldsToValidate = ["cpuCores", "ramGB", "diskGB"]
        break
      case 3:
        // Schema-level templateId is optional (image path); enforce it manually
        // for fresh installs.
        if (!sourceImageId && !watchedValues.templateId) {
          setError("templateId", { message: "Please select an OS template" })
          return
        }
        break
      case 4:
        fieldsToValidate = ["bandwidthMbps"]
        break
    }

    const isValid = await trigger(fieldsToValidate)
    if (isValid && currentStep < 5) {
      setCurrentStep(currentStep + 1)
    }
  }

  const handleBack = () => {
    if (currentStep > 1) {
      setCurrentStep(currentStep - 1)
    }
  }

  const onSubmit = async (data: FormData) => {
    // Only create from the final Review step. Guards against a stray submit —
    // e.g. the Enter key, or focus landing on the freshly-rendered submit button
    // when step 4 advances to step 5 — which would otherwise create the VM before
    // the user clicks "Create VM".
    if (currentStep !== 5) {
      return
    }
    setSubmitError(null)

    try {
      // VLAN is collected as free text; only forward a valid numeric tag.
      const vlanParsed = data.vlanId ? Number(data.vlanId) : NaN
      const vlanId = Number.isInteger(vlanParsed) && vlanParsed > 0 ? vlanParsed : undefined

      const result = await createVM.mutateAsync({
        hostname: data.hostname,
        // From-image creates omit the template — the backend derives the OS from
        // the image and clones the image's disk.
        os_template_id: sourceImageId ? undefined : data.templateId,
        source_image_id: sourceImageId || undefined,
        node_id: data.nodeId || undefined,
        // When a plan is selected the backend derives resources from it; we still
        // send the (plan-synced) sliders for display consistency.
        plan_id: data.planId || undefined,
        resources: {
          cpu: data.cpuCores,
          ram: data.ramGB * 1024, // Convert GB to MB
          disk: data.diskGB,
        },
        // Network: allocate from a managed pool (a specific IP is only valid
        // alongside a pool selection).
        ip_pool_id: data.ipPoolId || undefined,
        requested_ip: data.ipPoolId && data.ipAddress ? data.ipAddress : undefined,
        managed_network_id: data.managedNetworkId || undefined,
        bandwidth_mbps: data.bandwidthMbps,
        vlan_id: vlanId,
        cpu_model: data.cpuModel || undefined,
        user_data: data.userData || undefined,
        ssh_key_ids: selectedKeyIds,
        ssh_public_keys: pastedKeyList,
      })
      // Switch to the inline provisioning view (progress + one-time password)
      // instead of jumping straight to the VM detail page.
      setProvisioning({ id: result.id, password: result.root_password })
    } catch (error) {
      // Prefer the backend's message (e.g. "IP pool not assigned to this node")
      // over the generic axios "status code 4xx".
      const axiosErr = error as { response?: { data?: { message?: string; error?: string } } }
      const backendMsg = axiosErr.response?.data?.message || axiosErr.response?.data?.error
      setSubmitError(backendMsg || (error as Error).message || "Failed to create VM. Please try again.")
    }
  }

  const getSelectedTemplate = () => {
    return templates.find(t => t.id === watchedValues.templateId)
  }

  const getSelectedUser = () => {
    return users.find(u => u.id === watchedValues.userId)
  }

  const getSelectedNode = () => {
    return nodes.find(n => n.id === watchedValues.nodeId)
  }

  const getSelectedPool = () => {
    return pools.find(p => p.id === watchedValues.ipPoolId)
  }

  // Inline provisioning view — shown after Create instead of jumping straight to
  // the VM detail page. Displays live progress + the one-time root password.
  if (provisioning) {
    const status = provisioningVM.data?.status ?? "creating"
    const isRunning = status === "running"
    const failed = status === "error"
    const steps: { key: string; label: string; done: boolean; active: boolean }[] = [
      { key: "submitted", label: "Request submitted", done: true, active: false },
      {
        key: "provisioning",
        label: "Provisioning disk & installing OS",
        done: isRunning || status === "stopped",
        active: !isRunning && !failed && status !== "stopped",
      },
      { key: "running", label: "Running", done: isRunning, active: false },
    ]
    return (
      <div className="max-w-2xl mx-auto">
        <div className="mb-8">
          <h1 className="text-2xl font-semibold text-foreground mb-2">
            {failed ? "Provisioning failed" : isRunning ? "Your VM is ready" : "Creating your VM…"}
          </h1>
          <p className="text-muted-foreground text-sm">
            {provisioningVM.data?.hostname || "new virtual machine"}
          </p>
        </div>

        {/* Progress steps */}
        <div className="border rounded-lg bg-card text-card-foreground p-6 shadow-sm mb-6">
          <ul className="space-y-4">
            {steps.map((s) => (
              <li key={s.key} className="flex items-center gap-3">
                <span
                  className={`flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-xs font-semibold ${
                    s.done ? "bg-emerald-500 text-white border-emerald-500" : s.active ? "bg-amber-100 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900" : "bg-muted text-muted-foreground"
                  }`}
                >
                  {s.done ? "✓" : s.active ? "…" : "○"}
                </span>
                <span className={`font-medium ${s.done ? "text-foreground" : s.active ? "text-foreground" : "text-muted-foreground"}`}>
                  {s.label}
                  {s.active && <span className="ml-2 animate-pulse text-muted-foreground">in progress</span>}
                </span>
              </li>
            ))}
          </ul>
          {failed && (
            <p className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-sm font-medium text-red-700 dark:bg-red-950 dark:text-red-300 dark:border-red-900">
              Provisioning failed. Check the VM’s events on its detail page.
            </p>
          )}
        </div>

        {/* One-time root password */}
        {provisioning.password && (
          <div className="rounded-lg border border-amber-200 bg-amber-50 p-6 shadow-sm mb-6 dark:bg-amber-950/40 dark:border-amber-900">
            <h2 className="mb-1 text-lg font-semibold text-foreground">
              Root password
            </h2>
            <p className="mb-3 text-sm text-muted-foreground">
              Shown <strong>once</strong> — save it now. Log in as <code className="font-mono">root</code> with this password.
            </p>
            <div className="flex items-center gap-2 rounded-md border bg-background p-3">
              <code className="flex-1 break-all font-mono text-sm text-foreground">{provisioning.password}</code>
              <button
                type="button"
                onClick={() => {
                  navigator.clipboard?.writeText(provisioning.password || "")
                  setPwCopied(true)
                }}
                className="rounded-md border px-3 py-1 text-xs font-medium bg-background hover:bg-muted"
              >
                {pwCopied ? "Copied ✓" : "Copy"}
              </button>
            </div>
          </div>
        )}

        <div className="flex gap-3">
          <button
            type="button"
            onClick={() => router.push(`/vms/${provisioning.id}`)}
            className="flex-1 rounded-md bg-primary px-4 py-3 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          >
            {isRunning ? "Open VM →" : "Go to VM (still provisioning)"}
          </button>
        </div>
        {!isRunning && !failed && (
          <p className="mt-3 text-center text-xs font-medium text-muted-foreground">
            This can take a minute or two — the page updates automatically.
          </p>
        )}
      </div>
    )
  }

  // Get OS icon based on template name
  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <h1 className="text-2xl font-semibold text-foreground mb-2">
          Create New VM
        </h1>
        <p className="text-muted-foreground text-sm">
          Provision a new virtual machine in minutes
        </p>
      </div>

      {/* Progress Indicator */}
      <div className="mb-8">
        <div className="flex items-center justify-between">
          {steps.map((step, index) => {
            const Icon = step.icon
            const isActive = step.id === currentStep
            const isCompleted = step.id < currentStep
            
            return (
              <div key={step.id} className="flex items-center flex-1">
                <div className="flex flex-col items-center">
                  <div
                    className={`
                      w-11 h-11 flex items-center justify-center rounded-full border
                      transition-all duration-300
                      ${isCompleted ? "bg-emerald-500 text-white border-emerald-500" : ""}
                      ${isActive ? "bg-primary text-primary-foreground border-primary" : ""}
                      ${!isActive && !isCompleted ? "bg-muted text-muted-foreground" : ""}
                    `}
                  >
                    {isCompleted ? (
                      <Check className="w-5 h-5" />
                    ) : (
                      <Icon className="w-5 h-5" />
                    )}
                  </div>
                  <span className={`mt-2 text-xs font-medium ${
                    isActive ? "text-foreground" : "text-muted-foreground"
                  }`}>
                    {step.title}
                  </span>
                  <span className="text-[10px] text-muted-foreground">
                    {step.description}
                  </span>
                </div>
                
                {index < steps.length - 1 && (
                  <div className="flex-1 h-1 mx-2 rounded-full bg-muted overflow-hidden">
                    <div
                      className={`h-full bg-primary transition-all duration-300 ${
                        isCompleted ? "w-full" : "w-0"
                      }`}
                    />
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>

      {/* Form. The VM is created ONLY by clicking "Create VM" (the button calls
          handleSubmit directly). The form itself never submits-to-create, so no
          stray Enter keypress or focus-shift on the final step can trigger it. */}
      <form
        onSubmit={(e) => e.preventDefault()}
        onKeyDown={(e) => {
          // Belt-and-suspenders: stop Enter in a text field from doing anything
          // form-level before the Review step. Dropdowns/textarea keep Enter.
          if (
            e.key === "Enter" &&
            currentStep !== 5 &&
            (e.target as HTMLElement).tagName === "INPUT"
          ) {
            e.preventDefault()
          }
        }}
      >
        <div className="bg-card text-card-foreground border rounded-lg p-8 shadow-sm">

          {/* Step 1: Basic Info */}
          {currentStep === 1 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-muted text-foreground flex items-center justify-center rounded-md border">
                  <Server className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-semibold">Basic Information</h2>
                  <p className="text-sm text-muted-foreground">Configure hostname and assignment</p>
                </div>
              </div>

              {/* Hostname */}
              <div className="space-y-2">
                <label htmlFor="hostname" className="text-sm font-medium">
                  Hostname <span className="text-destructive">*</span>
                </label>
                <Input
                  id="hostname"
                  {...register("hostname")}
                  placeholder="e.g., web-server-01 or vm.example.com"
                  className={`h-12 ${errors.hostname ? "border-danger ring-2 ring-danger/30" : ""}`}
                />
                {errors.hostname && (
                  <p className="text-sm font-medium text-destructive flex items-center gap-1">
                    <AlertCircle className="w-4 h-4" />
                    {errors.hostname.message}
                  </p>
                )}
                <p className="text-xs text-muted-foreground">
                  Use a valid hostname format (letters, numbers, dots, hyphens)
                </p>
              </div>

              {/* User Assignment (optional) */}
              <div className="space-y-2">
                <label htmlFor="userId" className="text-sm font-medium">
                  Assign to User <span className="text-muted-foreground">(optional)</span>
                </label>
                {usersLoading ? (
                  <Skeleton className="h-12 w-full" />
                ) : (
                  <Controller
                    name="userId"
                    control={control}
                    render={({ field }) => (
                      <Select onValueChange={field.onChange} value={field.value}>
                        <SelectTrigger id="userId" className="h-12">
                          <SelectValue placeholder="Select a user (optional)" />
                        </SelectTrigger>
                        <SelectContent>
                          {users.map((user) => (
                            <SelectItem key={user.id} value={user.id}>
                              <div className="flex items-center gap-2">
                                <User className="w-4 h-4" />
                                <span>{user.email}</span>
                                <span className="text-muted-foreground text-sm">({user.role})</span>
                              </div>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                )}
              </div>

              {/* Node Selection (optional) */}
              <div className="space-y-2">
                <label htmlFor="nodeId" className="text-sm font-medium">
                  Target Node <span className="text-muted-foreground">(optional — auto-select if empty)</span>
                </label>
                {nodesLoading ? (
                  <Skeleton className="h-12 w-full" />
                ) : (
                  <Controller
                    name="nodeId"
                    control={control}
                    render={({ field }) => (
                      <Select onValueChange={field.onChange} value={field.value}>
                        <SelectTrigger id="nodeId" className="h-12">
                          <SelectValue placeholder="Auto-select best node" />
                        </SelectTrigger>
                        <SelectContent>
                          {activeNodes.map((node) => (
                            <SelectItem key={node.id} value={node.id}>
                              <div className="flex items-center gap-2">
                                <Server className="w-4 h-4" />
                                <span>{node.name}</span>
                                <span className="text-muted-foreground text-sm">({node.ip_address})</span>
                              </div>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                )}
              </div>
            </div>
          )}

          {/* Step 2: Resources */}
          {currentStep === 2 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-muted text-foreground flex items-center justify-center rounded-md border">
                  <Cpu className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-semibold">Resource Allocation</h2>
                  <p className="text-sm text-muted-foreground">Select CPU, RAM, and disk size</p>
                </div>
              </div>

              {/* Plan (flavor) selector — auto-fills the sliders below */}
              {plans.length > 0 && (
                <div className="space-y-2">
                  <label htmlFor="planId" className="text-sm font-medium">
                    Plan <span className="text-muted-foreground">(optional)</span>
                  </label>
                  <Select
                    value={watchedValues.planId || "custom"}
                    onValueChange={(val) => {
                      if (val === "custom") {
                        setValue("planId", "")
                        return
                      }
                      setValue("planId", val)
                      const p = plans.find((pl) => pl.id === val)
                      if (p) {
                        setValue("cpuCores", p.cpu, { shouldValidate: true })
                        setValue("ramGB", Math.max(1, Math.round(p.ram / 1024)), { shouldValidate: true })
                        setValue("diskGB", p.disk, { shouldValidate: true })
                      }
                    }}
                  >
                    <SelectTrigger id="planId" className="h-12">
                      <SelectValue placeholder="Custom (use sliders)" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="custom">Custom (use sliders)</SelectItem>
                      {plans.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          <span>{p.name}</span>
                          <span className="text-muted-foreground text-sm ml-2">
                            ({p.cpu} vCPU · {Math.round(p.ram / 1024)} GB · {p.disk} GB)
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">Pick a plan to auto-fill resources, or choose Custom.</p>
                </div>
              )}

              {/* CPU Cores */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="cpuCores" className="text-sm font-medium">CPU Cores</label>
                  <span className="text-2xl font-semibold">{watchedValues.cpuCores}</span>
                </div>
                <Controller
                  name="cpuCores"
                  control={control}
                  render={({ field: { onChange, value } }) => (
                    <div className="relative">
                      <input
                        id="cpuCores"
                        type="range"
                        min={1}
                        max={64}
                        value={value}
                        onChange={(e) => onChange(Number(e.target.value))}
                        className="w-full h-2 appearance-none rounded-full bg-muted cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, hsl(var(--primary)) 0%, hsl(var(--primary)) ${(value / 64) * 100}%, hsl(var(--muted)) ${(value / 64) * 100}%, hsl(var(--muted)) 100%)`
                        }}
                      />
                      <style jsx>{`
                        input[type="range"]::-webkit-slider-thumb {
                          -webkit-appearance: none;
                          width: 18px;
                          height: 18px;
                          border-radius: 9999px;
                          background: hsl(var(--primary));
                          border: 2px solid hsl(var(--background));
                          cursor: pointer;
                          box-shadow: 0 1px 2px 0 rgb(0 0 0 / 0.1);
                        }
                        input[type="range"]::-moz-range-thumb {
                          width: 18px;
                          height: 18px;
                          border-radius: 9999px;
                          background: hsl(var(--primary));
                          border: 2px solid hsl(var(--background));
                          cursor: pointer;
                          box-shadow: 0 1px 2px 0 rgb(0 0 0 / 0.1);
                        }
                      `}</style>
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span>1</span>
                  <span>64</span>
                </div>
              </div>

              {/* RAM */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="ramGB" className="text-sm font-medium">RAM (GB)</label>
                  <span className="text-2xl font-semibold">{watchedValues.ramGB} GB</span>
                </div>
                <Controller
                  name="ramGB"
                  control={control}
                  render={({ field: { onChange, value } }) => (
                    <div className="relative">
                      <input
                        id="ramGB"
                        type="range"
                        min={1}
                        max={512}
                        value={value}
                        onChange={(e) => onChange(Number(e.target.value))}
                        className="w-full h-2 appearance-none rounded-full bg-muted cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, hsl(var(--primary)) 0%, hsl(var(--primary)) ${(value / 512) * 100}%, hsl(var(--muted)) ${(value / 512) * 100}%, hsl(var(--muted)) 100%)`
                        }}
                      />
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span>1 GB</span>
                  <span>512 GB</span>
                </div>
              </div>

              {/* Disk */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="diskGB" className="text-sm font-medium">Disk (GB)</label>
                  <span className="text-2xl font-semibold">{watchedValues.diskGB} GB</span>
                </div>
                <Controller
                  name="diskGB"
                  control={control}
                  render={({ field: { onChange, value } }) => (
                    <div className="relative">
                      <input
                        id="diskGB"
                        type="range"
                        min={10}
                        max={2000}
                        step={10}
                        value={value}
                        onChange={(e) => onChange(Number(e.target.value))}
                        className="w-full h-2 appearance-none rounded-full bg-muted cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, hsl(var(--primary)) 0%, hsl(var(--primary)) ${(value / 2000) * 100}%, hsl(var(--muted)) ${(value / 2000) * 100}%, hsl(var(--muted)) 100%)`
                        }}
                      />
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span>10 GB</span>
                  <span>2 TB</span>
                </div>
              </div>

              {/* CPU Model */}
              <div className="space-y-2">
                <label htmlFor="cpuModel" className="text-sm font-medium">CPU Model</label>
                <select
                  id="cpuModel"
                  {...register("cpuModel")}
                  className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                >
                  <option value="">Default — kvm64 (portable, live-migratable)</option>
                  <option value="host-passthrough">Host Passthrough (max performance, not migratable)</option>
                  <option value="host-model">Host Model (near-host features)</option>
                  <option value="qemu64">qemu64 (generic)</option>
                  <option value="Haswell-noTSX">Haswell-noTSX (Intel baseline)</option>
                  <option value="EPYC">EPYC (AMD baseline)</option>
                </select>
                <p className="text-xs text-muted-foreground">
                  Leave default for cross-node live migration. Choose Host Passthrough for best single-node performance.
                </p>
              </div>

              {/* Recipe selector (fills the script below from a saved recipe) */}
              {recipes.length > 0 && (
                <div className="space-y-2">
                  <label htmlFor="recipeId" className="text-sm font-medium">Recipe (optional)</label>
                  <select
                    id="recipeId"
                    value={selectedRecipeId}
                    onChange={(e) => {
                      const id = e.target.value
                      setSelectedRecipeId(id)
                      const r = recipes.find((x) => x.id === id)
                      if (r) setValue("userData", r.script)
                    }}
                    className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                  >
                    <option value="">None — write a script below</option>
                    {recipes.map((r) => (
                      <option key={r.id} value={r.id}>{r.name}</option>
                    ))}
                  </select>
                  <p className="text-xs text-muted-foreground">
                    Pick a saved recipe to fill the script below (still editable). Manage them in Settings → Recipes.
                  </p>
                </div>
              )}

              {/* Startup script / recipe */}
              <div className="space-y-2">
                <label htmlFor="userData" className="text-sm font-medium">Startup Script (optional)</label>
                <textarea
                  id="userData"
                  {...register("userData")}
                  rows={5}
                  placeholder={"#!/bin/bash\napt-get update && apt-get install -y nginx"}
                  className="w-full rounded-md border border-input bg-background p-3 font-mono text-xs resize-y focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                />
                <p className="text-xs text-muted-foreground">
                  Runs once on first boot via cloud-init (first-boot recipe). Requires a cloud-init image. Plain shell script or cloud-config.
                </p>
              </div>

              {/* Resource Summary */}
              <div className="bg-muted border rounded-md p-4 mt-6">
                <p className="text-xs font-medium mb-2">Selected Resources</p>
                <div className="flex items-center gap-6">
                  <div className="flex items-center gap-2">
                    <Cpu className="w-5 h-5" />
                    <span className="font-medium">{watchedValues.cpuCores} vCPU</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <HardDrive className="w-5 h-5" />
                    <span className="font-medium">{watchedValues.ramGB} GB RAM</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <HardDrive className="w-5 h-5" />
                    <span className="font-medium">{watchedValues.diskGB} GB Disk</span>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Step 3: OS Template */}
          {currentStep === 3 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-muted text-foreground flex items-center justify-center rounded-md border">
                  <HardDrive className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-semibold">{sourceImageId ? "Source Image" : "Select OS Template"}</h2>
                  <p className="text-sm text-muted-foreground">
                    {sourceImageId ? "The VM's disk will be cloned from a saved image" : "Choose an operating system for your VM"}
                  </p>
                </div>
              </div>

              {sourceImageId ? (
                <div className="flex items-center gap-3 rounded-lg border border-primary/50 bg-primary/5 p-4">
                  <Layers className="w-5 h-5 text-primary shrink-0" />
                  <div className="text-sm">
                    <p className="font-medium">
                      Creating from image: <span className="font-semibold">{sourceImage?.name ?? sourceImageId}</span>
                    </p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      No OS template needed — the OS is derived from the image.
                    </p>
                  </div>
                </div>
              ) : templatesLoading ? (
                <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
                  {[1, 2, 3, 4, 5, 6].map(i => (
                    <Skeleton key={i} className="h-28 w-full" />
                  ))}
                </div>
              ) : installableTemplates.length === 0 ? (
                <div className="p-8 text-center border">
                  <HardDrive className="w-10 h-10 mx-auto text-muted-foreground mb-3" />
                  <p className="text-muted-foreground font-medium text-sm">No installable OS templates</p>
                  <p className="text-muted-foreground text-xs mt-1">
                    {templates.length === 0
                      ? "Create an OS template first on the Templates page."
                      : "Your templates are import placeholders with no base image. Add a real template (with an image URL) on the Templates page."}
                  </p>
                </div>
              ) : (
                <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
                  {installableTemplates.map((template) => {
                    const isSelected = watchedValues.templateId === template.id
                    
                    return (
                      <button
                        key={template.id}
                        type="button"
                        onClick={() => setValue("templateId", template.id, { shouldValidate: true })}
                        className={`
                          relative p-4 rounded-lg border text-left transition-all duration-200
                          ${isSelected
                            ? "border-primary ring-1 ring-primary bg-primary/5"
                            : "bg-card hover:bg-muted/50 hover:border-foreground/20"
                          }
                        `}
                      >
                        {isSelected && (
                          <div className="absolute top-2 right-2 w-6 h-6 rounded-full bg-primary text-primary-foreground flex items-center justify-center">
                            <Check className="w-4 h-4" />
                          </div>
                        )}
                        
                        <div className="mb-2 flex justify-center"><OSIcon name={template.name} className="w-10 h-10" /></div>
                        <p className="font-semibold text-sm leading-tight">{template.name}</p>
                        <p className="text-xs text-muted-foreground mt-1">v{template.version}</p>
                        {template.description && (
                          <span className="inline-block mt-2 px-2 py-0.5 rounded-md bg-muted text-muted-foreground text-xs font-medium">
                            {template.description}
                          </span>
                        )}
                      </button>
                    )
                  })}
                </div>
              )}

              {errors.templateId && (
                <p className="text-sm font-medium text-destructive flex items-center gap-1">
                  <AlertCircle className="w-4 h-4" />
                  {errors.templateId.message}
                </p>
              )}

              {/* SSH keys — injected into the new guest's root account */}
              <div className="border-t pt-5">
                <h3 className="font-semibold text-sm mb-1">SSH Keys</h3>
                <p className="text-xs text-muted-foreground mb-3">Injected into the new VM&apos;s root account for key-based login. Select saved keys and/or paste one below.</p>

                {!!sshKeys?.length && (
                  <div className="space-y-2 mb-4">
                    {sshKeys.map((k) => (
                      <label key={k.id} className="flex items-center gap-3 p-3 border cursor-pointer">
                        <input
                          type="checkbox"
                          checked={!excludedKeys.has(k.id)}
                          onChange={(e) =>
                            setExcludedKeys((prev) => {
                              const next = new Set(prev)
                              if (e.target.checked) next.delete(k.id)
                              else next.add(k.id)
                              return next
                            })
                          }
                          className="w-4 h-4"
                        />
                        <span className="font-medium">{k.name}</span>
                        <span className="text-xs font-mono text-muted-foreground truncate">{k.fingerprint}</span>
                      </label>
                    ))}
                  </div>
                )}

                <label className="block text-xs font-semibold text-muted-foreground mb-1">Paste a public key (optional)</label>
                <textarea
                  value={pastedKeys}
                  onChange={(e) => setPastedKeys(e.target.value)}
                  placeholder="ssh-ed25519 AAAA… user@host&#10;(one key per line)"
                  rows={3}
                  className="w-full px-3 py-2 rounded-md border border-input bg-background font-mono text-sm placeholder:text-muted-foreground"
                />
                {!sshKeys?.length && !pastedKeyList.length && (
                  <p className="text-xs text-muted-foreground mt-1">No key selected — the VM will use root-password login only.</p>
                )}
              </div>
            </div>
          )}

          {/* Step 4: Network */}
          {currentStep === 4 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-muted text-foreground flex items-center justify-center rounded-md border">
                  <Network className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-semibold">Network Configuration</h2>
                  <p className="text-sm text-muted-foreground">IP allocation, bandwidth, and VLAN</p>
                </div>
              </div>

              <div className="bg-muted border rounded-md p-4 mb-4">
                <p className="text-xs font-medium text-muted-foreground flex items-center gap-2">
                  <AlertCircle className="w-4 h-4" />
                  Note: The IP is allocated from the pool at creation; bandwidth and VLAN apply when the VM boots
                </p>
              </div>

              {/* Private network / VPC */}
              {managedNets && managedNets.some((n) => n.type === "isolated" || n.type === "nat") && (
                <div className="space-y-2 mb-4">
                  <label htmlFor="managedNetworkId" className="text-sm font-medium">
                    Private Network / VPC <span className="text-muted-foreground">(optional)</span>
                  </label>
                  <select
                    id="managedNetworkId"
                    {...register("managedNetworkId")}
                    className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                  >
                    <option value="">None — public network</option>
                    {managedNets
                      .filter((n) => n.type === "isolated" || n.type === "nat")
                      .map((n) => (
                        <option key={n.id} value={n.id}>{n.name} ({n.type})</option>
                      ))}
                  </select>
                  <p className="text-xs text-muted-foreground">
                    Attach the VM&apos;s NIC to a private/isolated network instead of the public bridge.
                  </p>
                </div>
              )}

              {/* IP Pool Selection */}
              <div className="space-y-2">
                <label htmlFor="ipPoolId" className="text-sm font-medium">
                  IP Pool <span className="text-muted-foreground">(optional — DHCP if empty)</span>
                </label>
                {poolsLoading ? (
                  <Skeleton className="h-12 w-full" />
                ) : availablePools.length === 0 ? (
                  <div className="p-4 border rounded-md bg-muted text-sm text-muted-foreground font-medium">
                    {pools.length === 0
                      ? "No IP pools configured — the VM will use DHCP. Create pools under the IP Pools page to assign managed addresses."
                      : "No IP pools are assigned to the selected node — the VM will use DHCP. Assign a pool to this node under the IP Pools page."}
                  </div>
                ) : (
                  <Controller
                    name="ipPoolId"
                    control={control}
                    render={({ field }) => (
                      <Select onValueChange={field.onChange} value={field.value}>
                        <SelectTrigger id="ipPoolId" className="h-12">
                          <SelectValue placeholder="DHCP (no pool)" />
                        </SelectTrigger>
                        <SelectContent>
                          {availablePools.map((pool) => (
                            <SelectItem key={pool.id} value={pool.id}>
                              <div className="flex items-center gap-2">
                                <Network className="w-4 h-4" />
                                <span>{pool.name}</span>
                                {pool.cidr && (
                                  <span className="text-muted-foreground text-sm">({pool.cidr})</span>
                                )}
                              </div>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                )}
                <p className="text-xs text-muted-foreground">
                  Selecting a pool assigns the next available public IP automatically.
                </p>
              </div>

              {/* Specific IP within the pool (optional) */}
              <div className="space-y-2">
                <label htmlFor="ipAddress" className="text-sm font-medium">
                  Specific IP <span className="text-muted-foreground">(optional)</span>
                </label>
                <Input
                  id="ipAddress"
                  {...register("ipAddress")}
                  placeholder={watchedValues.ipPoolId ? "e.g., 192.168.1.100" : "Select an IP pool first"}
                  disabled={!watchedValues.ipPoolId}
                  className={`h-12 ${errors.ipAddress ? "border-danger ring-2 ring-danger/30" : ""} ${!watchedValues.ipPoolId ? "opacity-50 cursor-not-allowed" : ""}`}
                />
                {errors.ipAddress && (
                  <p className="text-sm font-medium text-destructive flex items-center gap-1">
                    <AlertCircle className="w-4 h-4" />
                    {errors.ipAddress.message}
                  </p>
                )}
                <p className="text-xs text-muted-foreground">
                  Leave blank to auto-allocate the next free IP from the selected pool.
                </p>
              </div>

              {/* Bandwidth */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="bandwidthMbps" className="text-sm font-medium">Bandwidth Limit</label>
                  <span className="text-2xl font-semibold">{watchedValues.bandwidthMbps} Mbps</span>
                </div>
                <Controller
                  name="bandwidthMbps"
                  control={control}
                  render={({ field: { onChange, value } }) => (
                    <div className="relative">
                      <input
                        id="bandwidthMbps"
                        type="range"
                        min={1}
                        max={10000}
                        value={value}
                        onChange={(e) => onChange(Number(e.target.value))}
                        className="w-full h-2 appearance-none rounded-full bg-muted cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, hsl(var(--primary)) 0%, hsl(var(--primary)) ${(value / 10000) * 100}%, hsl(var(--muted)) ${(value / 10000) * 100}%, hsl(var(--muted)) 100%)`
                        }}
                      />
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span>1 Mbps</span>
                  <span>10 Gbps</span>
                </div>
              </div>

              {/* VLAN ID (optional text input) */}
              <div className="space-y-2">
                <label htmlFor="vlanId" className="text-sm font-medium">
                  VLAN ID <span className="text-muted-foreground">(optional)</span>
                </label>
                <Input
                  id="vlanId"
                  {...register("vlanId")}
                  placeholder="e.g., 10, 20, 100"
                  className="h-12"
                />
                <p className="text-xs text-muted-foreground">
                  Enter a VLAN ID to assign this VM to a specific network segment
                </p>
              </div>

              {/* Network Preview */}
              <div className="bg-muted border rounded-md p-4">
                <div className="flex items-center gap-2 mb-2">
                  <Network className="w-5 h-5" />
                  <p className="text-xs font-medium">Network Settings Preview</p>
                </div>
                <div className="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <span className="text-muted-foreground">IP Pool:</span>
                    <span className="ml-2 font-medium">{getSelectedPool()?.name || "DHCP"}</span>
                  </div>
                  <div>
                    <span className="text-muted-foreground">IP Address:</span>
                    <span className="ml-2 font-medium">
                      {getSelectedPool() ? (watchedValues.ipAddress || "Auto (next free)") : "Auto (DHCP)"}
                    </span>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Bandwidth:</span>
                    <span className="ml-2 font-medium">{watchedValues.bandwidthMbps} Mbps</span>
                  </div>
                  <div>
                    <span className="text-muted-foreground">VLAN:</span>
                    <span className="ml-2 font-medium">
                      {watchedValues.vlanId || "None"}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Step 5: Review */}
          {currentStep === 5 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-muted text-foreground flex items-center justify-center rounded-md border">
                  <CheckCircle2 className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-semibold">Review & Create</h2>
                  <p className="text-sm text-muted-foreground">Confirm your VM configuration before creating</p>
                </div>
              </div>

              {/* Summary Cards */}
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {/* Basic Info */}
                <div className="bg-muted border rounded-md p-4">
                  <p className="text-xs font-medium mb-3 flex items-center gap-2">
                    <Server className="w-4 h-4" /> Basic Information
                  </p>
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">Hostname:</span>
                      <span className="font-medium">{watchedValues.hostname}</span>
                    </div>
                    {getSelectedUser() && (
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">Assigned To:</span>
                        <span className="font-medium">{getSelectedUser()?.email}</span>
                      </div>
                    )}
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">Node:</span>
                      <span className="font-medium">{getSelectedNode()?.name || "Auto-select"}</span>
                    </div>
                  </div>
                </div>

                {/* Resources */}
                <div className="bg-muted border rounded-md p-4">
                  <p className="text-xs font-medium mb-3 flex items-center gap-2">
                    <Cpu className="w-4 h-4" /> Resources
                  </p>
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">CPU:</span>
                      <span className="font-medium">{watchedValues.cpuCores} vCPU</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">RAM:</span>
                      <span className="font-medium">{watchedValues.ramGB} GB</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">Disk:</span>
                      <span className="font-medium">{watchedValues.diskGB} GB</span>
                    </div>
                  </div>
                </div>

                {/* OS Template */}
                <div className="bg-muted border rounded-md p-4">
                  <p className="text-xs font-medium mb-3 flex items-center gap-2">
                    <HardDrive className="w-4 h-4" /> {sourceImageId ? "Source Image" : "OS Template"}
                  </p>
                  {sourceImageId && (
                    <div className="flex items-center gap-3">
                      <Layers className="w-8 h-8 text-primary" />
                      <div>
                        <p className="font-medium">{sourceImage?.name ?? sourceImageId}</p>
                        <p className="text-sm text-muted-foreground">Disk cloned from image</p>
                      </div>
                    </div>
                  )}
                  {!sourceImageId && getSelectedTemplate() && (
                    <div className="flex items-center gap-3">
                      <OSIcon name={getSelectedTemplate()!.name} className="w-8 h-8" />
                      <div>
                        <p className="font-medium">{getSelectedTemplate()?.name}</p>
                        <p className="text-sm text-muted-foreground">v{getSelectedTemplate()?.version}</p>
                      </div>
                    </div>
                  )}
                </div>

                {/* Network */}
                <div className="bg-muted border rounded-md p-4">
                  <p className="text-xs font-medium mb-3 flex items-center gap-2">
                    <Network className="w-4 h-4" /> Network
                  </p>
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">IP Pool:</span>
                      <span className="font-medium">{getSelectedPool()?.name || "DHCP"}</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">IP Address:</span>
                      <span className="font-medium">
                        {getSelectedPool() ? (watchedValues.ipAddress || "Auto (next free)") : "Auto (DHCP)"}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">Bandwidth:</span>
                      <span className="font-medium">{watchedValues.bandwidthMbps} Mbps</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">VLAN:</span>
                      <span className="font-medium">{watchedValues.vlanId || "None"}</span>
                    </div>
                  </div>
                  <p className="text-[10px] text-muted-foreground mt-2 italic">
                    Network settings are provisioned together with the VM
                  </p>
                </div>
              </div>

              {/* Error Message */}
              {submitError && (
                <div className="rounded-md border border-red-200 bg-red-50 p-4 flex items-center gap-2 dark:bg-red-950 dark:border-red-900">
                  <AlertCircle className="w-5 h-5 text-destructive" />
                  <p className="text-sm font-medium text-destructive">{submitError}</p>
                </div>
              )}
            </div>
          )}

          {/* Navigation Buttons */}
          <div className="flex items-center justify-between mt-8 pt-6 border-t">
            <Button
              type="button"
              variant="ghost"
              onClick={handleBack}
              disabled={currentStep === 1 || createVM.isPending}
              className="gap-2"
            >
              <ChevronLeft className="w-4 h-4" />
              Previous
            </Button>

            {currentStep < 5 ? (
              <Button
                type="button"
                onClick={handleNext}
                className="gap-2"
              >
                Next
                <ChevronRight className="w-4 h-4" />
              </Button>
            ) : (
              <Button
                type="button"
                variant="success"
                onClick={handleSubmit(onSubmit)}
                disabled={createVM.isPending}
                className="gap-2"
              >
                {createVM.isPending ? (
                  <>
                    <Loader2 className="w-4 h-4 animate-spin" />
                    Creating VM...
                  </>
                ) : (
                  <>
                    <CheckCircle2 className="w-4 h-4" />
                    Create VM
                  </>
                )}
              </Button>
            )}
          </div>
        </div>
      </form>
    </div>
  )
}
