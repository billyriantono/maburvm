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
import { useTemplates, useDeleteTemplate, useUploadISO, useDriversISO, useSetDriversISO } from "@/lib/hooks/use-templates"
import { api } from "@/lib/api-client"
import { useConfirm } from "@/components/confirm-provider"
import { ImageIntegrityFields, emptyIntegrity, integrityProblem, type ImageIntegrity } from "@/components/image-integrity-fields"

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
  onAdded,
}: {
  open: boolean
  onClose: () => void
  onAdded: () => void
}) {
  const [mode, setMode] = useState<"url" | "file">("url")
  const [remoteUrl, setRemoteUrl] = useState("")
  const [remoteFilename, setRemoteFilename] = useState("")
  const [osType, setOsType] = useState("linux")
  const [integrity, setIntegrity] = useState<ImageIntegrity>(emptyIntegrity)
  const [file, setFile] = useState<File | null>(null)
  const [fileSha, setFileSha] = useState("")
  const [sent, setSent] = useState(0)
  const upload = useUploadISO()
  const [busy, setBusy] = useState(false)

  const resetState = () => {
    if (busy) return
    setRemoteUrl(""); setRemoteFilename(""); setIntegrity(emptyIntegrity); setFile(null); setFileSha(""); setSent(0)
    onClose()
  }

  const fileName = (url: string, given: string) => {
    let name = given.trim()
    if (!name) {
      try {
        const last = new URL(url).pathname.split("/").pop()
        name = last && last.toLowerCase().endsWith(".iso") ? last : "downloaded-image.iso"
      } catch {
        name = "downloaded-image.iso"
      }
    } else if (!name.toLowerCase().endsWith(".iso")) {
      name += ".iso"
    }
    return name
  }

  const submitURL = async () => {
    const problem = integrityProblem(remoteUrl, integrity)
    if (problem) {
      toast.error(problem)
      return
    }
    setBusy(true)
    try {
      await api.post("/api/v1/templates/download-url", {
        url: remoteUrl.trim(), filename: fileName(remoteUrl, remoteFilename), os_type: osType,
        checksum_sha256: integrity.checksumSha256.trim() || undefined, checksum_url: integrity.checksumUrl.trim() || undefined,
        skip_checksum: integrity.skipChecksum,
      })
      toast.success("ISO added. Each node fetches it the first time it is attached")
      onAdded()
      setBusy(false)
      resetState()
    } catch (err) {
      setBusy(false)
      toast.error(`Could not add the ISO: ${(err as Error).message}`)
    }
  }

  const submitFile = async () => {
    if (!file) return
    setBusy(true)
    try {
      await upload.mutateAsync({ file, sha256: fileSha, osType, onProgress: setSent })
      toast.success(`${file.name} uploaded. The panel is copying it to every node`)
      onAdded()
      setBusy(false)
      resetState()
    } catch (err) {
      setBusy(false)
      toast.error(`Upload failed: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) resetState() }}>
      <DialogContent className="max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Upload className="w-6 h-6" aria-hidden="true" />
            Add ISO Image
          </DialogTitle>
          <DialogDescription>Fetch an ISO from an address, or upload one from this computer.</DialogDescription>
        </DialogHeader>

        <div role="tablist" className="flex gap-2 border-b pb-2">
          <Button role="tab" aria-selected={mode === "url"} variant={mode === "url" ? "default" : "ghost"} size="sm" onClick={() => setMode("url")} disabled={busy}>From an address</Button>
          <Button role="tab" aria-selected={mode === "file"} variant={mode === "file" ? "default" : "ghost"} size="sm" onClick={() => setMode("file")} disabled={busy}>Upload a file</Button>
        </div>

        <div className="space-y-4 my-4">
          {mode === "url" ? (
            <>
              <div>
                <label htmlFor="remote-url" className="block text-sm font-medium mb-1">Direct URL</label>
                <Input id="remote-url" placeholder="https://example.com/ubuntu.iso" value={remoteUrl} onChange={(e) => setRemoteUrl(e.target.value)} />
              </div>
              <div>
                <label htmlFor="remote-filename" className="block text-sm font-medium mb-1">Save As (Optional)</label>
                <Input id="remote-filename" placeholder="ubuntu-custom.iso" value={remoteFilename} onChange={(e) => setRemoteFilename(e.target.value)} />
              </div>
              <ImageIntegrityFields value={integrity} onChange={setIntegrity} idPrefix="iso" showFirmware={false} />
            </>
          ) : (
            <>
              <div>
                <label htmlFor="iso-file" className="block text-sm font-medium mb-1">ISO file</label>
                <Input id="iso-file" type="file" accept=".iso" onChange={(e) => { setFile(e.target.files?.[0] ?? null); setSent(0) }} disabled={busy} />
                <p className="text-xs text-muted-foreground mt-1">Kept on the panel and copied to every node, which checks it against its sha256.</p>
              </div>
              <div>
                <label htmlFor="iso-file-sha" className="block text-sm font-medium mb-1">sha256 (optional)</label>
                <Input id="iso-file-sha" value={fileSha} onChange={(e) => setFileSha(e.target.value)} placeholder="Checked once the upload is complete" className="font-mono" disabled={busy} />
              </div>
              {file && busy && (
                <div aria-live="polite">
                  <div className="h-2 rounded bg-muted overflow-hidden">
                    <div className="h-full bg-foreground/70" style={{ width: `${file.size ? Math.round((sent / file.size) * 100) : 0}%` }} />
                  </div>
                  <p className="text-xs text-muted-foreground mt-1 tabular-nums">{formatBytes(sent)} of {formatBytes(file.size)}</p>
                </div>
              )}
            </>
          )}
          <div>
            <label htmlFor="iso-os-type" className="block text-sm font-medium mb-1">System it installs</label>
            <select id="iso-os-type" value={osType} onChange={(e) => setOsType(e.target.value)}
              className="h-10 w-full max-w-xs rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2">
              <option value="linux">Linux or other</option>
              <option value="windows">Windows</option>
            </select>
            <p className="text-xs text-muted-foreground mt-1">A VM installed from this ISO gets virtual hardware for this kind of system.</p>
          </div>
        </div>

        <DialogFooter className="gap-3 sm:gap-0">
          <Button variant="outline" onClick={resetState} disabled={busy}>Cancel</Button>
          {mode === "url" ? (
            <Button disabled={!remoteUrl || busy} onClick={submitURL}>{busy && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Add ISO</Button>
          ) : (
            <Button disabled={!file || busy} onClick={submitFile}>{busy && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Upload</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default function ISOListPage() {
  const confirm = useConfirm()
  const { data: templates, isLoading, error, refetch } = useTemplates({ type: 'iso', includeNodes: true })
  const deleteTemplate = useDeleteTemplate()
  const { data: driversSetting } = useDriversISO()
  const setDrivers = useSetDriversISO()
  const driversID = driversSetting?.drivers_iso_template_id ?? ""
  const toggleDrivers = useCallback(async (id: string) => {
    const next = driversID === id ? "" : id
    try {
      await setDrivers.mutateAsync(next)
      toast.success(next ? "Windows installs can now get this ISO as their drivers disc" : "No drivers disc is set")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }, [driversID, setDrivers])
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
                {iso.image_path.startsWith("upload:") && (
                  <p className="text-xs text-muted-foreground mt-1">
                    On {(iso.nodes ?? []).filter((n) => n.status === "available").length} of {(iso.nodes ?? []).length} nodes
                  </p>
                )}
                {!iso.checksum_sha256 && <p className="text-xs text-amber-700 dark:text-amber-300 mt-1">No checksum</p>}
                {driversID === iso.id && <p className="text-xs font-medium mt-1">Windows drivers disc</p>}
              </div>

              <div className="col-span-2 flex items-center justify-end gap-2">
                <Button variant="ghost" size="sm" onClick={() => toggleDrivers(iso.id)} disabled={setDrivers.isPending}
                  title="Offer this ISO as the drivers disc of Windows installs">
                  {driversID === iso.id ? "Not the drivers disc" : "Drivers disc"}
                </Button>
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
        onAdded={() => refetch()}
      />
    </div>
  )
}