"use client"

import Link from "next/link"
import { AlertCircle, Layers, Loader2, Plus, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useDeleteImage, useImages } from "@/lib/hooks/use-images"
import { useVMs } from "@/lib/hooks/use-vms"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import type { Image } from "@/types"

function statusVariant(status: Image["status"]): "success" | "warning" | "destructive" {
  if (status === "available") return "success"
  if (status === "failed") return "destructive"
  return "warning"
}

export default function ImagesPage() {
  const { data: images, isLoading, error, refetch } = useImages()
  const { data: vmsData } = useVMs({ pageSize: 100 })
  const deleteImage = useDeleteImage()

  const vmName = (vmId?: string) => {
    if (!vmId) return "—"
    return vmsData?.data?.find((vm) => vm.id === vmId)?.hostname ?? `${vmId.slice(0, 8)}…`
  }

  const handleDelete = async (image: Image) => {
    if (!window.confirm(`Delete image "${image.name}"? This cannot be undone.`)) return
    try {
      await deleteImage.mutateAsync(image.id)
      toast.success(`Image "${image.name}" deleted`)
    } catch (err) {
      toast.error(`Failed to delete image: ${(err as Error).message}`)
    }
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2">
            <Layers className="w-6 h-6" />
            Images
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            Golden images captured from VMs — they survive VM deletion and can seed new VMs
          </p>
        </div>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6 shadow-sm mb-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-destructive" />
            <div className="flex-1">
              <p className="font-semibold">Error loading images</p>
              <p className="text-sm text-muted-foreground">{(error as Error).message}</p>
            </div>
            <Button variant="outline" size="sm" onClick={() => refetch()} className="gap-1"><RefreshCw className="w-4 h-4" />Retry</Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="flex items-center justify-center py-20"><Loader2 className="w-8 h-8 animate-spin" /><span className="ml-3 text-muted-foreground">Loading images...</span></div>
      ) : !images?.length ? (
        <div className="rounded-lg border bg-card p-12 shadow-sm text-center">
          <Layers className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">No images yet</p>
          <p className="text-sm text-muted-foreground mt-1">
            Capture one from a VM&apos;s detail page (Snapshots tab → Create Image).
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card shadow-sm overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-3">Name</div>
            <div className="col-span-2">Status</div>
            <div className="col-span-1">Size</div>
            <div className="col-span-2">Source VM</div>
            <div className="col-span-2">Created</div>
            <div className="col-span-2 text-right">Actions</div>
          </div>
          {images.map((image) => (
            <div key={image.id} className="p-4 border-b last:border-0">
             <div className="grid grid-cols-12 gap-3 items-center">
              <div className="col-span-3 font-medium text-foreground truncate" title={image.name}>{image.name}</div>
              <div className="col-span-2">
                <Badge variant={statusVariant(image.status)}>{image.status}</Badge>
              </div>
              <div className="col-span-1 text-sm font-mono">{image.size_bytes > 0 ? formatBytes(image.size_bytes) : "—"}</div>
              <div className="col-span-2 text-sm text-muted-foreground truncate">{vmName(image.source_vm_id)}</div>
              <div className="col-span-2 text-sm text-muted-foreground">{new Date(image.created_at).toLocaleString()}</div>
              <div className="col-span-2 flex justify-end items-center gap-1">
                {image.status === "available" && (
                  <Button asChild variant="outline" size="sm" className="gap-1">
                    <Link href={`/vms/new?source_image_id=${image.id}`}>
                      <Plus className="w-4 h-4" />
                      Create VM
                    </Link>
                  </Button>
                )}
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => handleDelete(image)}
                  disabled={deleteImage.isPending}
                  className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                  title="Delete image"
                >
                  <Trash2 className="w-4 h-4" />
                </Button>
              </div>
             </div>
              {image.status === "failed" && image.error_message && (
                <p className="mt-2 text-xs text-destructive break-words whitespace-pre-wrap rounded-md bg-destructive/10 px-3 py-2 font-mono">
                  {image.error_message}
                </p>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
