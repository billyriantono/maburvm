"use client"

import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, LifeBuoy, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useFlash } from "@/lib/use-feedback"

interface RescueStatus {
  /** The image rescue mode boots now; empty when there is none. */
  iso_url: string
  /** settings: saved here; environment: RESCUE_ISO_URL on the panel host. */
  source: "settings" | "environment" | ""
  /** The built-in rescue system's disk image and its checksum; empty when none. */
  image_url?: string
  image_sha256?: string
}

const QUERY_KEY = ["settings", "rescue"]

// The image a server boots when its owner, or staff, turns on rescue mode.
export function RescueImageSettings() {
  const queryClient = useQueryClient()
  const saved = useFlash()
  const { data, isLoading, isError, error, refetch } = useQuery<RescueStatus>({
    queryKey: QUERY_KEY,
    queryFn: async () => (await api.get<RescueStatus>("/api/v1/settings/system/rescue")).data.data,
  })
  const [iso, setIso] = useState("")
  const [image, setImage] = useState("")
  const [imageSum, setImageSum] = useState("")

  useEffect(() => {
    if (data) {
      setIso(data.source === "settings" ? data.iso_url : "")
      setImage(data.image_url ?? "")
      setImageSum(data.image_sha256 ?? "")
    }
  }, [data])

  const save = useMutation({
    mutationFn: async () => {
      await api.put("/api/v1/settings/system", { rescue: { isoUrl: iso.trim(), imageUrl: image.trim(), imageSha256: imageSum.trim() } })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY })
      saved.trigger()
      toast.success("Rescue image saved")
    },
    onError: (e: Error) => toast.error(`Not saved: ${e.message}`),
  })

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle className="flex items-center gap-2">
          <LifeBuoy className="w-5 h-5" />
          Rescue image
        </CardTitle>
        <CardDescription>
          What a server starts in rescue mode. The built-in rescue system boots a fresh copy of a small Linux image on the
          server&apos;s own address, with its disks attached and a one-time root password shown to whoever turned rescue on.
          The rescue ISO is used where the built-in system is not set or its node cannot run it yet. Without either, the
          Enter rescue button tells customers rescue mode is not available.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-6">
        {isLoading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="w-4 h-4 animate-spin" />Loading…</div>
        ) : isError ? (
          <div className="text-sm">
            <p className="text-destructive">Could not load this setting: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : (
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              save.mutate()
            }}
          >
            <div className="space-y-2">
              <label htmlFor="rescueImage" className="block text-sm font-medium text-muted-foreground">Built-in rescue system: disk image address or path</label>
              <Input id="rescueImage" value={image} onChange={(e) => setImage(e.target.value)}
                placeholder="https://example.com/rescue.qcow2" className="font-mono" />
              <label htmlFor="rescueImageSum" className="block text-sm font-medium text-muted-foreground">Its sha256</label>
              <Input id="rescueImageSum" value={imageSum} onChange={(e) => setImageSum(e.target.value)} className="font-mono" />
              <p className="text-xs text-muted-foreground">
                A qcow2 cloud image that uses systemd-networkd or cloud-init (a minimal Debian or Ubuntu cloud image works),
                which no template here is built from, so its disk is never mistaken for the server&apos;s own. A URL needs its
                sha256; a path must be the same on every node.
              </p>
            </div>

            <div className="space-y-2">
              <label htmlFor="rescueIso" className="block text-sm font-medium text-muted-foreground">Rescue ISO address or path</label>
              <Input
                id="rescueIso"
                value={iso}
                onChange={(e) => setIso(e.target.value)}
                placeholder="https://example.com/rescue.iso"
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">
                An http(s) URL each node downloads, or the full path of an ISO already on every node.
              </p>
            </div>

            <p className="flex items-center gap-2 text-sm">
              <span className={`h-2 w-2 rounded-full ${data?.iso_url || data?.image_url ? "bg-success" : "bg-warning"}`} aria-hidden="true" />
              {data?.image_url
                ? "Rescue mode is available, with the built-in rescue system."
                : data?.source === "settings"
                ? "Rescue mode is available."
                : data?.source === "environment"
                  ? <span>Rescue mode uses <span className="font-mono break-all">{data.iso_url}</span> from RESCUE_ISO_URL on the panel host until an image is saved here.</span>
                  : "Rescue mode is not available: no image is set."}
            </p>

            <div className="flex justify-end">
              <Button type="submit" disabled={save.isPending}>
                {save.isPending ? "Saving..." : saved.active() ? <><Check className="w-4 h-4 mr-2" aria-hidden="true" />Saved</> : "Save rescue image"}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}
