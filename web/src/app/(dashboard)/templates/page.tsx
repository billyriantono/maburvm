"use client"

import { useState, useMemo, useEffect, useCallback } from "react"
import Link from "next/link"
import { 
  Plus, 
  Search,
  Trash2,
  FileArchive,
  CheckCircle2,
  XCircle,
  HardDrive,
  Loader2,
  AlertCircle,
  X
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useTemplates, useDeleteTemplate } from "@/lib/hooks/use-templates"
import type { OSTemplate } from "@/types"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" })
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success text-black" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

function ConfirmDialog({ open, title, message, loading, onConfirm, onCancel }: { open: boolean; title: string; message: string; loading?: boolean; onConfirm: () => void; onCancel: () => void }) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Confirm dialog">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onCancel} aria-label="Close dialog" />
      <div className="relative bg-white border-4 border-black p-6 shadow-neo-xl max-w-md w-full mx-4">
        <h3 className="text-xl font-black uppercase mb-4">{title}</h3>
        <p className="text-gray-600 font-medium mb-6">{message}</p>
        <div className="flex gap-3 justify-end">
          <Button variant="ghost" onClick={onCancel} className="border-2 border-black" disabled={loading}>Cancel</Button>
          <Button variant="destructive" onClick={onConfirm} disabled={loading}>
            {loading && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
            Confirm Delete
          </Button>
        </div>
      </div>
    </div>
  )
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

export default function TemplateListPage() {
  const [searchQuery, setSearchQuery] = useState("")
  const [activeFilter, setActiveFilter] = useState<string>("")
  const [deleteConfirm, setDeleteConfirm] = useState<{ id: string; name: string } | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)

  // Data hooks
  const { data: templates, isLoading, error, refetch } = useTemplates()
  const deleteTemplate = useDeleteTemplate()

  // Filter templates
  const filteredTemplates = useMemo(() => {
    if (!templates) return []
    let result = [...templates]

    if (searchQuery) {
      const query = searchQuery.toLowerCase()
      result = result.filter(t =>
        t.name.toLowerCase().includes(query) ||
        (t.description?.toLowerCase().includes(query))
      )
    }

    if (activeFilter === "active") {
      result = result.filter(t => t.is_active)
    } else if (activeFilter === "inactive") {
      result = result.filter(t => !t.is_active)
    }

    return result
  }, [templates, searchQuery, activeFilter])

  // Delete handler
  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    try {
      await deleteTemplate.mutateAsync(deleteConfirm.id)
      setToast({ message: `Template "${deleteConfirm.name}" deleted`, type: "success" })
      setDeleteConfirm(null)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteConfirm, deleteTemplate, refetch])

  const clearFilters = () => { setSearchQuery(""); setActiveFilter("") }
  const hasFilters = searchQuery || activeFilter

  // Loading
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex items-center justify-between mb-6">
          <div>
            <h1 className="text-3xl font-black uppercase tracking-tight text-black">OS Templates</h1>
            <Skeleton className="h-5 w-32 mt-1" />
          </div>
        </div>
        <Skeleton className="h-16 border-4 border-black mb-6" />
        <div className="space-y-4">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-20 border-4 border-black" />)}
        </div>
      </div>
    )
  }

  // Error
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
          <AlertCircle className="w-16 h-16 text-danger mx-auto mb-4" />
          <h2 className="text-xl font-black uppercase mb-2">Failed to load templates</h2>
          <p className="text-gray-500 font-medium mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()}>Retry</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-7xl mx-auto">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">OS Templates</h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            {filteredTemplates.length} templates
          </p>
        </div>
        <Link href="/templates/new">
          <Button className="gap-2"><Plus className="w-4 h-4" />Add Template</Button>
        </Link>
      </div>

      {/* Filters */}
      <div className="bg-white border-4 border-black p-4 shadow-neo mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <Input type="text" placeholder="Search templates..." value={searchQuery} onChange={(e) => setSearchQuery(e.target.value)} className="pl-10 border-2 border-black" />
          </div>
          <select value={activeFilter} onChange={(e) => setActiveFilter(e.target.value)} className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm">
            <option value="">All</option>
            <option value="active">Active</option>
            <option value="inactive">Inactive</option>
          </select>
          {hasFilters && (
            <Button variant="ghost" onClick={clearFilters} className="border-2 border-black gap-1"><X className="w-4 h-4" />Clear</Button>
          )}
        </div>
      </div>

      {/* Data Table */}
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
          <div className="col-span-4">Template</div>
          <div className="col-span-2">Version</div>
          <div className="col-span-2">Status</div>
          <div className="col-span-2">Created</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {filteredTemplates.length === 0 ? (
          <div className="p-12 text-center">
            <FileArchive className="w-12 h-12 text-gray-300 mx-auto mb-4" />
            <p className="text-gray-500 font-bold uppercase">No templates found</p>
            {hasFilters && (
              <Button variant="ghost" onClick={clearFilters} className="mt-4 border-2 border-black">Clear filters</Button>
            )}
          </div>
        ) : (
          filteredTemplates.map((template, index) => (
            <div key={template.id} className={`grid grid-cols-12 gap-4 p-4 items-center border-b-2 border-black last:border-0 ${index % 2 === 0 ? "bg-white" : "bg-gray-50"}`}>
              {/* Template Info */}
              <div className="col-span-4">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 bg-secondary flex items-center justify-center border-2 border-black text-xl">
                    {getTemplateIcon(template.name)}
                  </div>
                  <div>
                    <Link href={`/templates/${template.id}`} className="font-black text-black hover:text-primary transition-colors">
                      {template.name}
                    </Link>
                    {template.description && (
                      <p className="text-xs text-gray-500 font-medium truncate max-w-[200px]">{template.description}</p>
                    )}
                  </div>
                </div>
              </div>

              {/* Version */}
              <div className="col-span-2">
                <span className="inline-flex items-center px-2 py-1 text-xs font-bold border border-black bg-gray-100">v{template.version}</span>
              </div>

              {/* Status */}
              <div className="col-span-2">
                {template.is_active ? (
                  <span className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase border border-black bg-success">
                    <CheckCircle2 className="w-3 h-3" />Active
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase border border-black bg-gray-300">
                    <XCircle className="w-3 h-3" />Inactive
                  </span>
                )}
              </div>

              {/* Created */}
              <div className="col-span-2">
                <span className="text-sm font-medium">{formatDate(template.created_at)}</span>
              </div>

              {/* Actions */}
              <div className="col-span-2 flex items-center justify-end gap-2">
                <Link href={`/templates/${template.id}`}>
                  <Button variant="secondary" size="sm" className="h-8">Details</Button>
                </Link>
                <Button variant="ghost" size="sm" onClick={() => setDeleteConfirm({ id: template.id, name: template.name })} disabled={deleteTemplate.isPending} className="h-8 w-8 p-0 border-2 border-black hover:bg-danger hover:text-white" title="Delete">
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>

      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete Template"
        message={`Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`}
        loading={deleteTemplate.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
