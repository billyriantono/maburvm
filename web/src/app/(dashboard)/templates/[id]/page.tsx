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
import type { VM } from "@/types"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" })
}

function getTemplateIcon(name: string): string {
  const lower = name.toLowerCase()
  if (lower.includes("ubuntu")) return "🟠"
  if (lower.includes("debian")) return "🔴"
  if (lower.includes("centos") || lower.includes("alma") || lower.includes("rocky")) return "🟢"
  if (lower.includes("fedora")) return "🔵"
  if (lower.includes("windows")) return "🪟"
  if (lower.includes("arch")) return "🔷"
  return "🐧"
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${type === "success" ? "bg-success text-black" : "bg-danger text-white"}`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

export default function TemplateDetailPage() {
  const params = useParams()
  const router = useRouter()
  const templateId = params.id as string

  const [deleteConfirm, setDeleteConfirm] = useState(false)
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
    try {
      await deleteTemplate.mutateAsync(templateId)
      setToast({ message: "Template deleted", type: "success" })
      setDeleteConfirm(false)
      setTimeout(() => router.push("/templates"), 1000)
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteTemplate, templateId, router])

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
          <Link href="/templates"><Button variant="ghost" size="sm" className="border-2 border-black"><ArrowLeft className="w-4 h-4" /></Button></Link>
          <Skeleton className="h-12 w-64" />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <Skeleton className="h-64 border-4 border-black" />
          <Skeleton className="h-64 border-4 border-black" />
        </div>
      </div>
    )
  }

  // Error / not found
  if (error || !template) {
    return (
      <div className="max-w-5xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Template Not Found</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error)?.message || "The requested template does not exist."}</p>
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
          <Button variant="ghost" size="sm" className="border-2 border-black"><ArrowLeft className="w-4 h-4" /></Button>
        </Link>
        <div className="flex-1">
          <div className="flex items-center gap-3">
            <div className="w-12 h-12 bg-secondary flex items-center justify-center border-2 border-black text-2xl">
              {getTemplateIcon(template.name)}
            </div>
            <div>
              <h1 className="text-3xl font-black uppercase tracking-tight text-black">{template.name}</h1>
              <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">v{template.version}</p>
            </div>
          </div>
        </div>
        <div className="flex gap-2">
          <Dialog open={editOpen} onOpenChange={(open) => {
            setEditOpen(open)
            if (open) { setEditName(template.name); setEditVersion(template.version); setEditDescription(template.description || "") }
          }}>
            <DialogTrigger asChild>
              <Button variant="ghost" className="border-2 border-black gap-2"><Edit2 className="w-4 h-4" />Edit</Button>
            </DialogTrigger>
            <DialogContent className="max-w-md border-4 border-black shadow-neo-xl">
              <DialogHeader>
                <DialogTitle className="text-lg font-black uppercase">Edit Template</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div>
                  <label htmlFor="edit-name" className="block text-xs font-black uppercase text-gray-500 mb-1">Name</label>
                  <Input id="edit-name" value={editName} onChange={(e) => setEditName(e.target.value)} className="border-2 border-black" />
                </div>
                <div>
                  <label htmlFor="edit-version" className="block text-xs font-black uppercase text-gray-500 mb-1">Version</label>
                  <Input id="edit-version" value={editVersion} onChange={(e) => setEditVersion(e.target.value)} className="border-2 border-black" />
                </div>
                <div>
                  <label htmlFor="edit-desc" className="block text-xs font-black uppercase text-gray-500 mb-1">Description</label>
                  <Input id="edit-desc" value={editDescription} onChange={(e) => setEditDescription(e.target.value)} className="border-2 border-black" />
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
          <Button variant="destructive" onClick={() => setDeleteConfirm(true)} className="gap-2"><Trash2 className="w-4 h-4" />Delete</Button>
        </div>
      </div>

      {/* Template Info */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6 mb-6">
        {/* Details Card */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2"><FileArchive className="w-5 h-5" />Template Details</h2>
          <div className="space-y-4">
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Name</span>
              <span className="font-black">{template.name}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Version</span>
              <span className="font-black">v{template.version}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Status</span>
              {template.is_active ? (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-black uppercase border border-black bg-success"><CheckCircle2 className="w-3 h-3" />Active</span>
              ) : (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-black uppercase border border-black bg-gray-300"><XCircle className="w-3 h-3" />Inactive</span>
              )}
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Image Path</span>
              <span className="font-mono text-xs">{template.image_path}</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b-2 border-black">
              <span className="text-sm font-bold uppercase text-gray-500">Created</span>
              <span className="font-bold">{formatDate(template.created_at)}</span>
            </div>
            <div className="flex items-center justify-between py-2">
              <span className="text-sm font-bold uppercase text-gray-500">Updated</span>
              <span className="font-bold">{formatDate(template.updated_at)}</span>
            </div>
          </div>
          {template.description && (
            <div className="mt-6 pt-4 border-t-2 border-black">
              <h3 className="text-xs font-black uppercase text-gray-500 mb-2">Description</h3>
              <p className="text-sm font-medium">{template.description}</p>
            </div>
          )}
        </div>

        {/* VMs Using Template */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2"><Monitor className="w-5 h-5" />VMs Using This Template ({templateVMs.length})</h2>
          {vmsLoading ? (
            <div className="space-y-3">
              {[1,2,3].map(i => <Skeleton key={i} className="h-14 w-full" />)}
            </div>
          ) : templateVMs.length === 0 ? (
            <div className="p-8 text-center">
              <Monitor className="w-10 h-10 text-gray-300 mx-auto mb-3" />
              <p className="text-gray-500 text-sm font-medium">No VMs are using this template</p>
            </div>
          ) : (
            <div className="space-y-3 max-h-80 overflow-y-auto">
              {templateVMs.map((vm: VM) => (
                <Link key={vm.id} href={`/vms/${vm.id}`} className="flex items-center gap-3 p-3 bg-gray-50 border-2 border-black hover:bg-primary/20 transition-colors">
                  <div className="w-8 h-8 bg-primary flex items-center justify-center border-2 border-black">
                    <Monitor className="w-4 h-4" />
                  </div>
                  <div className="flex-1">
                    <p className="font-black text-sm">{vm.hostname}</p>
                    <p className="text-xs text-gray-500">{vm.resources.cpu} vCPU • {Math.round(vm.resources.ram / 1024)} GB RAM</p>
                  </div>
                  <span className={`text-[10px] font-black uppercase px-2 py-0.5 border border-black ${
                    vm.status === "running" ? "bg-success" : vm.status === "error" ? "bg-danger text-white" : "bg-gray-200"
                  }`}>{vm.status}</span>
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Delete Confirmation */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Delete confirmation">
          <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={() => setDeleteConfirm(false)} aria-label="Close dialog" />
          <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
            <h3 className="text-xl font-black uppercase mb-4 flex items-center gap-2"><AlertTriangle className="w-6 h-6 text-warning" />Delete Template</h3>
            {templateVMs.length > 0 ? (
              <p className="text-gray-600 font-medium mb-6">
                <span className="text-danger font-bold">WARNING:</span> This template is used by {templateVMs.length} VM(s). Deleting it may affect those VMs. Are you sure?
              </p>
            ) : (
              <p className="text-gray-600 font-medium mb-6">Are you sure you want to delete &quot;{template.name}&quot;? This action cannot be undone.</p>
            )}
            <div className="flex gap-3 justify-end">
              <Button variant="ghost" onClick={() => setDeleteConfirm(false)} className="border-2 border-black" disabled={deleteTemplate.isPending}>Cancel</Button>
              <Button variant="destructive" onClick={handleDelete} disabled={deleteTemplate.isPending}>
                {deleteTemplate.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Delete Template
              </Button>
            </div>
          </div>
        </div>
      )}

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
