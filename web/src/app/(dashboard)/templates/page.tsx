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
  Loader2,
  AlertCircle,
  X
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useTemplates, useDeleteTemplate } from "@/lib/hooks/use-templates"
import { OSIcon } from "@/components/os-icon"
import { useConfirm } from "@/components/confirm-provider"

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" })
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])

  return (
    <div className={`fixed bottom-4 right-4 z-50 rounded-md border px-6 py-4 shadow-md ${
      type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"
    }`}>
      <p className="text-sm font-medium">{message}</p>
    </div>
  )
}
export default function TemplateListPage() {
  const confirm = useConfirm()
  const [searchQuery, setSearchQuery] = useState("")
  const [activeFilter, setActiveFilter] = useState<string>("")
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
  const handleDelete = useCallback(async (template: { id: string; name: string }) => {
    const ok = await confirm({
      title: `Delete template "${template.name}"?`,
      description:
        "New VMs can no longer be built from it. Machines already created from this template are unaffected.",
      confirmLabel: "Delete template",
      destructive: true,
      action: () => deleteTemplate.mutateAsync(template.id),
    })
    if (!ok) return
    setToast({ message: `Template "${template.name}" deleted`, type: "success" })
    refetch()
  }, [confirm, deleteTemplate, refetch])

  const clearFilters = () => { setSearchQuery(""); setActiveFilter("") }
  const hasFilters = searchQuery || activeFilter

  // Loading
  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex items-center justify-between mb-6">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">OS Templates</h1>
            <Skeleton className="h-5 w-32 mt-1" />
          </div>
        </div>
        <Skeleton className="h-16 rounded-lg mb-6" />
        <div className="space-y-4">
          {[1,2,3,4].map(i => <Skeleton key={i} className="h-20 rounded-lg" />)}
        </div>
      </div>
    )
  }

  // Error
  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="bg-card text-card-foreground border rounded-lg p-12 shadow-sm text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" />
          <h2 className="text-lg font-semibold mb-2">Failed to load templates</h2>
          <p className="text-muted-foreground text-sm mb-6">{(error as Error).message}</p>
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
          <h1 className="text-2xl font-semibold tracking-tight">OS Templates</h1>
          <p className="text-muted-foreground text-sm mt-1">
            {filteredTemplates.length} templates
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Link href="/templates/catalog">
            <Button variant="outline" className="gap-2"><FileArchive className="w-4 h-4" />Browse Catalog</Button>
          </Link>
          <Link href="/templates/new">
            <Button className="gap-2"><Plus className="w-4 h-4" />Add Template</Button>
          </Link>
        </div>
      </div>

      {/* Filters */}
      <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input type="text" placeholder="Search templates..." value={searchQuery} onChange={(e) => setSearchQuery(e.target.value)} className="pl-10" />
          </div>
          <select value={activeFilter} onChange={(e) => setActiveFilter(e.target.value)} className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2">
            <option value="">All</option>
            <option value="active">Active</option>
            <option value="inactive">Inactive</option>
          </select>
          {hasFilters && (
            <Button variant="outline" onClick={clearFilters} className="gap-1"><X className="w-4 h-4" />Clear</Button>
          )}
        </div>
      </div>

      {/* Data Table */}
      <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-muted text-muted-foreground font-medium text-xs">
          <div className="col-span-4">Template</div>
          <div className="col-span-2">Version</div>
          <div className="col-span-2">Status</div>
          <div className="col-span-2">Created</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {filteredTemplates.length === 0 ? (
          <div className="p-12 text-center">
            <FileArchive className="w-12 h-12 text-muted-foreground mx-auto mb-4" />
            <p className="text-muted-foreground font-medium">No templates found</p>
            {hasFilters && (
              <Button variant="outline" onClick={clearFilters} className="mt-4">Clear filters</Button>
            )}
          </div>
        ) : (
          filteredTemplates.map((template) => (
            <div key={template.id} className="grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0 hover:bg-muted/50 transition-colors">
              {/* Template Info */}
              <div className="col-span-4">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 bg-muted rounded-md flex items-center justify-center border">
                    <OSIcon name={template.name} className="w-6 h-6" />
                  </div>
                  <div>
                    <Link href={`/templates/${template.id}`} className="font-medium hover:text-primary transition-colors">
                      {template.name}
                    </Link>
                    {template.description && (
                      <p className="text-xs text-muted-foreground truncate max-w-[200px]">{template.description}</p>
                    )}
                  </div>
                </div>
              </div>

              {/* Version */}
              <div className="col-span-2">
                <span className="inline-flex items-center px-2 py-1 text-xs font-medium rounded-md border bg-muted text-muted-foreground">v{template.version}</span>
              </div>

              {/* Status */}
              <div className="col-span-2">
                {template.is_active ? (
                  <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900">
                    <CheckCircle2 className="w-3 h-3" />Active
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-muted text-muted-foreground">
                    <XCircle className="w-3 h-3" />Inactive
                  </span>
                )}
              </div>

              {/* Created */}
              <div className="col-span-2">
                <span className="text-sm text-muted-foreground">{formatDate(template.created_at)}</span>
              </div>

              {/* Actions */}
              <div className="col-span-2 flex items-center justify-end gap-2">
                <Link href={`/templates/${template.id}`}>
                  <Button variant="outline" size="sm" className="h-8">Details</Button>
                </Link>
                <Button variant="ghost" size="sm" onClick={() => handleDelete({ id: template.id, name: template.name })} disabled={deleteTemplate.isPending} className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive" title="Delete">
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>


      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
