"use client"

import { Suspense, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import Link from "next/link"
import { toast } from "sonner"
import { ArrowLeft, Check, Copy, Loader2, HardDrive, Gauge, Database, Layers } from "lucide-react"
import { usePlans } from "@/lib/hooks/use-plans"
import { useVPCs } from "@/lib/hooks/use-vpcs"
import { useRegions } from "@/lib/hooks/use-regions"
import { CountryFlag } from "@/components/country-flag"
import { useTemplates } from "@/lib/hooks/use-templates"
import { useImages } from "@/lib/hooks/use-images"
import { useCreateVM, type CreateVMResult } from "@/lib/hooks/use-vms"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { OSPicker } from "@/components/os-picker"
import { useCopy } from "@/lib/use-feedback"
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
  const { data: vpcs } = useVPCs()
  const { data: regions } = useRegions()

  const { data: templates, isLoading: templatesLoading } = useTemplates()
  const { data: images } = useImages()
  const sourceImage = sourceImageId ? images?.find((img) => img.id === sourceImageId) : undefined
  const { data: sshKeys } = useSSHKeys()
  const createVM = useCreateVM()

  const [planId, setPlanId] = useState<string>("")
  const [templateId, setTemplateId] = useState<string>("")
  const [hostname, setHostname] = useState<string>("")
  const [vpcId, setVpcId] = useState<string>("")
  const [region, setRegion] = useState<string>("")

  // Derived AFTER the state it reads: declaring these above `region` put the
  // component in the temporal dead zone and the page crashed on first render
  // with "Cannot access 'region' before initialization".
  const selectedRegion = (regions ?? []).find((r) => r.slug === region)
  // A private network lives on one node, so it belongs to exactly one region and
  // cannot be joined from another; offering one from elsewhere would fail at
  // submit through no fault of the customer.
  const regionVPCs = (vpcs ?? []).filter(
    (v) => !!selectedRegion && v.region_id === selectedRegion.id,
  )
  const [error, setError] = useState<string>("")
  const [created, setCreated] = useState<CreateVMResult | null>(null)
  const { copy, copied } = useCopy()
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
  // Region is required: it is a physical location with real latency
  // consequences, so the platform must not choose one silently.
  const canSubmit =
    !!planId && (!!templateId || !!sourceImageId) && hostnameValid && !!region && !createVM.isPending

  const handleSubmit = () => {
    setError("")
    if (!selectedPlan) {
      setError("Please select a plan.")
      return
    }
    if (!region) {
      setError("Please choose a region.")
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
        // Placing the VM in one of the customer's own private networks. Ownership
        // is verified server-side, and the network also decides which node the VM
        // lands on, since a private network lives on one host.
        vpc_id: vpcId || undefined,
        region,
      },
      {
        onSuccess: (data: CreateVMResult) => {
          // The server always generates a root password, with or without SSH
          // keys. Show it ONCE: it is the way in when the key is not at hand.
          if (data.root_password) {
            toast.success("Your VM is being created")
            setCreated(data)
          } else {
            toast.success("Your VM is being created")
            router.push("/client/vms")
          }
        },
        onError: (e: Error) => {
          const msg = e.message || "Failed to create VM"
          setError(msg)
          toast.error(msg)
        },
      }
    )
  }

  if (created) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold tracking-tight">VM created</h1>
        </div>
        <div className="border rounded-lg bg-card p-6 space-y-5">
          <div>
            <p className="text-sm text-muted-foreground">Your VM <span className="font-semibold text-foreground">{created.hostname}</span> is being provisioned.</p>
          </div>
          <div className="border rounded-md bg-amber-50 dark:bg-amber-950/40 border-amber-200 dark:border-amber-900 p-4">
            <p className="text-sm font-semibold text-amber-800 dark:text-amber-300 mb-2">Save your root password now. It is shown only once.</p>
            <div className="flex items-center gap-2">
              <code className="flex-1 font-mono text-sm bg-background border rounded px-3 py-2 break-all">{created.root_password}</code>
              <Button variant="outline" size="sm" aria-label="Copy root password" onClick={() => copy(created.root_password || "", "root-password")}>
                {copied("root-password") ? <Check className="w-4 h-4" aria-hidden="true" /> : <Copy className="w-4 h-4" aria-hidden="true" />}
                {copied("root-password") ? "Copied" : "Copy"}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground mt-2">Log in as <span className="font-mono">root</span> with this password, on the console or over SSH when your key is not at hand. You can change it later from inside the VM.</p>
          </div>
          <Button onClick={() => router.push("/client/vms")}>Go to My VMs</Button>
        </div>
      </div>
    )
  }

  const selectedTemplate = activeTemplates.find((t) => t.id === templateId)
  const selectedVPC = regionVPCs.find((v) => v.id === vpcId)
  const osDone = !!templateId || !!sourceImageId
  // What still stands between the customer and the button, in page order.
  const missing = [
    !region && "a location",
    !planId && "a plan",
    !osDone && "an operating system",
    !hostnameValid && "a hostname",
  ].filter(Boolean) as string[]

  return (
    <div>
      <div className="mb-8 flex items-center gap-3">
        <Link
          href="/client/vms"
          aria-label="Back to my VMs"
          className="rounded-md border bg-background p-2 transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ArrowLeft className="h-5 w-5" aria-hidden="true" />
        </Link>
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Order a VM</h1>
          <p className="text-sm text-muted-foreground">Four choices, then it builds in a few minutes.</p>
        </div>
      </div>

      <div className="lg:grid lg:grid-cols-[minmax(0,1fr)_20rem] lg:items-start lg:gap-10">
        <div>
          <Step n={1} title="Location" done={!!region} hint="Where your VM runs. Pick the one closest to your users. A VM cannot be moved to another location later.">
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {(regions ?? []).map((r) => {
                const selected = region === r.slug
                return (
                  <button
                    key={r.id}
                    type="button"
                    aria-pressed={selected}
                    onClick={() => {
                      setRegion(r.slug)
                      setVpcId("")
                    }}
                    className={choiceClass(selected, "flex items-center gap-3 p-4")}
                  >
                    <CountryFlag country={r.country} className="shrink-0 rounded-sm text-2xl" />
                    <span className="min-w-0">
                      <span className="block truncate text-sm font-semibold">{r.name}</span>
                      <span className="block truncate text-xs text-muted-foreground">{r.slug}</span>
                    </span>
                    <SelectedMark show={selected} className="ml-auto" />
                  </button>
                )
              })}
            </div>
            {!regions?.length && <p className="text-sm text-muted-foreground">No location is available to order right now.</p>}
          </Step>

          <Step n={2} title="Plan" done={!!planId} hint="The size of the VM. Network speed and monthly transfer come with the plan.">
            {plansLoading ? (
              <ChoiceSkeleton />
            ) : activePlans.length === 0 ? (
              <p className="text-sm text-muted-foreground">No plans are available. Please contact support.</p>
            ) : (
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {activePlans.map((p) => {
                  const selected = p.id === planId
                  return (
                    <button
                      key={p.id}
                      type="button"
                      aria-pressed={selected}
                      onClick={() => setPlanId(p.id)}
                      className={choiceClass(selected, "p-4")}
                    >
                      <span className="flex items-start justify-between gap-2">
                        <span className="text-sm font-semibold">{p.name}</span>
                        <SelectedMark show={selected} />
                      </span>
                      <span className="mt-3 block text-xl font-semibold tracking-tight">
                        {p.cpu} vCPU <span className="text-muted-foreground">/</span> {formatRAM(p.ram)}
                      </span>
                      <span className="mt-3 block space-y-1.5 border-t pt-3 text-xs text-muted-foreground">
                        <span className="flex items-center gap-2"><HardDrive className="h-3.5 w-3.5" aria-hidden="true" />{p.disk} GB disk</span>
                        <span className="flex items-center gap-2"><Gauge className="h-3.5 w-3.5" aria-hidden="true" />{formatSpeed(p.bandwidth_mbps)}</span>
                        <span className="flex items-center gap-2"><Database className="h-3.5 w-3.5" aria-hidden="true" />{formatTransfer(p.data_quota_gb)}</span>
                      </span>
                    </button>
                  )
                })}
              </div>
            )}
          </Step>

          <Step
            n={3}
            title={sourceImageId ? "Source image" : "Operating system"}
            titleId="order-os-title"
            done={osDone}
            hint={sourceImageId ? undefined : "Installed fresh on the VM. Pick a version inside the card."}
          >
            {sourceImageId ? (
              <div className="flex items-center gap-3 rounded-lg border border-primary bg-card p-4">
                <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-muted">
                  <Layers className="h-5 w-5" aria-hidden="true" />
                </span>
                <div className="min-w-0 text-sm">
                  <p className="truncate font-semibold">{sourceImage?.name ?? sourceImageId}</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    The new VM starts as a copy of this image.{" "}
                    <Link href="/client/order" className="font-medium text-foreground underline underline-offset-4">Install a fresh system instead</Link>
                  </p>
                </div>
              </div>
            ) : templatesLoading ? (
              <ChoiceSkeleton />
            ) : activeTemplates.length === 0 ? (
              <p className="text-sm text-muted-foreground">No operating systems are available. Please contact support.</p>
            ) : (
              <OSPicker templates={activeTemplates} value={templateId} onChange={setTemplateId} labelledBy="order-os-title" />
            )}
          </Step>

          <Step n={4} title="Hostname" done={hostnameValid} hint="A name for the VM. You can rename it later.">
            <label htmlFor="order-hostname" className="sr-only">Hostname</label>
            <Input
              id="order-hostname"
              value={hostname}
              onChange={(e) => setHostname(e.target.value)}
              placeholder="web-01.example.com"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={hostname !== "" && !hostnameValid}
              aria-describedby="order-hostname-help"
              className="h-11 max-w-md font-mono text-sm"
            />
            <p
              id="order-hostname-help"
              className={`mt-2 text-xs ${hostname !== "" && !hostnameValid ? "font-medium text-destructive" : "text-muted-foreground"}`}
            >
              Letters, digits, hyphens and dots.
            </p>
          </Step>

          {/* Only the networks in the chosen location: a private network lives in
              one place and cannot be joined from another. */}
          {!!regionVPCs.length && (
            <Step title="Network" optional hint="Leave it on a public address, or put it in one of your private networks." className="animate-in fade-in slide-in-from-top-2 duration-200">
              <div className="grid gap-3 sm:grid-cols-2">
                <button type="button" aria-pressed={vpcId === ""} onClick={() => setVpcId("")} className={choiceClass(vpcId === "", "flex items-start gap-3 p-4")}>
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold">Public address</span>
                    <span className="mt-0.5 block text-xs text-muted-foreground">Reachable from the internet directly.</span>
                  </span>
                  <SelectedMark show={vpcId === ""} className="ml-auto" />
                </button>
                {regionVPCs.map((v) => (
                  <button key={v.id} type="button" aria-pressed={vpcId === v.id} onClick={() => setVpcId(v.id)} className={choiceClass(vpcId === v.id, "flex items-start gap-3 p-4")}>
                    <span className="min-w-0">
                      <span className="block truncate text-sm font-semibold">{v.name}</span>
                      <span className="mt-0.5 block font-mono text-xs text-muted-foreground">{v.subnet}</span>
                      <span className="mt-1 block text-xs text-muted-foreground">Private. Attach a floating IP to expose it.</span>
                    </span>
                    <SelectedMark show={vpcId === v.id} className="ml-auto" />
                  </button>
                ))}
              </div>
            </Step>
          )}

          <Step title="SSH keys" optional last hint="Keys ticked here are installed for root. A root password is also generated and shown once, so you can still get in without the key.">
            {!sshKeys?.length ? (
              <p className="text-sm text-muted-foreground">
                No keys saved yet. You will log in with the root password.{" "}
                <Link href="/client/settings/ssh-keys" className="font-medium text-foreground underline underline-offset-4">Add an SSH key</Link>
              </p>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2">
                {sshKeys.map((k) => (
                  <label
                    key={k.id}
                    className="flex min-h-11 cursor-pointer items-center gap-3 rounded-lg border bg-card px-3 py-2 transition-colors hover:border-foreground/30"
                  >
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
                      className="h-4 w-4 shrink-0 accent-[hsl(var(--primary))]"
                    />
                    <span className="min-w-0">
                      <span className="block truncate text-sm font-medium">{k.name}</span>
                      <span className="block truncate font-mono text-[11px] text-muted-foreground">{k.fingerprint}</span>
                    </span>
                  </label>
                ))}
              </div>
            )}
          </Step>
        </div>

        {/* The order as it stands, beside the steps on a wide screen and after
            them on a narrow one, so nothing is committed unseen. */}
        <aside className="mt-2 lg:sticky lg:top-24 lg:mt-0" aria-label="Order summary">
          <div className="rounded-lg border bg-card shadow-sm">
            <h2 className="border-b px-5 py-4 text-sm font-semibold">Your VM</h2>
            <dl className="divide-y text-sm">
              <SummaryRow label="Location" value={selectedRegion?.name} />
              <SummaryRow
                label="Plan"
                value={selectedPlan?.name}
                detail={selectedPlan && `${selectedPlan.cpu} vCPU, ${formatRAM(selectedPlan.ram)}, ${selectedPlan.disk} GB disk`}
              />
              <SummaryRow
                label={sourceImageId ? "Image" : "System"}
                value={sourceImageId ? (sourceImage?.name ?? "Saved image") : selectedTemplate && templateLabel(selectedTemplate.name, selectedTemplate.version)}
              />
              <SummaryRow label="Hostname" value={hostnameValid ? hostname : undefined} mono />
              {!!regionVPCs.length && <SummaryRow label="Network" value={selectedVPC ? selectedVPC.name : "Public address"} />}
              <SummaryRow
                label="Login"
                value={selectedKeyIds.length ? `${selectedKeyIds.length} SSH ${selectedKeyIds.length === 1 ? "key" : "keys"} and a password` : "Root password"}
              />
            </dl>
            <div className="border-t p-5">
              {error && (
                <p role="alert" className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm font-medium text-destructive">
                  {error}
                </p>
              )}
              <Button size="lg" className="h-11 w-full" onClick={handleSubmit} disabled={!canSubmit}>
                {createVM.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
                {createVM.isPending ? "Creating" : "Create VM"}
              </Button>
              <p className="mt-3 text-xs text-muted-foreground" aria-live="polite">
                {missing.length
                  ? `Still needed: ${missing.join(", ")}.`
                  : "Ready. The root password is shown once on the next screen."}
              </p>
            </div>
          </div>
        </aside>
      </div>
    </div>
  )
}

