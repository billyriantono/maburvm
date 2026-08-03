"use client"

import { Suspense, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import Link from "next/link"
import { ArrowLeft, Check, Cpu, MemoryStick, HardDrive, Gauge, KeyRound, Database, Layers } from "lucide-react"
import { usePlans } from "@/lib/hooks/use-plans"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useImages } from "@/lib/hooks/use-images"
import { useCreateVM, type CreateVMResult } from "@/lib/hooks/use-vms"
import { Button } from "@/components/ui/button"
import { useSSHKeys } from "@/lib/hooks/use-ssh-keys"
import type { OSTemplate } from "@/types"
import type { Plan } from "@/types/plan"

// A client orders a VM by picking a plan (resource flavor) + an OS template and a
// hostname. Ownership is forced server-side to the caller, and quota is enforced,
// so this form can't be used to provision on behalf of another tenant or exceed
// the account's limits.
function OrderVMForm() {
  const router = useRouter()
  const searchParams = useSearchParams()
  // Deploy-from-image: when set, the OS template step is skipped — the server
  // derives the OS from the image and clones its disk.
  const sourceImageId = searchParams.get("source_image_id") ?? ""
  const { data: plans, isLoading: plansLoading } = usePlans(true)
  const { data: templates, isLoading: templatesLoading } = useTemplates()
  const { data: images } = useImages()
  const sourceImage = sourceImageId ? images?.find((img) => img.id === sourceImageId) : undefined
  const { data: sshKeys } = useSSHKeys()
  const createVM = useCreateVM()

  const [planId, setPlanId] = useState<string>("")
  const [templateId, setTemplateId] = useState<string>("")
  const [hostname, setHostname] = useState<string>("")
  const [error, setError] = useState<string>("")
  const [created, setCreated] = useState<CreateVMResult | null>(null)
  const [copied, setCopied] = useState(false)
  // SSH keys are opt-out: every saved key is injected unless the user unchecks it.
  const [excludedKeys, setExcludedKeys] = useState<Set<string>>(new Set())
  const selectedKeyIds = (sshKeys ?? []).filter((k) => !excludedKeys.has(k.id)).map((k) => k.id)

  const activePlans: Plan[] = (plans ?? []).filter((p) => p.is_active)
  // Only offer installable templates: active AND backed by a real base image.
  // The "/imported" image path is a placeholder for VMs imported from another
  // system — it can't seed a fresh install, so hide those from ordering.
  const activeTemplates: OSTemplate[] = (templates ?? []).filter(
    (t) => t.is_active && t.image_path !== "/imported"
  )
  const selectedPlan = activePlans.find((p) => p.id === planId)

  const hostnameValid = /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/.test(hostname)
  const canSubmit = !!planId && (!!templateId || !!sourceImageId) && hostnameValid && !createVM.isPending

  const handleSubmit = () => {
    setError("")
    if (!selectedPlan) {
      setError("Please select a plan.")
      return
    }
    createVM.mutate(
      {
        hostname,
        // From-image deploys omit the OS template — the backend derives it from
        // the image and clones the image's disk instead of a fresh install.
        os_template_id: sourceImageId ? undefined : templateId,
        source_image_id: sourceImageId || undefined,
        plan_id: planId,
        // Resources are re-derived from the plan server-side; we send the plan's
        // values so the request passes validation.
        resources: {
          cpu: selectedPlan.cpu,
          ram: selectedPlan.ram,
          disk: selectedPlan.disk,
        },
        // Do NOT send bandwidth_mbps/node/pool — clients may not set
        // infrastructure/network placement (server rejects it). The backend
        // derives bandwidth + placement from the plan automatically.
        ssh_key_ids: selectedKeyIds,
      },
      {
        onSuccess: (data: CreateVMResult) => {
          // If the server generated a root password (no SSH key / no password),
          // show it ONCE — otherwise the customer can never log in.
          if (data.root_password) {
            setCreated(data)
          } else {
            router.push("/client/vms")
          }
        },
        onError: (e: Error) => setError(e.message || "Failed to create VM"),
      }
    )
  }

  if (created) {
    return (
      <div className="space-y-6 max-w-2xl">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold tracking-tight">VM created</h1>
        </div>
        <div className="border rounded-lg bg-card p-6 space-y-5">
          <div>
            <p className="text-sm text-muted-foreground">Your VM <span className="font-semibold text-foreground">{created.hostname}</span> is being provisioned.</p>
          </div>
          <div className="border rounded-md bg-amber-50 dark:bg-amber-950/40 border-amber-200 dark:border-amber-900 p-4">
            <p className="text-sm font-semibold text-amber-800 dark:text-amber-300 mb-2">Save your root password now — it is shown only once.</p>
            <div className="flex items-center gap-2">
              <code className="flex-1 font-mono text-sm bg-background border rounded px-3 py-2 break-all">{created.root_password}</code>
              <Button variant="outline" size="sm" onClick={() => { navigator.clipboard.writeText(created.root_password || ""); setCopied(true); setTimeout(() => setCopied(false), 1500) }}>
                {copied ? <Check className="w-4 h-4" /> : "Copy"}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground mt-2">Log in as <span className="font-mono">root</span> with this password. You can change it later from the VM&apos;s console, or reset it from the VM page.</p>
          </div>
          <Button onClick={() => router.push("/client/vms")}>Go to My VMs</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-3">
        <Link href="/client/vms" className="p-2 rounded-md border bg-background hover:bg-muted transition-colors">
          <ArrowLeft className="w-5 h-5" />
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">Order a VM</h1>
      </div>

      {/* Step 1: plan */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">1 · Choose a plan</h2>
        </div>
        <div className="p-5">
          {plansLoading ? (
            <p className="text-muted-foreground">Loading plans…</p>
          ) : activePlans.length === 0 ? (
            <p className="text-muted-foreground">No plans are available. Please contact support.</p>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              {activePlans.map((p) => {
                const active = p.id === planId
                return (
                  <button
                    key={p.id}
                    onClick={() => setPlanId(p.id)}
                    className={`text-left p-4 rounded-md border transition-colors ${active ? "border-primary bg-primary/5 ring-1 ring-primary" : "bg-background hover:bg-muted/50"}`}
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-semibold">{p.name}</span>
                      {active && <Check className="w-5 h-5 text-primary" />}
                    </div>
                    <div className="mt-2 grid grid-cols-2 gap-1 text-xs text-muted-foreground">
                      <span className="flex items-center gap-1"><Cpu className="w-3 h-3" />{p.cpu} vCPU</span>
                      <span className="flex items-center gap-1"><MemoryStick className="w-3 h-3" />{p.ram} MB</span>
                      <span className="flex items-center gap-1"><HardDrive className="w-3 h-3" />{p.disk} GB</span>
                      <span className="flex items-center gap-1"><Gauge className="w-3 h-3" />{p.bandwidth_mbps} Mbps</span>
                      <span className="col-span-2 flex items-center gap-1"><Database className="w-3 h-3" />{p.data_quota_gb === 0 ? "Unlimited transfer" : p.data_quota_gb >= 1000 ? `${p.data_quota_gb / 1000} TB / mo transfer` : `${p.data_quota_gb} GB / mo transfer`}</span>
                    </div>
                  </button>
                )
              })}
            </div>
          )}
        </div>
      </section>

      {/* Step 2: OS (skipped when deploying from an image) */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">
            {sourceImageId ? "2 · Source image" : "2 · Choose an operating system"}
          </h2>
        </div>
        {sourceImageId ? (
          <div className="p-5">
            <div className="flex items-center gap-3 rounded-md border border-primary/50 bg-primary/5 p-4">
              <Layers className="w-5 h-5 text-primary shrink-0" />
              <div className="text-sm">
                <p className="font-medium">
                  Creating from image: <span className="font-semibold">{sourceImage?.name ?? sourceImageId}</span>
                </p>
                <p className="text-xs text-muted-foreground mt-0.5">
                  The new VM&apos;s disk is cloned from this image — no OS selection needed.{" "}
                  <Link href="/client/order" className="text-primary hover:underline">Start fresh instead</Link>
                </p>
              </div>
            </div>
          </div>
        ) : (
        <div className="p-5">
          {templatesLoading ? (
            <p className="text-muted-foreground">Loading templates…</p>
          ) : activeTemplates.length === 0 ? (
            <p className="text-muted-foreground">No OS templates are available. Please contact support.</p>
          ) : (
            <select
              value={templateId}
              onChange={(e) => setTemplateId(e.target.value)}
              className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
            >
              <option value="">Select an OS…</option>
              {activeTemplates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name} {t.version}
                </option>
              ))}
            </select>
          )}
        </div>
        )}
      </section>

      {/* Step 3: hostname */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">3 · Hostname</h2>
        </div>
        <div className="p-5">
          <input
            type="text"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            placeholder="my-server.example.com"
            className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
          {hostname !== "" && !hostnameValid && (
            <p className="text-sm text-destructive mt-2">
              Enter a valid hostname (letters, digits, hyphens, dots).
            </p>
          )}
        </div>
      </section>

      {/* Step 4: SSH keys */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b flex items-center gap-2">
          <KeyRound className="w-5 h-5 text-muted-foreground" />
          <h2 className="text-lg font-semibold">4 · SSH Keys</h2>
        </div>
        <div className="p-5">
          {!sshKeys?.length ? (
            <p className="text-sm text-muted-foreground">
              No SSH keys saved. The VM will use root-password login only. Add keys under{" "}
              <Link href="/client/settings/ssh-keys" className="text-primary hover:underline font-medium">Settings → SSH Keys</Link>.
            </p>
          ) : (
            <div className="space-y-2">
              {sshKeys.map((k) => (
                <label key={k.id} className="flex items-center gap-3 p-3 rounded-md border cursor-pointer hover:bg-muted/50 transition-colors">
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
              <p className="text-xs text-muted-foreground">Checked keys are injected into the new VM&apos;s root account.</p>
            </div>
          )}
        </div>
      </section>

      {error && (
        <div className="rounded-md border border-destructive/50 bg-destructive/10 text-destructive px-5 py-4 text-sm font-medium">
          {error}
        </div>
      )}

      <button
        onClick={handleSubmit}
        disabled={!canSubmit}
        className="w-full h-11 rounded-md bg-primary text-primary-foreground font-medium shadow-sm hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {createVM.isPending ? "Creating…" : "Create VM"}
      </button>
    </div>
  )
}

// useSearchParams requires a Suspense boundary in Next 15 client pages.
export default function OrderVMPage() {
  return (
    <Suspense fallback={null}>
      <OrderVMForm />
    </Suspense>
  )
}
