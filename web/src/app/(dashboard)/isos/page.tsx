"use client"

import { useState, useCallback } from "react"
import {
  Plus, 
  Search,
  Trash2,
  Loader2,
  Folder,
  Disc,
  HardDrive,
  Upload,
  AlertCircle
} from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { useTemplates, useDeleteTemplate } from "@/lib/hooks/use-templates"
import { api } from "@/lib/api-client"
import { useConfirm } from "@/components/confirm-provider"

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}
function UploadDialog({
  open,
  onClose,
  onRemoteUpload
}: {
  open: boolean
  onClose: () => void
  onRemoteUpload: (url: string, filename: string) => void
}) {
  const [remoteUrl, setRemoteUrl] = useState("")
  const [remoteFilename, setRemoteFilename] = useState("")
  
  const handleRemoteSubmit = () => {
    if (remoteUrl) {
      let finalName = remoteFilename.trim()
      if (!finalName) {
        try {
          const parsedUrl = new URL(remoteUrl)
          const pathParts = parsedUrl.pathname.split('/')
          const lastPart = pathParts[pathParts.length - 1]
          finalName = lastPart && lastPart.endsWith('.iso') ? lastPart : 'downloaded-image.iso'
        } catch (e) {
          finalName = 'downloaded-image.iso'
        }
      } else if (!finalName.endsWith('.iso')) {
        finalName += '.iso'
      }
      onRemoteUpload(remoteUrl, finalName)
      setRemoteUrl("")
      setRemoteFilename("")
    }
  }

  const resetState = () => {
    setRemoteUrl("")
    setRemoteFilename("")
    onClose()
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) resetState() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Upload className="w-6 h-6" aria-hidden="true" />
            Add ISO Image
          </DialogTitle>
          <DialogDescription className="sr-only">Download an ISO from a remote URL.</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 mb-6">
            <div>
              <label htmlFor="remote-url" className="block text-sm font-medium mb-1">Direct URL</label>
              <Input
                id="remote-url"
                placeholder="https://example.com/ubuntu.iso"
                value={remoteUrl}
                onChange={(e) => setRemoteUrl(e.target.value)}
              />
            </div>
            <div>
              <label htmlFor="remote-filename" className="block text-sm font-medium mb-1">Save As (Optional)</label>
              <Input
                id="remote-filename"
                placeholder="ubuntu-custom.iso"
                value={remoteFilename}
                onChange={(e) => setRemoteFilename(e.target.value)}
              />
            </div>
          </div>
        
        <DialogFooter className="gap-3 sm:gap-0">
          <Button variant="outline" onClick={resetState}>
            Cancel
          </Button>
          <Button
            disabled={!remoteUrl}
            onClick={() => {
              handleRemoteSubmit()
              resetState()
            }}
          >
            Download
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default function ISOListPage() {
  const confirm = useConfirm()
  const { data: templates, isLoading, error, refetch } = useTemplates({ type: 'iso' })
  const deleteTemplate = useDeleteTemplate()
  const [searchQuery, setSearchQuery] = useState("")
  const [osFilter, setOsFilter] = useState("")
  const [uploadDialogOpen, setUploadDialogOpen] = useState(false)
  
  const osTypes = Array.from(new Set(templates?.map(t => t.name.split('-')[0].charAt(0).toUpperCase() + t.name.split('-')[0].slice(1)) || []))

  const handleSearchChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setSearchQuery(e.target.value)
  }
  
  const handleOsFilterChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    setOsFilter(e.target.value)
  }

  const filteredISOs = templates?.filter(t => {
    const query = searchQuery.toLowerCase()
    const matchesSearch = !query || t.name.toLowerCase().includes(query) || t.description?.toLowerCase().includes(query)
    const matchesOs = !osFilter || t.name.toLowerCase().startsWith(osFilter.toLowerCase())
    return matchesSearch && matchesOs
  }) || []

  const handleDelete = useCallback(async (iso: { id: string; name: string }) => {
    const ok = await confirm({
      title: `Delete ISO "${iso.name}"?`,
      description: "The stored image is removed from the nodes. This cannot be undone.",
      confirmLabel: "Delete ISO",
      destructive: true,
      action: () => deleteTemplate.mutateAsync(iso.id),
    })
    if (!ok) return
    toast.success(`ISO "${iso.name}" deleted`)
    refetch()
  }, [confirm, deleteTemplate, refetch])

  const handleRemoteUpload = useCallback((url: string, filename: string) => {
    setUploadDialogOpen(false)
    api.post('/api/v1/templates/download-url', { url, filename }).then(() => {
      toast.success(`Downloading ${filename} from remote URL...`)
      refetch()
    }).catch((err: any) => {
      toast.error(`Download failed: ${err?.message || 'Unknown error'}`)
    })
  }, [refetch])

  if (isLoading) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="flex items-center gap-4 mb-6">
          <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-3">
            <Folder className="w-7 h-7" />
            ISOs
          </h1>
        </div>
        <div className="flex items-center justify-center p-12">
          <Loader2 className="w-8 h-8 animate-spin" />
          <span className="ml-2 font-medium text-muted-foreground">Loading ISOs...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="max-w-7xl mx-auto">
        <div className="rounded-lg border border-destructive bg-destructive/10 text-destructive p-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-5 h-5" aria-hidden="true" />
            <p className="font-medium flex-1">Error loading ISOs: {error.message}</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-3">
            <Folder className="w-7 h-7" />
            ISOs
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            {filteredISOs.length} images
          </p>
        </div>
        <Button className="gap-2" onClick={() => setUploadDialogOpen(true)}>
          <Plus className="w-4 h-4" />
          Add ISO
        </Button>
      </div>
      
      <div className="bg-card text-card-foreground border rounded-lg p-4 shadow-sm mb-6">
        <div className="flex flex-col md:flex-row gap-4">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input
              type="text"
              placeholder="Search ISOs..."
              value={searchQuery}
              onChange={handleSearchChange}
              className="pl-10"
            />
          </div>

          <select
            value={osFilter}
            onChange={handleOsFilterChange}
            className="h-10 px-3 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <option value="">All Operating Systems</option>
            {osTypes.map(os => (
              <option key={os} value={os}>{os}</option>
            ))}
          </select>
        </div>
      </div>
      
      <div className="bg-card text-card-foreground border rounded-lg shadow-sm overflow-hidden">
        <div className="grid grid-cols-12 gap-4 p-4 bg-muted text-muted-foreground font-medium text-xs">
          <div className="col-span-4">ISO Image</div>
          <div className="col-span-2">OS</div>
          <div className="col-span-2">Size</div>
          <div className="col-span-2">Status</div>
          <div className="col-span-2 text-right">Actions</div>
        </div>

        {filteredISOs.length === 0 ? (
          <div className="p-12 text-center text-muted-foreground font-medium">
            No ISOs found
          </div>
        ) : (
          filteredISOs.map((iso) => (
            <div
              key={iso.id}
              className="grid grid-cols-12 gap-4 p-4 items-center border-b last:border-0 hover:bg-muted/50 transition-colors"
            >
              <div className="col-span-4">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 bg-muted rounded-md flex items-center justify-center border">
                    <Disc className="w-5 h-5" />
                  </div>
                  <div>
                    <p className="font-medium text-sm truncate max-w-[250px]" title={iso.name}>
                      {iso.name}
                    </p>
                    <p className="text-xs text-muted-foreground">v{iso.version} • {iso.created_at?.split('T')[0]}</p>
                  </div>
                </div>
              </div>

              <div className="col-span-2">
                <span className="inline-flex items-center px-2 py-1 text-xs font-medium rounded-md border bg-muted text-muted-foreground">
                  {iso.name.split('-')[0] || 'N/A'}
                </span>
              </div>

              <div className="col-span-2">
                <div className="flex items-center gap-2">
                  <HardDrive className="w-4 h-4 text-muted-foreground" />
                  <span className="font-mono text-sm">{iso.size_bytes ? formatBytes(iso.size_bytes) : "-"}</span>
                </div>
              </div>

              <div className="col-span-2">
                <span className={iso.is_active
                  ? "inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900"
                  : "inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-muted text-muted-foreground"}>
                  {iso.is_active ? "Active" : "Inactive"}
                </span>
              </div>

              <div className="col-span-2 flex items-center justify-end gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleDelete({ id: iso.id, name: iso.name })}
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title="Delete"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          ))
        )}
      </div>
      

      <UploadDialog
        open={uploadDialogOpen}
        onClose={() => setUploadDialogOpen(false)}
        onRemoteUpload={handleRemoteUpload}
      />
    </div>
  )
}