// A step in the order. The marker fills in with a check once its choice is
// made, so the page shows how far along the order is without a progress bar.
function Step({
  n,
  title,
  titleId,
  hint,
  done,
  optional,
  last,
  className,
  children,
}: {
  n?: number
  title: string
  titleId?: string
  hint?: string
  done?: boolean
  optional?: boolean
  last?: boolean
  className?: string
  children: React.ReactNode
}) {
  return (
    <section className={`relative pl-11 sm:pl-14 ${last ? "pb-8" : "pb-10"} ${className ?? ""}`}>
      {!last && <span aria-hidden="true" className="absolute bottom-0 left-[15px] top-9 w-px bg-border sm:left-[19px]" />}
      <span
        aria-hidden="true"
        className={`absolute left-0 top-0 flex h-8 w-8 items-center justify-center rounded-full border text-sm font-semibold transition-colors duration-300 sm:h-10 sm:w-10 ${
          done ? "border-primary bg-primary text-primary-foreground" : "bg-background text-muted-foreground"
        }`}
      >
        {done ? <Check className="h-4 w-4 animate-in zoom-in-50 duration-200" /> : (n ?? <span className="h-1.5 w-1.5 rounded-full bg-muted-foreground/60" />)}
      </span>
      <div className="mb-4 sm:pt-1.5">
        <h2 id={titleId} className="text-base font-semibold leading-8 sm:leading-7">
          {title}
          {optional && <span className="ml-2 text-xs font-normal text-muted-foreground">Optional</span>}
        </h2>
        {hint && <p className="mt-0.5 max-w-prose text-sm text-muted-foreground">{hint}</p>}
      </div>
      {children}
    </section>
  )
}

