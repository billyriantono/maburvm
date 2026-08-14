"use client"

import Link from "next/link"
import { AlertCircle, Layers, Loader2, Plus, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useDeleteImage, useImages, useRetryImage } from "@/lib/hooks/use-images"
import { useVMs } from "@/lib/hooks/use-vms"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import type { Image } from "@/types"
import { useConfirm } from "@/components/confirm-provider"

function statusVariant(status: Image["status"]): "success" | "warning" | "destructive" {
  if (status === "available") return "success"
  if (status === "failed") return "destructive"
  return "warning"
}

// ExportProgressRow shows what a running capture has actually produced.
//
// No percentage bar, on purpose. The output is compressed, so its size against
// the source disk is the compression ratio and not completion — a bar drawn from
// that would move at a rate nobody could interpret and would routinely stop far
// short of the end. Bytes written, elapsed time and current rate answer the
// question actually being asked ("is this moving?") without inventing precision.
function ExportProgressRow({ progress }: { progress: NonNullable<Image["progress"]> }) {
  const mins = Math.floor(progress.elapsed_seconds / 60)
  const elapsed = mins >= 60 ? `${Math.floor(mins / 60)}h ${mins % 60}m` : `${mins}m`
  const rate = progress.bytes_per_second > 0 ? `${formatBytes(progress.bytes_per_second)}/s` : "starting…"

  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span className="flex items-center gap-2">
        <Loader2 className="w-3.5 h-3.5 animate-spin shrink-0" />
        <span className="font-mono text-foreground">{formatBytes(progress.written_bytes)}</span> written
        {progress.source_bytes > 0 && (
          <> from a <span className="font-mono">{formatBytes(progress.source_bytes)}</span> disk</>
        )}
      </span>
      <span>· {elapsed} elapsed</span>
      <span>· {rate}</span>
    </div>
  )
}

export default function ImagesPage() {
  const confirm = useConfirm()
  const { data: images, isLoading, error, refetch } = useImages()
  const { data: vmsData } = useVMs({ pageSize: 100 })
  const deleteImage = useDeleteImage()
  const retryImage = useRetryImage()

  const vmName = (vmId?: string) => {
    if (!vmId) return "—"
    return vmsData?.data?.find((vm) => vm.id === vmId)?.hostname ?? `${vmId.slice(0, 8)}…`
  }

  const handleDelete = async (image: Image) => {
    const ok = await confirm({
      title: `Delete image "${image.name}"?`,
      description: "The stored disk image is removed. This cannot be undone.",
      confirmLabel: "Delete image",
      destructive: true,
      action: () => deleteImage.mutateAsync(image.id),
    })
    if (!ok) return
    toast.success(`Image "${image.name}" deleted`)
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
                {/* A capture that failed — including one interrupted by a panel
                    restart — can be run again on the same row, rather than
                    forcing the operator to delete it and start over. */}
                {image.status === "failed" && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="gap-1"
                    disabled={retryImage.isPending}
                    onClick={async () => {
                      try {
                        await retryImage.mutateAsync(image.id)
                        toast.success(`Capture of "${image.name}" restarted`)
                      } catch (err) {
                        toast.error((err as Error).message)
                      }
                    }}
                  >
                    <RefreshCw className="w-4 h-4" />
                    Retry
                  </Button>
                )}
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
              {image.progress && <ExportProgressRow progress={image.progress} />}
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
