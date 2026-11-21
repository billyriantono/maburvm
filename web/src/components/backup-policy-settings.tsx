"use client"

import { useEffect, useState } from "react"
import { Check, Clock, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useSaveSystemSettings, useSystemSettings } from "@/lib/hooks/use-settings"
import { useFlash } from "@/lib/use-feedback"

// Matches queue.BackupPolicy in the panel.
interface BackupPolicy {
  window_start?: string
  window_end?: string
  per_node?: number
}

const SECTION = "backup_policy"
const DEFAULT_PER_NODE = 2

// When scheduled backups may start, and how many run on one node at once.
export function BackupPolicySettings() {
  const { data, isLoading, isError, error, refetch } = useSystemSettings()
  const save = useSaveSystemSettings()
  const saved = useFlash()
  const [start, setStart] = useState("")
  const [end, setEnd] = useState("")
  const [perNode, setPerNode] = useState(String(DEFAULT_PER_NODE))

  useEffect(() => {
    const p = (data?.[SECTION] ?? {}) as BackupPolicy
    setStart(p.window_start ?? "")
    setEnd(p.window_end ?? "")
    setPerNode(String(p.per_node || DEFAULT_PER_NODE))
  }, [data])

  const n = Number(perNode)
  const perNodeOK = Number.isInteger(n) && n >= 1 && n <= 20
  const windowOK = (start === "") === (end === "")
  const windowText = !start || !end || start === end
    ? "Scheduled backups start at their own time, at any hour."
    : `Scheduled backups due outside ${start} to ${end} (server time) wait for it, then run once.`

  return (
    <Card className="mt-6">
      <CardHeader className="border-b">
        <CardTitle className="flex items-center gap-2">
          <Clock className="w-5 h-5" />
          Backup window and load
        </CardTitle>
        <CardDescription>
          Each backup reads a whole VM and compresses it on its node, beside the customers&apos; own disk use.
          Schedules and retention are set per VM.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-6">
        {isLoading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="w-4 h-4 animate-spin" />Loading…</div>
        ) : isError ? (
          <div className="text-sm">
            <p className="text-destructive">Could not load these settings: {(error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Try again</Button>
          </div>
        ) : (
          <form
            className="space-y-6"
            onSubmit={(e) => {
              e.preventDefault()
              if (!perNodeOK || !windowOK) return
              save.mutate({ [SECTION]: { window_start: start, window_end: end, per_node: n } }, {
                onSuccess: () => { saved.trigger(); toast.success("Backup settings saved") },
                onError: (err) => toast.error(`Not saved: ${err.message}`),
              })
            }}
          >
            <div className="space-y-2">
              <span className="block text-sm font-medium text-muted-foreground">Backup window</span>
              <div className="flex flex-wrap items-center gap-2">
                <label htmlFor="bw-start" className="sr-only">Window start</label>
                <Input id="bw-start" type="time" value={start} onChange={(e) => setStart(e.target.value)} className="w-32 font-mono" />
                <span className="text-sm text-muted-foreground">to</span>
                <label htmlFor="bw-end" className="sr-only">Window end</label>
                <Input id="bw-end" type="time" value={end} onChange={(e) => setEnd(e.target.value)} className="w-32 font-mono" />
                {(start || end) && (
                  <Button type="button" variant="ghost" size="sm" onClick={() => { setStart(""); setEnd("") }}>Any time</Button>
                )}
              </div>
              {windowOK ? (
                <p className="text-xs text-muted-foreground">{windowText} Backups someone starts by hand are not held back.</p>
              ) : (
                <p className="text-xs text-destructive">Give both a start and an end, or neither.</p>
              )}
            </div>

            <div className="space-y-2">
              <label htmlFor="bw-per-node" className="block text-sm font-medium text-muted-foreground">Backups at once on one node</label>
              <Input id="bw-per-node" inputMode="numeric" value={perNode} onChange={(e) => setPerNode(e.target.value)} className="w-24 font-mono" aria-invalid={!perNodeOK} />
              {perNodeOK ? (
                <p className="text-xs text-muted-foreground">More backups due on the same node wait in turn, and say so on the VM&apos;s Backups tab.</p>
              ) : (
                <p className="text-xs text-destructive">A whole number from 1 to 20.</p>
              )}
            </div>

            <div className="flex justify-end">
              <Button type="submit" disabled={save.isPending || !perNodeOK || !windowOK}>
                {save.isPending ? "Saving..." : saved.active() ? <><Check className="w-4 h-4 mr-2" aria-hidden="true" />Saved</> : "Save backup settings"}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}