function choiceClass(selected: boolean, extra: string) {
  return `w-full rounded-lg border bg-card text-left transition-[border-color,box-shadow,transform] duration-150 active:scale-[0.98] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 ${
    selected ? "border-primary shadow-[0_0_0_1px_hsl(var(--primary))]" : "hover:border-foreground/30"
  } ${extra}`
}

function SelectedMark({ show, className = "" }: { show: boolean; className?: string }) {
  if (!show) return null
  return (
    <span className={`flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground animate-in zoom-in-50 duration-200 ${className}`}>
      <Check className="h-3 w-3" aria-hidden="true" />
    </span>
  )
}

function ChoiceSkeleton() {
  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true">
      {[0, 1, 2].map((i) => (
        <div key={i} className="h-28 animate-pulse rounded-lg bg-muted" />
      ))}
    </div>
  )
}

function SummaryRow({ label, value, detail, mono }: { label: string; value?: string | false; detail?: string | false; mono?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-4 px-5 py-3">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right">
        {value ? (
          <>
            <span className={`block truncate font-medium ${mono ? "font-mono text-xs" : ""}`}>{value}</span>
            {detail && <span className="block text-xs text-muted-foreground">{detail}</span>}
          </>
        ) : (
          <span className="text-muted-foreground">Not chosen</span>
        )}
      </dd>
    </div>
  )
}

