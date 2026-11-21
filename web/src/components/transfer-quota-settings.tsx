"use client"

import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Gauge, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useFlash } from "@/lib/use-feedback"

interface TransferSettings {
  enforce: boolean
  /** setting: saved here; environment: BANDWIDTH_ENFORCE on the panel host; default: neither. */
  enforce_source: "setting" | "environment" | "default"
  warn_percents: number[]
}

const QUERY_KEY = ["settings", "bandwidth"]
const ENDPOINT = "/api/v1/settings/system/bandwidth"

const SOURCE_WORDS: Record<TransferSettings["enforce_source"], string> = {
  setting: "Saved here.",
  environment: "Taken from BANDWIDTH_ENFORCE on the panel host until it is saved here.",
  default: "Off until it is turned on here.",
}

// parseLevels reads "80, 100" into sorted levels; null when it cannot.
function parseLevels(text: string): number[] | null {
  const parts = text.split(/[\s,%]+/).filter(Boolean)
  const out: number[] = []
  for (const p of parts) {
    const n = Number(p)
    if (!Number.isInteger(n) || n < 1 || n > 100 || out.includes(n)) return null
    out.push(n)
  }
  return out.length > 5 ? null : out.sort((a, b) => a - b)
}

// What happens around monthly transfer quotas: whether going over restricts a
// VM, and when its owner is warned by email.
export function TransferQuotaSettings() {
  const queryClient = useQueryClient()
  const saved = useFlash()
  const { data, isLoading, isError, error, refetch } = useQuery<TransferSettings>({
    queryKey: QUERY_KEY,
    queryFn: async () => (await api.get<TransferSettings>(ENDPOINT)).data.data,
  })
  const [enforce, setEnforce] = useState(false)
  const [levels, setLevels] = useState("")

  useEffect(() => {
    if (!data) return
    setEnforce(data.enforce)
    setLevels(data.warn_percents.join(", "))
  }, [data])

  const parsed = parseLevels(levels)
  const save = useMutation({
    mutationFn: async () => {
      await api.put(ENDPOINT, { enforce, warnPercents: parsed ?? [] })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY })
      saved.trigger()
      toast.success("Transfer quota settings saved")
    },
    onError: (e: Error) => toast.error(`Not saved: ${e.message}`),
  })

  return (
    <Card className="mt-6">
      <CardHeader className="border-b">
        <CardTitle className="flex items-center gap-2">
          <Gauge className="w-5 h-5" />
          Transfer quotas
        </CardTitle>
        <CardDescription>
          A plan&apos;s monthly transfer quota and what happens past it are set on the plan. This decides
          whether that happens at all, and when owners are warned.
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
              if (parsed) save.mutate()
            }}
          >
            <label className="flex items-start gap-3 cursor-pointer p-4 rounded-md border bg-muted">
              <input type="checkbox" checked={enforce} onChange={(e) => setEnforce(e.target.checked)} className="w-5 h-5 mt-0.5 accent-primary" />
              <div>
                <span className="font-medium">Restrict VMs that use up their quota</span>
                <p className="text-xs text-muted-foreground">
                  When this is off, a plan set to slow down or stop a VM past its quota does neither; overage is
                  still billed. {data && SOURCE_WORDS[data.enforce_source]}
                </p>
              </div>
            </label>

            <div className="space-y-2">
              <label htmlFor="warnPercents" className="block text-sm font-medium text-muted-foreground">Warn owners at (percent of the quota)</label>
              <Input id="warnPercents" value={levels} onChange={(e) => setLevels(e.target.value)} placeholder="No warnings" aria-invalid={!parsed} />
              {parsed ? (
                <p className="text-xs text-muted-foreground">
                  {parsed.length
                    ? `The owner is emailed once per level each month, at ${parsed.map((p) => `${p}%`).join(" and ")}. The text is the Transfer usage warning under Emails.`
                    : "Owners get no warnings."}
                </p>
              ) : (
                <p className="text-xs text-destructive">Give up to five different whole numbers from 1 to 100, such as 80, 100.</p>
              )}
            </div>

            <div className="flex justify-end">
              <Button type="submit" disabled={save.isPending || !parsed}>
                {save.isPending ? "Saving..." : saved.active() ? <><Check className="w-4 h-4 mr-2" aria-hidden="true" />Saved</> : "Save transfer settings"}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}
