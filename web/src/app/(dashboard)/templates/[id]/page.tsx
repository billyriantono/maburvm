"use client"

import { useState, useCallback } from "react"
import Link from "next/link"
import { useParams, useRouter } from "next/navigation"
import { 
  ArrowLeft, 
  FileArchive,
  HardDrive,
  Trash2,
  Loader2,
  Server,
  Monitor,
  AlertTriangle,
  AlertCircle,
  CheckCircle2,
  XCircle,
  Calendar,
  Edit2,
} from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { useTemplate, useDeleteTemplate, useUpdateTemplate, useTemplates, useRefreshTemplate } from "@/lib/hooks/use-templates"
import { ImageIntegrityFields, emptyIntegrity, integrityProblem, type ImageIntegrity } from "@/components/image-integrity-fields"
import { useVMs } from "@/lib/hooks/use-vms"
import { usePlans } from "@/lib/hooks/use-plans"
import { OSIcon } from "@/components/os-icon"
import type { VM } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" })
}

export default function TemplateDetailPage() {
  const confirm = useConfirm()
  const params = useParams()
  const router = useRouter()
  const templateId = params.id as string

  const [editOpen, setEditOpen] = useState(false)
  const [editName, setEditName] = useState("")
  const [editVersion, setEditVersion] = useState("")
  const [editDescription, setEditDescription] = useState("")
  const [editLicensed, setEditLicensed] = useState(false)
  const [editPlanIds, setEditPlanIds] = useState<string[]>([])
  const [editIntegrity, setEditIntegrity] = useState<ImageIntegrity>(emptyIntegrity)

  // Data hooks
  const { data: template, isLoading, error, refetch } = useTemplate(templateId)
  const { data: vmsData, isLoading: vmsLoading } = useVMs({ pageSize: 100 })
  const deleteTemplate = useDeleteTemplate()
  const updateTemplate = useUpdateTemplate(templateId)
  const { data: plans } = usePlans()
  // Where each node stands with the image, and the checksum of its copy.
  const { data: withNodes, refetch: refetchNodes } = useTemplates({ includeNodes: true })
  const nodes = withNodes?.find((t) => t.id === templateId)?.nodes ?? []
  const refreshTemplate = useRefreshTemplate()

  const handleRefresh = useCallback(async (force: boolean) => {
    try {
      const res = await refreshTemplate.mutateAsync({ id: templateId, force })
      if (res.changed) toast.success("The image changed. Every node is fetching the new one; VMs built from the old one are not touched")
      else if (force) toast.success("Every node is fetching the image")
      else toast.success("The image has not changed")
      refetch(); refetchNodes()
    } catch (err) {
      toast.error(`Refresh failed: ${(err as Error).message}`)
    }
  }, [refreshTemplate, templateId, refetch, refetchNodes])

  // Filter VMs that use this template
  const templateVMs = (vmsData?.data || []).filter((vm: VM) => vm.os_template_id === templateId)

  // Delete handler
  const handleDelete = useCallback(async () => {
    const ok = await confirm({
      title: `Delete template "${template?.name ?? ""}"?`,
      description:
        templateVMs.length > 0
          ? `${templateVMs.length} VM(s) were built from this template. They keep running, but the template is gone for new builds.`
          : "New VMs can no longer be built from it. This cannot be undone.",
      confirmLabel: "Delete template",
      destructive: true,
      details: templateVMs.length > 0 ? [{ label: "VMs from it", value: templateVMs.length }] : undefined,
      action: () => deleteTemplate.mutateAsync(templateId),
    })
    if (!ok) return
    toast.success("Template deleted")
    setTimeout(() => router.push("/templates"), 1000)
  }, [confirm, template, templateVMs.length, deleteTemplate, templateId, router])

  // Edit handler
  const handleEdit = useCallback(async () => {
    const problem = integrityProblem(template?.image_path ?? "", editIntegrity, false)
    if (problem) {
      toast.error(problem)
      return
    }
    try {
      await updateTemplate.mutateAsync({
        name: editName,
        version: editVersion,
        description: editDescription || undefined,
        licensed: editLicensed,
        plan_ids: editPlanIds,
        checksum_sha256: editIntegrity.checksumSha256.trim(),
        checksum_url: editIntegrity.checksumUrl.trim(),
        allow_http: editIntegrity.allowHttp,
        auto_refresh: editIntegrity.autoRefresh,
        firmware: editIntegrity.firmware,
        secure_boot: editIntegrity.firmware === "uefi" && !!editIntegrity.secureBoot,
        tpm: !!editIntegrity.tpm,
      })
      toast.success("Template updated")
      setEditOpen(false)
      refetch()
    } catch (err) {
      toast.error(`Failed to update: ${(err as Error).message}`)
    }
  }, [updateTemplate, editName, editVersion, editDescription, editLicensed, editPlanIds, editIntegrity, template, refetch])

  // Loading
  if (isLoading) {
    return (
      <div className="max-w-5xl mx-auto">
        <div className="flex items-center gap-4 mb-6">
          <Link href="/templates"><Button variant="outline" size="icon"><ArrowLeft className="w-4 h-4" /></Button></Link>
          <Skeleton className="h-12 w-64" />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <Skeleton className="h-64 rounded-lg" />
          <Skeleton className="h-64 rounded-lg" />
        </div>
      </div>
    )
  }

  // Error / not found
  if (error || !template) {
    return (
      <div className="max-w-5xl mx-auto">
        <div className="bg-card text-card-foreground border rounded-lg p-12 shadow-sm text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" />
          <h2 className="text-lg font-semibold mb-2">Template Not Found</h2>
          <p className="text-muted-foreground text-sm mb-6">{(error as Error)?.message || "The requested template does not exist."}</p>
          <Link href="/templates"><Button className="gap-2"><ArrowLeft className="w-4 h-4" />Back to Templates</Button></Link>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/templates">
          <Button variant="outline" size="icon"><ArrowLeft className="w-4 h-4" /></Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <div className="w-12 h-12 bg-muted rounded-md flex items-center justify-center border">
              <OSIcon name={template.name} className="w-7 h-7" />
            </div>
            <div>
              <h1 className="text-2xl font-semibold tracking-tight">{template.name}</h1>
              <p className="text-muted-foreground text-sm">v{template.version}</p>
            </div>
          </div>
        </div>
        <div className="flex gap-2">
          <Dialog open={editOpen} onOpenChange={(open) => {
            setEditOpen(open)
            if (open) {
              setEditName(template.name); setEditVersion(template.version); setEditDescription(template.description || "")
              setEditLicensed(!!template.licensed); setEditPlanIds(template.plan_ids ?? [])
              setEditIntegrity({ checksumSha256: template.checksum_sha256 ?? "", checksumUrl: template.checksum_url ?? "", skipChecksum: false,
                allowHttp: !!template.allow_http, autoRefresh: !!template.auto_refresh, firmware: template.firmware ?? "bios",
                secureBoot: !!template.secure_boot, tpm: !!template.tpm })
            }
          }}>
            <DialogTrigger asChild>
              <Button variant="outline" className="gap-2"><Edit2 className="w-4 h-4" />Edit</Button>
            </DialogTrigger>
            <DialogContent className="max-w-md max-h-[90vh] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>Edit Template</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div>
                  <label htmlFor="edit-name" className="block text-xs font-medium text-muted-foreground mb-1">Name</label>
                  <Input id="edit-name" value={editName} onChange={(e) => setEditName(e.target.value)} />
                </div>
                <div>
                  <label htmlFor="edit-version" className="block text-xs font-medium text-muted-foreground mb-1">Version</label>
                  <Input id="edit-version" value={editVersion} onChange={(e) => setEditVersion(e.target.value)} />
                </div>
                <div>
                  <label htmlFor="edit-desc" className="block text-xs font-medium text-muted-foreground mb-1">Description</label>
                  <Input id="edit-desc" value={editDescription} onChange={(e) => setEditDescription(e.target.value)} />
                </div>
                <fieldset className="border-t pt-4">
                  <legend className="sr-only">Who can install it</legend>
                  <label className="flex items-start gap-3 cursor-pointer">
                    <input type="checkbox" checked={editLicensed} onChange={(e) => setEditLicensed(e.target.checked)} className="mt-0.5 w-4 h-4 accent-primary" />
                    <span>
                      <span className="block text-sm font-medium">Needs a paid licence</span>
                      <span className="block text-xs text-muted-foreground">
                        Customers can install it only on the plans ticked below. With none ticked, only staff and orders from your billing system can.
                      </span>
                    </span>
                  </label>
                  <p className="mt-4 mb-2 text-xs font-medium text-muted-foreground">Offered on these plans</p>
                  {!plans ? (
                    <p className="text-sm text-muted-foreground">Loading plans</p>
                  ) : plans.length === 0 ? (
                    <p className="text-sm text-muted-foreground">There are no plans yet.</p>
                  ) : (
                    <div className="max-h-40 space-y-1.5 overflow-y-auto">
                      {plans.map((p) => (
                        <label key={p.id} className="flex items-center gap-3 cursor-pointer text-sm">
                          <input
                            type="checkbox"
                            className="w-4 h-4 accent-primary"
                            checked={editPlanIds.includes(p.id)}
                            onChange={(e) => setEditPlanIds(e.target.checked ? [...editPlanIds, p.id] : editPlanIds.filter((id) => id !== p.id))}
                          />
                          {p.name}{!p.is_active && <span className="text-muted-foreground">(not on sale)</span>}
                        </label>
                      ))}
                    </div>
                  )}
                  <p className="mt-2 text-xs text-muted-foreground">
                    {editPlanIds.length > 0
                      ? "Customers and billing orders on any other plan are refused this template."
                      : editLicensed
                        ? "No plan ticked: customers cannot pick it themselves."
                        : "No plan ticked: every plan can use it."}
                  </p>
                </fieldset>
                <fieldset className="border-t pt-4">
                  <legend className="mb-3 text-sm font-medium">Checksum</legend>
                  <ImageIntegrityFields value={editIntegrity} onChange={setEditIntegrity} idPrefix="edit" allowSkip={false} />
                  <p className="mt-2 text-xs text-muted-foreground">A new checksum applies to the next download on each node; use Refresh on nodes to fetch it now.</p>
                </fieldset>
              </div>
              <DialogFooter>
                <Button variant="ghost" onClick={() => setEditOpen(false)}>Cancel</Button>
                <Button onClick={handleEdit} disabled={updateTemplate.isPending || !editName.trim()}>
                  {updateTemplate.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Save
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          <Button variant="destructive" onClick={() => handleDelete()} className="gap-2"><Trash2 className="w-4 h-4" />Delete</Button>
        </div>
      </div>

      {/* Template Info */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6 mb-6">
        {/* Details Card */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
          <h2 className="text-base font-semibold mb-4 flex items-center gap-2"><FileArchive className="w-5 h-5" />Template Details</h2>
          <div className="space-y-1">
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Name</span>
              <span className="font-medium">{template.name}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Version</span>
              <span className="font-medium">v{template.version}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Status</span>
              {template.is_active ? (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900"><CheckCircle2 className="w-3 h-3" />Active</span>
              ) : (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-muted text-muted-foreground"><XCircle className="w-3 h-3" />Inactive</span>
              )}
            </div>
            <div className="flex items-center justify-between gap-4 py-2 border-b">
              <span className="text-sm text-muted-foreground">Customers can install it</span>
              <span className="text-right text-sm font-medium">
                {(template.plan_ids ?? []).length > 0
                  ? `On ${(template.plan_ids ?? []).map((id) => plans?.find((p) => p.id === id)?.name ?? "a deleted plan").join(", ")}`
                  : template.licensed ? "No, staff and billing orders only" : "On every plan"}
                {template.licensed && <span className="block text-xs font-normal text-muted-foreground">Needs a paid licence</span>}
              </span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Image Path</span>
              <span className="font-mono text-xs break-all text-right">{template.image_path}</span>
            </div>
            <div className="flex items-center justify-between gap-4 py-2 border-b">
              <span className="text-sm text-muted-foreground">sha256</span>
              {template.checksum_sha256 ? (
                <span className="font-mono text-xs break-all text-right">{template.checksum_sha256}</span>
              ) : (
                <span className="text-right text-sm font-medium text-amber-700 dark:text-amber-300">None: nodes cannot check the download</span>
              )}
            </div>
            {template.checksum_url && (
              <div className="flex items-center justify-between gap-4 py-2 border-b">
                <span className="text-sm text-muted-foreground">Checksum list</span>
                <span className="text-right text-xs break-all">
                  <span className="font-mono">{template.checksum_url}</span>
                  <span className="block text-muted-foreground">{template.auto_refresh ? "Checked every day" : "Checked when refreshed"}{template.checksum_checked_at ? `, last ${formatDate(template.checksum_checked_at)}` : ""}</span>
                </span>
              </div>
            )}
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Kind and boot</span>
              <span className="font-medium text-sm">
                {template.kind === "iso" ? "ISO" : "Disk image"}, {template.firmware === "uefi" ? (template.secure_boot ? "UEFI with Secure Boot" : "UEFI") : "BIOS"}
                {template.tpm ? ", virtual TPM" : ""}
              </span>
            </div>
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Created</span>
              <span className="font-medium">{formatDate(template.created_at)}</span>
            </div>
            <div className="flex items-center justify-between py-2">
              <span className="text-sm text-muted-foreground">Updated</span>
              <span className="font-medium">{formatDate(template.updated_at)}</span>
            </div>
          </div>
          {template.description && (
            <div className="mt-6 pt-4 border-t">
              <h3 className="text-xs font-medium text-muted-foreground mb-2">Description</h3>
              <p className="text-sm">{template.description}</p>
            </div>
          )}
        </div>

        {/* On the nodes */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm md:col-span-2">
          <div className="flex flex-wrap items-center justify-between gap-3 mb-4">
            <h2 className="text-base font-semibold flex items-center gap-2"><Server className="w-5 h-5" />On the nodes</h2>
            <div className="flex gap-2">
              {template.checksum_url && (
                <Button variant="outline" size="sm" onClick={() => handleRefresh(false)} disabled={refreshTemplate.isPending}>
                  {refreshTemplate.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Check for a new image
                </Button>
              )}
              <Button variant="outline" size="sm" onClick={() => handleRefresh(true)} disabled={refreshTemplate.isPending}>Refresh on nodes</Button>
            </div>
          </div>
          {nodes.length === 0 ? (
            <p className="text-sm text-muted-foreground">No node has been asked for this image yet. Each node fetches it the first time a VM is built from it, or now with Refresh on nodes.</p>
          ) : (
            <div className="divide-y">
              {nodes.map((n) => {
                const stale = n.status === "available" && !!template.checksum_sha256 && !!n.checksum && n.checksum !== template.checksum_sha256
                return (
                  <div key={n.node_id} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
                    <span className="font-medium">{n.node_name}</span>
                    <span className="text-right">
                      {n.status === "available" ? (stale ? "Has an older copy" : "Ready") : n.status === "error" ? "Failed" : n.status === "syncing" ? "Fetching" : "Not fetched yet"}
                      {n.status === "error" && n.error_msg && <span className="block text-xs text-muted-foreground max-w-md">{n.error_msg}</span>}
                    </span>
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {/* VMs Using Template */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm">
          <h2 className="text-base font-semibold mb-4 flex items-center gap-2"><Monitor className="w-5 h-5" />VMs Using This Template ({templateVMs.length})</h2>
          {vmsLoading ? (
            <div className="space-y-3">
              {[1,2,3].map(i => <Skeleton key={i} className="h-14 w-full" />)}
            </div>
          ) : templateVMs.length === 0 ? (
            <div className="p-8 text-center">
              <Monitor className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
              <p className="text-muted-foreground text-sm">No VMs are using this template</p>
            </div>
          ) : (
            <div className="space-y-3 max-h-80 overflow-y-auto">
              {templateVMs.map((vm: VM) => (
                <Link key={vm.id} href={`/vms/${vm.id}`} className="flex items-center gap-3 p-3 rounded-md border bg-muted/50 hover:bg-muted transition-colors">
                  <div className="w-8 h-8 bg-muted rounded-md flex items-center justify-center border">
                    <Monitor className="w-4 h-4" />
                  </div>
                  <div className="flex-1">
                    <p className="font-medium text-sm">{vm.hostname}</p>
                    <p className="text-xs text-muted-foreground">{vm.resources.cpu} vCPU • {Math.round(vm.resources.ram / 1024)} GB RAM</p>
                  </div>
                  <span className={`text-xs font-medium px-2 py-0.5 rounded-md border ${
                    vm.status === "running" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : vm.status === "error" ? "bg-destructive text-destructive-foreground border-destructive" : "bg-muted text-muted-foreground"
                  }`}>{vm.status}</span>
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>


    </div>
  )
}