function formatRAM(mb: number) {
  return mb >= 1024 ? `${+(mb / 1024).toFixed(1)} GB RAM` : `${mb} MB RAM`
}
function formatSpeed(mbps: number) {
  if (!mbps) return "Unmetered speed"
  return mbps >= 1000 ? `${+(mbps / 1000).toFixed(1)} Gbps network` : `${mbps} Mbps network`
}
function formatTransfer(gb: number) {
  if (gb === 0) return "Unlimited transfer"
  return gb >= 1000 ? `${+(gb / 1000).toFixed(1)} TB transfer a month` : `${gb} GB transfer a month`
}

// useSearchParams requires a Suspense boundary in Next 15 client pages.
// Template names usually already carry the version ("Debian 12"), so appending
// it produced "Debian 12 12". Only append when the name does not already end
// with it, which keeps names that genuinely lack a version readable.
function templateLabel(name: string, version?: string): string {
  const n = (name ?? "").trim()
  const v = (version ?? "").trim()
  if (!v || n.toLowerCase().endsWith(v.toLowerCase())) return n
  return `${n} ${v}`
}

export default function OrderVMPage() {
  return (
    <Suspense fallback={<p className="p-8 text-center text-muted-foreground">Loading…</p>}>
      <OrderVMForm />
    </Suspense>
  )
}
