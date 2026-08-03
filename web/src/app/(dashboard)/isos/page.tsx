"use client"

import { useState, useCallback, useRef } from "react"
import {
  Plus, 
  Search,
  Download,
  Trash2,
  Loader2,
  Folder,
  Disc,
  HardDrive,
  Play,
  Upload,
  Globe
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useTemplates, useDeleteTemplate } from "@/lib/hooks/use-templates"
import { api } from "@/lib/api-client"
import { cn } from "@/lib/utils"

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

function ConfirmDialog({
  open, 
  title, 
  message, 
  onConfirm, 
  onCancel 
}: { 
  open: boolean
  title: string
  message: string
  onConfirm: () => void
  onCancel: () => void
}) {
  if (!open) return null
  
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Confirm dialog">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={onCancel} aria-label="Close dialog" />
      <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-md w-full mx-4">
        <h3 className="text-lg font-semibold mb-4">{title}</h3>
        <p className="text-muted-foreground text-sm mb-6">{message}</p>
        <div className="flex gap-3 justify-end">
          <Button variant="outline" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            Confirm Delete
          </Button>
        </div>
      </div>
    </div>
  )
}

function UploadDialog({
  open,
  onClose,
  onUpload,
  onRemoteUpload
}: {
  open: boolean
  onClose: () => void
  onUpload: (file: File) => void
  onRemoteUpload: (url: string, filename: string) => void
}) {
  const [activeTab, setActiveTab] = useState<"local" | "remote">("local")
  const [file, setFile] = useState<File | null>(null)
  const [remoteUrl, setRemoteUrl] = useState("")
  const [remoteFilename, setRemoteFilename] = useState("")
  const fileInputRef = useRef<HTMLInputElement>(null)
  
  if (!open) return null

  const handleDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      setFile(e.dataTransfer.files[0])
    }
  }

  const handleDragOver = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault()
  }

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
    setFile(null)
    setRemoteUrl("")
    setRemoteFilename("")
    onClose()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" role="dialog" aria-modal="true" aria-label="Upload dialog">
      <button type="button" className="absolute inset-0 bg-black/50 cursor-default focus:outline-none" onClick={resetState} aria-label="Close dialog" />
      <div className="relative bg-background border rounded-lg p-6 shadow-lg max-w-md w-full mx-4">
        <h3 className="text-lg font-semibold mb-4 flex items-center gap-2">
          <Upload className="w-6 h-6" />
          Add ISO Image
        </h3>

        <div className="flex rounded-md border overflow-hidden mb-6">
          <button
            type="button"
            className={cn(
              "flex-1 py-2 font-medium text-sm border-r flex items-center justify-center gap-2 transition-colors",
              activeTab === "local" ? "bg-muted text-foreground" : "bg-background text-muted-foreground hover:bg-muted/50"
            )}
            onClick={() => setActiveTab("local")}
          >
            <HardDrive className="w-4 h-4" />
            Local Upload
          </button>
          <button
            type="button"
            className={cn(
              "flex-1 py-2 font-medium text-sm flex items-center justify-center gap-2 transition-colors",
              activeTab === "remote" ? "bg-muted text-foreground" : "bg-background text-muted-foreground hover:bg-muted/50"
            )}
            onClick={() => setActiveTab("remote")}
          >
            <Globe className="w-4 h-4" />
            Remote URL
          </button>
        </div>

        {activeTab === "local" ? (
          <div
            className="rounded-md border-2 border-dashed p-8 text-center mb-6 cursor-pointer hover:bg-muted/50 transition-colors"
            onDrop={handleDrop}
            onDragOver={handleDragOver}
            onClick={() => fileInputRef.current?.click()}
            role="button"
            tabIndex={0}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                fileInputRef.current?.click()
              }
            }}
          >
            <input 
              type="file" 
              ref={fileInputRef}
              className="hidden" 
              accept=".iso"
              onChange={(e) => {
                if (e.target.files && e.target.files.length > 0) {
                  setFile(e.target.files[0])
                }
              }}
            />
            {file ? (
              <div className="flex flex-col items-center gap-2">
                <Disc className="w-12 h-12 text-foreground" />
                <p className="font-medium truncate max-w-full">{file.name}</p>
                <p className="text-sm text-muted-foreground">{formatBytes(file.size)}</p>
              </div>
            ) : (
              <div className="flex flex-col items-center gap-2">
                <Upload className="w-12 h-12 text-muted-foreground" />
                <p className="font-medium">Click or drag ISO file</p>
                <p className="text-sm text-muted-foreground">Only .iso files are supported</p>
              </div>
            )}
          </div>
        ) : (
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
        )}
        
        <div className="flex gap-3 justify-end">
          <Button variant="outline" onClick={resetState}>
            Cancel
          </Button>
          {activeTab === "local" ? (
            <Button 
              disabled={!file} 
              onClick={() => {
                if (file) {
                  onUpload(file)
                  resetState()
                }
              }}
            >
              Upload
            </Button>
          ) : (
            <Button 
              disabled={!remoteUrl} 
              onClick={() => {
                handleRemoteSubmit()
                resetState()
              }}
            >
              Download
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}

function Toast({ message, type }: { message: string, type: "success" | "error", onClose: () => void }) {
  return (
    <div className={`fixed bottom-4 right-4 z-50 rounded-md border px-6 py-4 shadow-md ${
      type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"
    }`}>
      <p className="text-sm font-medium">{message}</p>
    </div>
  )
}

export default function ISOListPage() {
  const { data: templates, isLoading, error, refetch } = useTemplates({ type: 'iso' })
  const deleteTemplate = useDeleteTemplate()
  const [searchQuery, setSearchQuery] = useState("")
  const [osFilter, setOsFilter] = useState("")
  const [deleteConfirm, setDeleteConfirm] = useState<{ id: string; name: string } | null>(null)
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
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

  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return
    try {
      await deleteTemplate.mutateAsync(deleteConfirm.id)
      setToast({ message: `ISO "${deleteConfirm.name}" deleted`, type: "success" })
      setDeleteConfirm(null)
      refetch()
    } catch (err) {
      setToast({ message: `Failed to delete: ${(err as Error).message}`, type: "error" })
    }
  }, [deleteConfirm, deleteTemplate, refetch])

  const handleUpload = useCallback(() => {
    setUploadDialogOpen(false)
    // Direct file upload isn't supported (the node fetches images by URL).
    setToast({ message: "Local upload isn't supported — use Remote URL so the node can fetch the ISO.", type: "error" })
  }, [])

  const handleRemoteUpload = useCallback((url: string, filename: string) => {
    setUploadDialogOpen(false)
    api.post('/api/v1/templates/download-url', { url, filename }).then(() => {
      setToast({ message: `Downloading ${filename} from remote URL...`, type: "success" })
    }).catch((err: any) => {
      setToast({ message: `Download failed: ${err?.message || 'Unknown error'}`, type: "error" })
    })
  }, [])

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
          <p className="font-medium">Error loading ISOs: {error.message}</p>
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
          Upload ISO
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
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-md border bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900">
                  Available
                </span>
              </div>

              <div className="col-span-2 flex items-center justify-end gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled
                  className="gap-1"
                  title="Download"
                >
                  <Download className="w-4 h-4" />
                  <span className="hidden sm:inline">Download</span>
                </Button>

                <Button
                  variant="success"
                  size="sm"
                  disabled
                  className="gap-1"
                  title="Mount to VM"
                >
                  <Play className="w-4 h-4" />
                  <span className="hidden sm:inline">Mount</span>
                </Button>

                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirm({ id: iso.id, name: iso.name })}
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
      
      <ConfirmDialog
        open={!!deleteConfirm}
        title="Delete ISO"
        message={`Are you sure you want to delete "${deleteConfirm?.name}"? This action cannot be undone.`}
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />

      <UploadDialog
        open={uploadDialogOpen}
        onClose={() => setUploadDialogOpen(false)}
        onUpload={handleUpload}
        onRemoteUpload={handleRemoteUpload}
      />
      
      {toast && (
        <Toast 
          message={toast.message} 
          type={toast.type} 
          onClose={() => setToast(null)} 
        />
      )}
    </div>
  )
}