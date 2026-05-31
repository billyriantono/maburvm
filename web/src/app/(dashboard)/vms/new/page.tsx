"use client"

import { useState, useMemo, useEffect } from "react"
import { useRouter } from "next/navigation"
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
  User
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
import { usePlans } from "@/lib/hooks/use-plans"
import { useRecipes } from "@/lib/hooks/use-recipes"
import { useCreateVM } from "@/lib/hooks/use-vms"

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

const step3Schema = z.object({
  templateId: z.string().min(1, "Please select an OS template"),
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
const steps = [
  { id: 1, title: "Basic", description: "Hostname & Node", icon: Server },
  { id: 2, title: "Resources", description: "CPU, RAM, Disk", icon: Cpu },
  { id: 3, title: "OS Template", description: "Select OS", icon: HardDrive },
  { id: 4, title: "Network", description: "IP & Config", icon: Network },
  { id: 5, title: "Review", description: "Confirm & Create", icon: CheckCircle2 },
]

export default function NewVMPage() {
  const router = useRouter()
  const [currentStep, setCurrentStep] = useState(1)
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
  const nodes = useMemo(() => nodesData || [], [nodesData])
  const activeNodes = useMemo(() => nodes.filter(n => n.status === "active"), [nodes])
  const pools = useMemo(() => poolsData || [], [poolsData])
  const plans = useMemo(() => plansData || [], [plansData])
  const { data: recipesData } = useRecipes()
  const recipes = useMemo(() => recipesData || [], [recipesData])
  const [selectedRecipeId, setSelectedRecipeId] = useState("")

  const {
    register,
    handleSubmit,
    control,
    watch,
    trigger,
    setValue,
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
        fieldsToValidate = ["templateId"]
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
    setSubmitError(null)

    try {
      // VLAN is collected as free text; only forward a valid numeric tag.
      const vlanParsed = data.vlanId ? Number(data.vlanId) : NaN
      const vlanId = Number.isInteger(vlanParsed) && vlanParsed > 0 ? vlanParsed : undefined

      const result = await createVM.mutateAsync({
        hostname: data.hostname,
        os_template_id: data.templateId,
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
      })
      router.push(`/vms/${result.id}`)
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

  // Get OS icon based on template name
  const getTemplateIcon = (name: string): string => {
    const lower = name.toLowerCase()
    if (lower.includes("ubuntu")) return "🟠"
    if (lower.includes("debian")) return "🔴"
    if (lower.includes("centos") || lower.includes("alma") || lower.includes("rocky")) return "🟢"
    if (lower.includes("fedora")) return "🔵"
    if (lower.includes("windows")) return "🪟"
    if (lower.includes("arch")) return "🔷"
    return "🐧"
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8">
        <h1 className="text-3xl font-black uppercase tracking-tight text-black mb-2">
          Create New VM
        </h1>
        <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
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
                      w-12 h-12 flex items-center justify-center border-4 border-black 
                      transition-all duration-300
                      ${isCompleted ? "bg-success shadow-neo" : ""}
                      ${isActive ? "bg-primary shadow-neo scale-110" : ""}
                      ${!isActive && !isCompleted ? "bg-white shadow-neo-sm" : ""}
                    `}
                  >
                    {isCompleted ? (
                      <Check className="w-6 h-6" />
                    ) : (
                      <Icon className={`w-6 h-6 ${isActive ? "" : "text-gray-600"}`} />
                    )}
                  </div>
                  <span className={`mt-2 text-xs font-bold uppercase tracking-wider ${
                    isActive ? "text-black" : "text-gray-600"
                  }`}>
                    {step.title}
                  </span>
                  <span className="text-[10px] text-gray-500">
                    {step.description}
                  </span>
                </div>
                
                {index < steps.length - 1 && (
                  <div className={`flex-1 h-2 mx-2 border-2 border-black ${
                    isCompleted ? "bg-success" : "bg-gray-200"
                  }`}>
                    <div 
                      className={`h-full bg-black transition-all duration-300 ${
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

      {/* Form */}
      <form onSubmit={handleSubmit(onSubmit)}>
        <div className="bg-white border-4 border-black p-8 shadow-neo">
          
          {/* Step 1: Basic Info */}
          {currentStep === 1 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-primary flex items-center justify-center border-2 border-black">
                  <Server className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-black uppercase">Basic Information</h2>
                  <p className="text-sm text-gray-500">Configure hostname and assignment</p>
                </div>
              </div>

              {/* Hostname */}
              <div className="space-y-2">
                <label htmlFor="hostname" className="text-sm font-bold uppercase tracking-wide">
                  Hostname <span className="text-danger">*</span>
                </label>
                <Input
                  id="hostname"
                  {...register("hostname")}
                  placeholder="e.g., web-server-01 or vm.example.com"
                  className={`h-12 ${errors.hostname ? "border-danger ring-2 ring-danger/30" : ""}`}
                />
                {errors.hostname && (
                  <p className="text-sm font-medium text-danger flex items-center gap-1">
                    <AlertCircle className="w-4 h-4" />
                    {errors.hostname.message}
                  </p>
                )}
                <p className="text-xs text-gray-500">
                  Use a valid hostname format (letters, numbers, dots, hyphens)
                </p>
              </div>

              {/* User Assignment (optional) */}
              <div className="space-y-2">
                <label htmlFor="userId" className="text-sm font-bold uppercase tracking-wide">
                  Assign to User <span className="text-gray-600">(optional)</span>
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
                                <span className="text-gray-600 text-sm">({user.role})</span>
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
                <label htmlFor="nodeId" className="text-sm font-bold uppercase tracking-wide">
                  Target Node <span className="text-gray-600">(optional — auto-select if empty)</span>
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
                                <span className="text-gray-600 text-sm">({node.ip_address})</span>
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
                <div className="w-10 h-10 bg-secondary flex items-center justify-center border-2 border-black">
                  <Cpu className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-black uppercase">Resource Allocation</h2>
                  <p className="text-sm text-gray-500">Select CPU, RAM, and disk size</p>
                </div>
              </div>

              {/* Plan (flavor) selector — auto-fills the sliders below */}
              {plans.length > 0 && (
                <div className="space-y-2">
                  <label htmlFor="planId" className="text-sm font-bold uppercase tracking-wide">
                    Plan <span className="text-gray-500">(optional)</span>
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
                          <span className="text-gray-600 text-sm ml-2">
                            ({p.cpu} vCPU · {Math.round(p.ram / 1024)} GB · {p.disk} GB)
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-gray-500">Pick a plan to auto-fill resources, or choose Custom.</p>
                </div>
              )}

              {/* CPU Cores */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="cpuCores" className="text-sm font-bold uppercase tracking-wide">CPU Cores</label>
                  <span className="text-2xl font-black">{watchedValues.cpuCores}</span>
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
                        className="w-full h-4 appearance-none bg-white border-2 border-black cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, #000000 0%, #000000 ${(value / 64) * 100}%, #e5e5e5 ${(value / 64) * 100}%, #e5e5e5 100%)`
                        }}
                      />
                      <style jsx>{`
                        input[type="range"]::-webkit-slider-thumb {
                          -webkit-appearance: none;
                          width: 24px;
                          height: 24px;
                          background: #FFE500;
                          border: 3px solid black;
                          cursor: pointer;
                          box-shadow: 2px 2px 0 0 #000;
                        }
                        input[type="range"]::-moz-range-thumb {
                          width: 24px;
                          height: 24px;
                          background: #FFE500;
                          border: 3px solid black;
                          cursor: pointer;
                          box-shadow: 2px 2px 0 0 #000;
                        }
                      `}</style>
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-gray-500">
                  <span>1</span>
                  <span>64</span>
                </div>
              </div>

              {/* RAM */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="ramGB" className="text-sm font-bold uppercase tracking-wide">RAM (GB)</label>
                  <span className="text-2xl font-black">{watchedValues.ramGB} GB</span>
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
                        className="w-full h-4 appearance-none bg-white border-2 border-black cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, #000000 0%, #000000 ${(value / 512) * 100}%, #e5e5e5 ${(value / 512) * 100}%, #e5e5e5 100%)`
                        }}
                      />
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-gray-500">
                  <span>1 GB</span>
                  <span>512 GB</span>
                </div>
              </div>

              {/* Disk */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="diskGB" className="text-sm font-bold uppercase tracking-wide">Disk (GB)</label>
                  <span className="text-2xl font-black">{watchedValues.diskGB} GB</span>
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
                        className="w-full h-4 appearance-none bg-white border-2 border-black cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, #000000 0%, #000000 ${(value / 2000) * 100}%, #e5e5e5 ${(value / 2000) * 100}%, #e5e5e5 100%)`
                        }}
                      />
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-gray-500">
                  <span>10 GB</span>
                  <span>2 TB</span>
                </div>
              </div>

              {/* CPU Model */}
              <div className="space-y-2">
                <label htmlFor="cpuModel" className="text-sm font-bold uppercase tracking-wide">CPU Model</label>
                <select
                  id="cpuModel"
                  {...register("cpuModel")}
                  className="w-full h-12 px-3 border-2 border-black font-medium bg-white"
                >
                  <option value="">Default — kvm64 (portable, live-migratable)</option>
                  <option value="host-passthrough">Host Passthrough (max performance, not migratable)</option>
                  <option value="host-model">Host Model (near-host features)</option>
                  <option value="qemu64">qemu64 (generic)</option>
                  <option value="Haswell-noTSX">Haswell-noTSX (Intel baseline)</option>
                  <option value="EPYC">EPYC (AMD baseline)</option>
                </select>
                <p className="text-xs text-gray-500">
                  Leave default for cross-node live migration. Choose Host Passthrough for best single-node performance.
                </p>
              </div>

              {/* Recipe selector (fills the script below from a saved recipe) */}
              {recipes.length > 0 && (
                <div className="space-y-2">
                  <label htmlFor="recipeId" className="text-sm font-bold uppercase tracking-wide">Recipe (optional)</label>
                  <select
                    id="recipeId"
                    value={selectedRecipeId}
                    onChange={(e) => {
                      const id = e.target.value
                      setSelectedRecipeId(id)
                      const r = recipes.find((x) => x.id === id)
                      if (r) setValue("userData", r.script)
                    }}
                    className="w-full h-12 px-3 border-2 border-black font-medium bg-white"
                  >
                    <option value="">None — write a script below</option>
                    {recipes.map((r) => (
                      <option key={r.id} value={r.id}>{r.name}</option>
                    ))}
                  </select>
                  <p className="text-xs text-gray-500">
                    Pick a saved recipe to fill the script below (still editable). Manage them in Settings → Recipes.
                  </p>
                </div>
              )}

              {/* Startup script / recipe */}
              <div className="space-y-2">
                <label htmlFor="userData" className="text-sm font-bold uppercase tracking-wide">Startup Script (optional)</label>
                <textarea
                  id="userData"
                  {...register("userData")}
                  rows={5}
                  placeholder={"#!/bin/bash\napt-get update && apt-get install -y nginx"}
                  className="w-full border-2 border-black p-3 font-mono text-xs resize-y focus:outline-none focus:ring-2 focus:ring-primary"
                />
                <p className="text-xs text-gray-500">
                  Runs once on first boot via cloud-init (Virtualizor-style recipe). Requires a cloud-init image. Plain shell script or cloud-config.
                </p>
              </div>

              {/* Resource Summary */}
              <div className="bg-primary/20 border-2 border-black p-4 mt-6">
                <p className="text-xs font-bold uppercase mb-2">Selected Resources</p>
                <div className="flex items-center gap-6">
                  <div className="flex items-center gap-2">
                    <Cpu className="w-5 h-5" />
                    <span className="font-bold">{watchedValues.cpuCores} vCPU</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <HardDrive className="w-5 h-5" />
                    <span className="font-bold">{watchedValues.ramGB} GB RAM</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <HardDrive className="w-5 h-5" />
                    <span className="font-bold">{watchedValues.diskGB} GB Disk</span>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Step 3: OS Template */}
          {currentStep === 3 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-success flex items-center justify-center border-2 border-black">
                  <HardDrive className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-black uppercase">Select OS Template</h2>
                  <p className="text-sm text-gray-500">Choose an operating system for your VM</p>
                </div>
              </div>

              {templatesLoading ? (
                <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
                  {[1, 2, 3, 4, 5, 6].map(i => (
                    <Skeleton key={i} className="h-28 w-full" />
                  ))}
                </div>
              ) : templates.length === 0 ? (
                <div className="p-8 text-center border-2 border-black">
                  <HardDrive className="w-10 h-10 mx-auto text-gray-500 mb-3" />
                  <p className="text-gray-500 font-bold uppercase text-sm">No templates available</p>
                  <p className="text-gray-600 text-xs mt-1">Please create OS templates first</p>
                </div>
              ) : (
                <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
                  {templates.filter(t => t.is_active).map((template) => {
                    const isSelected = watchedValues.templateId === template.id
                    
                    return (
                      <button
                        key={template.id}
                        type="button"
                        onClick={() => setValue("templateId", template.id, { shouldValidate: true })}
                        className={`
                          relative p-4 border-4 text-left transition-all duration-200
                          ${isSelected 
                            ? "bg-primary border-black shadow-neo hover:shadow-neo-hover" 
                            : "bg-white border-black hover:shadow-neo-sm hover:-translate-y-1"
                          }
                        `}
                      >
                        {isSelected && (
                          <div className="absolute top-2 right-2 w-6 h-6 bg-black text-white flex items-center justify-center">
                            <Check className="w-4 h-4" />
                          </div>
                        )}
                        
                        <div className="text-4xl mb-2">{getTemplateIcon(template.name)}</div>
                        <p className="font-black uppercase text-sm leading-tight">{template.name}</p>
                        <p className="text-xs text-gray-500 mt-1">v{template.version}</p>
                        {template.description && (
                          <span className="inline-block mt-2 px-2 py-0.5 bg-gray-200 text-xs font-bold uppercase">
                            {template.description}
                          </span>
                        )}
                      </button>
                    )
                  })}
                </div>
              )}

              {errors.templateId && (
                <p className="text-sm font-medium text-danger flex items-center gap-1">
                  <AlertCircle className="w-4 h-4" />
                  {errors.templateId.message}
                </p>
              )}
            </div>
          )}

          {/* Step 4: Network */}
          {currentStep === 4 && (
            <div className="space-y-6 animate-in fade-in slide-in-from-right-4 duration-300">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 bg-accent text-white flex items-center justify-center border-2 border-black">
                  <Network className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-black uppercase">Network Configuration</h2>
                  <p className="text-sm text-gray-500">IP allocation, bandwidth, and VLAN</p>
                </div>
              </div>

              <div className="bg-gray-100 border-2 border-black p-4 mb-4">
                <p className="text-xs font-bold uppercase text-gray-600 flex items-center gap-2">
                  <AlertCircle className="w-4 h-4" />
                  Note: The IP is allocated from the pool at creation; bandwidth and VLAN apply when the VM boots
                </p>
              </div>

              {/* Private network / VPC */}
              {managedNets && managedNets.some((n) => n.type === "isolated" || n.type === "nat") && (
                <div className="space-y-2 mb-4">
                  <label htmlFor="managedNetworkId" className="text-sm font-bold uppercase tracking-wide">
                    Private Network / VPC <span className="text-gray-500">(optional)</span>
                  </label>
                  <select
                    id="managedNetworkId"
                    {...register("managedNetworkId")}
                    className="w-full h-12 px-3 border-2 border-black font-medium bg-white"
                  >
                    <option value="">None — public network</option>
                    {managedNets
                      .filter((n) => n.type === "isolated" || n.type === "nat")
                      .map((n) => (
                        <option key={n.id} value={n.id}>{n.name} ({n.type})</option>
                      ))}
                  </select>
                  <p className="text-xs text-gray-500">
                    Attach the VM&apos;s NIC to a private/isolated network instead of the public bridge.
                  </p>
                </div>
              )}

              {/* IP Pool Selection */}
              <div className="space-y-2">
                <label htmlFor="ipPoolId" className="text-sm font-bold uppercase tracking-wide">
                  IP Pool <span className="text-gray-500">(optional — DHCP if empty)</span>
                </label>
                {poolsLoading ? (
                  <Skeleton className="h-12 w-full" />
                ) : availablePools.length === 0 ? (
                  <div className="p-4 border-2 border-black bg-gray-100 text-sm text-gray-600 font-medium">
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
                                  <span className="text-gray-600 text-sm">({pool.cidr})</span>
                                )}
                              </div>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                )}
                <p className="text-xs text-gray-500">
                  Selecting a pool assigns the next available public IP automatically.
                </p>
              </div>

              {/* Specific IP within the pool (optional) */}
              <div className="space-y-2">
                <label htmlFor="ipAddress" className="text-sm font-bold uppercase tracking-wide">
                  Specific IP <span className="text-gray-500">(optional)</span>
                </label>
                <Input
                  id="ipAddress"
                  {...register("ipAddress")}
                  placeholder={watchedValues.ipPoolId ? "e.g., 192.168.1.100" : "Select an IP pool first"}
                  disabled={!watchedValues.ipPoolId}
                  className={`h-12 ${errors.ipAddress ? "border-danger ring-2 ring-danger/30" : ""} ${!watchedValues.ipPoolId ? "opacity-50 cursor-not-allowed" : ""}`}
                />
                {errors.ipAddress && (
                  <p className="text-sm font-medium text-danger flex items-center gap-1">
                    <AlertCircle className="w-4 h-4" />
                    {errors.ipAddress.message}
                  </p>
                )}
                <p className="text-xs text-gray-500">
                  Leave blank to auto-allocate the next free IP from the selected pool.
                </p>
              </div>

              {/* Bandwidth */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <label htmlFor="bandwidthMbps" className="text-sm font-bold uppercase tracking-wide">Bandwidth Limit</label>
                  <span className="text-2xl font-black">{watchedValues.bandwidthMbps} Mbps</span>
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
                        className="w-full h-4 appearance-none bg-white border-2 border-black cursor-pointer"
                        style={{
                          background: `linear-gradient(to right, #000000 0%, #000000 ${(value / 10000) * 100}%, #e5e5e5 ${(value / 10000) * 100}%, #e5e5e5 100%)`
                        }}
                      />
                    </div>
                  )}
                />
                <div className="flex justify-between text-xs text-gray-500">
                  <span>1 Mbps</span>
                  <span>10 Gbps</span>
                </div>
              </div>

              {/* VLAN ID (optional text input) */}
              <div className="space-y-2">
                <label htmlFor="vlanId" className="text-sm font-bold uppercase tracking-wide">
                  VLAN ID <span className="text-gray-600">(optional)</span>
                </label>
                <Input
                  id="vlanId"
                  {...register("vlanId")}
                  placeholder="e.g., 10, 20, 100"
                  className="h-12"
                />
                <p className="text-xs text-gray-500">
                  Enter a VLAN ID to assign this VM to a specific network segment
                </p>
              </div>

              {/* Network Preview */}
              <div className="bg-gray-100 border-2 border-black p-4">
                <div className="flex items-center gap-2 mb-2">
                  <Network className="w-5 h-5" />
                  <p className="text-xs font-bold uppercase">Network Settings Preview</p>
                </div>
                <div className="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <span className="text-gray-500">IP Pool:</span>
                    <span className="ml-2 font-bold">{getSelectedPool()?.name || "DHCP"}</span>
                  </div>
                  <div>
                    <span className="text-gray-500">IP Address:</span>
                    <span className="ml-2 font-bold">
                      {getSelectedPool() ? (watchedValues.ipAddress || "Auto (next free)") : "Auto (DHCP)"}
                    </span>
                  </div>
                  <div>
                    <span className="text-gray-500">Bandwidth:</span>
                    <span className="ml-2 font-bold">{watchedValues.bandwidthMbps} Mbps</span>
                  </div>
                  <div>
                    <span className="text-gray-500">VLAN:</span>
                    <span className="ml-2 font-bold">
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
                <div className="w-10 h-10 bg-success flex items-center justify-center border-2 border-black">
                  <CheckCircle2 className="w-5 h-5" />
                </div>
                <div>
                  <h2 className="text-xl font-black uppercase">Review & Create</h2>
                  <p className="text-sm text-gray-500">Confirm your VM configuration before creating</p>
                </div>
              </div>

              {/* Summary Cards */}
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {/* Basic Info */}
                <div className="bg-gray-100 border-2 border-black p-4">
                  <p className="text-xs font-bold uppercase mb-3 flex items-center gap-2">
                    <Server className="w-4 h-4" /> Basic Information
                  </p>
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between">
                      <span className="text-gray-500">Hostname:</span>
                      <span className="font-bold">{watchedValues.hostname}</span>
                    </div>
                    {getSelectedUser() && (
                      <div className="flex justify-between">
                        <span className="text-gray-500">Assigned To:</span>
                        <span className="font-bold">{getSelectedUser()?.email}</span>
                      </div>
                    )}
                    <div className="flex justify-between">
                      <span className="text-gray-500">Node:</span>
                      <span className="font-bold">{getSelectedNode()?.name || "Auto-select"}</span>
                    </div>
                  </div>
                </div>

                {/* Resources */}
                <div className="bg-gray-100 border-2 border-black p-4">
                  <p className="text-xs font-bold uppercase mb-3 flex items-center gap-2">
                    <Cpu className="w-4 h-4" /> Resources
                  </p>
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between">
                      <span className="text-gray-500">CPU:</span>
                      <span className="font-bold">{watchedValues.cpuCores} vCPU</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-gray-500">RAM:</span>
                      <span className="font-bold">{watchedValues.ramGB} GB</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-gray-500">Disk:</span>
                      <span className="font-bold">{watchedValues.diskGB} GB</span>
                    </div>
                  </div>
                </div>

                {/* OS Template */}
                <div className="bg-gray-100 border-2 border-black p-4">
                  <p className="text-xs font-bold uppercase mb-3 flex items-center gap-2">
                    <HardDrive className="w-4 h-4" /> OS Template
                  </p>
                  {getSelectedTemplate() && (
                    <div className="flex items-center gap-3">
                      <span className="text-3xl">{getTemplateIcon(getSelectedTemplate()!.name)}</span>
                      <div>
                        <p className="font-bold">{getSelectedTemplate()?.name}</p>
                        <p className="text-sm text-gray-500">v{getSelectedTemplate()?.version}</p>
                      </div>
                    </div>
                  )}
                </div>

                {/* Network */}
                <div className="bg-gray-100 border-2 border-black p-4">
                  <p className="text-xs font-bold uppercase mb-3 flex items-center gap-2">
                    <Network className="w-4 h-4" /> Network
                  </p>
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between">
                      <span className="text-gray-500">IP Pool:</span>
                      <span className="font-bold">{getSelectedPool()?.name || "DHCP"}</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-gray-500">IP Address:</span>
                      <span className="font-bold">
                        {getSelectedPool() ? (watchedValues.ipAddress || "Auto (next free)") : "Auto (DHCP)"}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-gray-500">Bandwidth:</span>
                      <span className="font-bold">{watchedValues.bandwidthMbps} Mbps</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="text-gray-500">VLAN:</span>
                      <span className="font-bold">{watchedValues.vlanId || "None"}</span>
                    </div>
                  </div>
                  <p className="text-[10px] text-gray-600 mt-2 italic">
                    Network settings are provisioned together with the VM
                  </p>
                </div>
              </div>

              {/* Error Message */}
              {submitError && (
                <div className="bg-danger/20 border-2 border-danger p-4 flex items-center gap-2">
                  <AlertCircle className="w-5 h-5 text-danger" />
                  <p className="text-sm font-bold text-danger">{submitError}</p>
                </div>
              )}
            </div>
          )}

          {/* Navigation Buttons */}
          <div className="flex items-center justify-between mt-8 pt-6 border-t-2 border-black">
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
                type="submit"
                disabled={createVM.isPending}
                className="gap-2 bg-success hover:bg-success/80"
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
