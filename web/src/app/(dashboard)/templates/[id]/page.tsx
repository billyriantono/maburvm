"use client"

import { useState, useCallback, useEffect } from "react"
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
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { useTemplate, useDeleteTemplate, useUpdateTemplate } from "@/lib/hooks/use-templates"
import { useVMs } from "@/lib/hooks/use-vms"
import { OSIcon } from "@/components/os-icon"
import type { VM } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" })
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  return (
    <div className={`fixed bottom-4 right-4 z-50 rounded-md border px-6 py-4 shadow-md ${type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"}`}>
      <p className="text-sm font-medium">{message}</p>
    </div>
  )
}

export default function TemplateDetailPage() {
  const confirm = useConfirm()
  const params = useParams()
  const router = useRouter()
  const templateId = params.id as string

  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [editOpen, setEditOpen] = useState(false)
  const [editName, setEditName] = useState("")
  const [editVersion, setEditVersion] = useState("")
  const [editDescription, setEditDescription] = useState("")

  // Data hooks
  const { data: template, isLoading, error, refetch } = useTemplate(templateId)
  const { data: vmsData, isLoading: vmsLoading } = useVMs({ pageSize: 100 })
  const deleteTemplate = useDeleteTemplate()
  const updateTemplate = useUpdateTemplate(templateId)

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
    })
    if (!ok) return
    try {
      await deleteTemplate.mutateAsync(templateId)
      setToast({ message: "Template deleted", type: "success" })
      setTimeout(() => router.push("/templates"), 1000)
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [confirm, template, templateVMs.length, deleteTemplate, templateId, router])

  // Edit handler
  const handleEdit = useCallback(async () => {
    try {
      await updateTemplate.mutateAsync({
        name: editName,
        version: editVersion,
        description: editDescription || undefined,
      })
      setToast({ message: "Template updated", type: "success" })
      setEditOpen(false)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to update: ${(err as Error).message}`, type: "error" })
    }
  }, [updateTemplate, editName, editVersion, editDescription, refetch])

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
            if (open) { setEditName(template.name); setEditVersion(template.version); setEditDescription(template.description || "") }
          }}>
            <DialogTrigger asChild>
              <Button variant="outline" className="gap-2"><Edit2 className="w-4 h-4" />Edit</Button>
            </DialogTrigger>
            <DialogContent className="max-w-md">
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
            <div className="flex items-center justify-between py-2 border-b">
              <span className="text-sm text-muted-foreground">Image Path</span>
              <span className="font-mono text-xs">{template.image_path}</span>
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


      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
