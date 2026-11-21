"use client"

import { useEffect, useState } from "react"
import { Check, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useAbusePolicy, useSaveAbusePolicy } from "@/lib/hooks/use-abuse"
import { useFlash } from "@/lib/use-feedback"
import { ABUSE_ACTION_WORDS, ABUSE_KIND_WORDS, abuseActionsFor, policyProblem } from "@/lib/abuse"
import type { AbusePolicy, AbuseRule } from "@/types"

const RULES = ["outbound_syn", "outbound_pps", "inbound_syn", "inbound_pps"] as const

// What guests are watched for and what the panel does on its own. Every
// automatic action is off until staff choose one.
export function AbusePolicyCard() {
  const { data, isLoading, isError, error, refetch } = useAbusePolicy()
  const save = useSaveAbusePolicy()
  const saved = useFlash()
  const [policy, setPolicy] = useState<AbusePolicy | null>(null)

  useEffect(() => {
    if (data) setPolicy(data)
  }, [data])

  const setRule = (key: (typeof RULES)[number], change: Partial<AbuseRule>) =>
    setPolicy((p) => (p ? { ...p, [key]: { ...p[key], ...change } } : p))
  const problem = policy ? policyProblem(policy) : null

  return (
    <Card className="mt-6">
      <CardHeader className="border-b">
        <CardTitle>Policy</CardTitle>
        <CardDescription>
          Readings are taken once a minute. An action is taken once a guest stays over its threshold for the readings
          below in a row, and only once per episode; release it from the timeline.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-6">
        {isLoading || (!policy && !isError) ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            Loading…
          </div>
        ) : isError || !policy ? (
          <div className="text-sm">
            <p className="text-destructive">Could not load the policy: {(error as Error)?.message}</p>
            <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>
              Try again
            </Button>
          </div>
        ) : (
          <form
            className="space-y-6"
            onSubmit={(e) => {
              e.preventDefault()
              if (problem) return
              save.mutate(policy, {
                onSuccess: () => {
                  saved.trigger()
                  toast.success("Abuse policy saved")
                },
                onError: (err) => toast.error(`Not saved: ${err.message}`),
              })
            }}
          >
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-muted-foreground">
                    <th className="py-2 pr-3 font-medium">Watch</th>
                    <th className="py-2 pr-3 font-medium">Reading</th>
                    <th className="py-2 pr-3 font-medium">Threshold per second</th>
                    <th className="py-2 font-medium">Then</th>
                  </tr>
                </thead>
                <tbody>
                  {RULES.map((key) => {
                    const rule = policy[key]
                    return (
                      <tr key={key} className="border-t">
                        <td className="py-2 pr-3">
                          <input
                            type="checkbox"
                            aria-label={`Watch ${ABUSE_KIND_WORDS[key].toLowerCase()}`}
                            checked={rule.enabled}
                            onChange={(e) => setRule(key, { enabled: e.target.checked })}
                            className="w-4 h-4 accent-primary"
                          />
                        </td>
                        <td className="py-2 pr-3">{ABUSE_KIND_WORDS[key]}</td>
                        <td className="py-2 pr-3">
                          <Input
                            type="number"
                            min={1}
                            aria-label={`${ABUSE_KIND_WORDS[key]} threshold`}
                            className="w-36 tabular-nums"
                            value={rule.threshold}
                            onChange={(e) => setRule(key, { threshold: Number(e.target.value) })}
                          />
                        </td>
                        <td className="py-2">
                          <select
                            aria-label={`${ABUSE_KIND_WORDS[key]} action`}
                            className="h-9 rounded-[4px] border bg-background px-2 text-sm"
                            value={rule.action}
                            onChange={(e) => setRule(key, { action: e.target.value as AbuseRule["action"] })}
                          >
                            {abuseActionsFor(key).map((a) => (
                              <option key={a} value={a}>
                                {ABUSE_ACTION_WORDS[a]}
                              </option>
                            ))}
                          </select>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>

            <div className="grid gap-4 sm:grid-cols-3">
              <label className="space-y-1 text-sm">
                <span className="text-muted-foreground">Readings over before acting</span>
                <Input
                  type="number"
                  min={1}
                  max={30}
                  className="tabular-nums"
                  value={policy.sustain_samples}
                  onChange={(e) => setPolicy({ ...policy, sustain_samples: Number(e.target.value) })}
                />
              </label>
              <label className="space-y-1 text-sm">
                <span className="text-muted-foreground">Null-route for (minutes)</span>
                <Input
                  type="number"
                  min={1}
                  max={10080}
                  className="tabular-nums"
                  value={policy.null_route_minutes}
                  onChange={(e) => setPolicy({ ...policy, null_route_minutes: Number(e.target.value) })}
                />
              </label>
              <label className="space-y-1 text-sm">
                <span className="text-muted-foreground">Rate limit to (Mbps)</span>
                <Input
                  type="number"
                  min={1}
                  className="tabular-nums"
                  value={policy.rate_limit_mbps}
                  onChange={(e) => setPolicy({ ...policy, rate_limit_mbps: Number(e.target.value) })}
                />
              </label>
            </div>

            <label className="flex items-start gap-3 cursor-pointer">
              <input
                type="checkbox"
                checked={policy.notify_owner}
                onChange={(e) => setPolicy({ ...policy, notify_owner: e.target.checked })}
                className="w-4 h-4 mt-0.5 accent-primary"
              />
              <span className="text-sm">
                Email the owner when an action is taken on their server or lifted
                <span className="block text-xs text-muted-foreground">
                  The text is the Network action on a server email under Emails. Staff are told under Alerts.
                </span>
              </span>
            </label>

            <p className="text-xs text-muted-foreground">
              A null-route drops traffic to the attacked addresses on the node only. It protects the node and its other
              guests, not the link into it: for that, ask your upstream to blackhole the address.
            </p>

            {problem && <p className="text-xs text-destructive">{problem}</p>}
            <div className="flex justify-end">
              <Button type="submit" disabled={save.isPending || !!problem}>
                {save.isPending ? (
                  "Saving..."
                ) : saved.active() ? (
                  <>
                    <Check className="w-4 h-4 mr-2" aria-hidden="true" />
                    Saved
                  </>
                ) : (
                  "Save policy"
                )}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}
