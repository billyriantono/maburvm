"use client"

import { useState } from "react"
import { AlertCircle, AlertTriangle, Loader2, ShieldAlert, ShieldCheck } from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  useAbuseEvents,
  useAbuseGuests,
  useNullRoute,
  useReleaseAbuseEvent,
  useSetQuarantine,
} from "@/lib/hooks/use-abuse"
import { useNodes } from "@/lib/hooks/use-nodes"
import { IPHistoryLookup } from "@/components/ip-history-lookup"
import { AbusePolicyCard } from "@/components/abuse-policy"
import {
  ABUSE_ACTION_WORDS,
  ABUSE_KIND_WORDS,
  ABUSE_STATE_WORDS,
  abuseUnit,
  releasable,
  sampleValue,
} from "@/lib/abuse"
import type { AbuseEvent, GuestConnection } from "@/types"

// Abuse is measured in connections and packets per second, not bytes. A guest
// running a port scan is trivial in bandwidth and enormous in connections, so a
// traffic graph stays flat while the node's connection tracking table fills —
// at which point the node refuses new connections for every VM on it.

// guestLabel names a guest the way a person would refer to it. Falls back to the
// MAC only when there is genuinely nothing better — a guest the panel does not
// manage has no hostname, and pretending otherwise would be worse than the raw
// address.
function guestLabel(g: { vm_hostname: string; mac: string }): string {
  return g.vm_hostname || (g.mac ? `unmanaged guest ${g.mac}` : "address")
}

const n = (v: number) => Math.round(v).toLocaleString()
const when = (iso: string) => new Date(iso).toLocaleString()

export default function AbusePage() {
  const [showAll, setShowAll] = useState(false)
  const [openOnly, setOpenOnly] = useState(true)
  const { data, isLoading, error, refetch } = useAbuseGuests(showAll)
  const events = useAbuseEvents(openOnly)
  const setQuarantine = useSetQuarantine()
  const release = useReleaseAbuseEvent()

  // One dialog for both directions. Cutting a guest off and putting it back are
  // the same decision seen from two sides, and both need the machine named:
  // nobody recognises a customer by MAC address.
  const [target, setTarget] = useState<{ guest: GuestConnection; mode: "cut" | "restore" } | null>(
    null
  )
  const [reason, setReason] = useState("")
  const [nullRouteFor, setNullRouteFor] = useState<{ nodeId: string; label: string } | null>(null)
  const [releasing, setReleasing] = useState<AbuseEvent | null>(null)

  const guests = data?.guests ?? []
  const policy = data?.policy
  const synOut = policy?.outbound_syn.threshold ?? data?.threshold ?? 0
  const over = (value: number, rule?: { enabled: boolean; threshold: number }) =>
    !!rule && rule.enabled && value >= rule.threshold

  const handleConfirm = async () => {
    if (!target) return
    const { guest, mode } = target
    try {
      await setQuarantine.mutateAsync({
        node_id: guest.node_id,
        mac: guest.mac,
        quarantined: mode === "cut",
        reason: mode === "cut" ? reason.trim() : "",
      })
      toast.success(
        mode === "cut"
          ? `${guestLabel(guest)} taken off the network`
          : `${guestLabel(guest)} is back on the network`
      )
      setTarget(null)
      setReason("")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const rate = (value: number, rule?: { enabled: boolean; threshold: number }) => (
    <span className={`font-mono tabular-nums ${over(value, rule) ? "text-red-600 font-semibold" : ""}`}>
      {n(value)}
    </span>
  )

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold flex items-center gap-2">
            <ShieldAlert className="w-6 h-6" />
            Abuse
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            Every guest&apos;s new connections and packets per second, in and out. A guest sending far
            more than a real workload has usually been compromised; one receiving a flood is under
            attack. Above {synOut} new connections a second out, the node already drops the excess.
          </p>
        </div>
        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={() => setNullRouteFor({ nodeId: "", label: "" })}>
            Null-route an address
          </Button>
          <Button type="button" variant="outline" onClick={() => setShowAll(!showAll)}>
            {showAll ? "Show flagged only" : "Show every guest"}
          </Button>
        </div>
      </div>

      <IPHistoryLookup />

      {isLoading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
        </div>
      ) : error ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <AlertCircle className="w-16 h-16 text-destructive mx-auto mb-4" aria-hidden="true" />
          <h2 className="text-xl font-semibold mb-2">Failed to load abuse data</h2>
          <p className="text-muted-foreground mb-6">{(error as Error).message}</p>
          <Button onClick={() => refetch()}>Retry</Button>
        </div>
      ) : !guests.length ? (
        <div className="rounded-lg border bg-card p-12 text-center">
          <ShieldCheck className="w-16 h-16 text-emerald-500/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">
            {showAll ? "No guests reported" : "Nothing flagged"}
          </p>
          <p className="text-sm text-muted-foreground mt-1">
            {showAll
              ? "Nodes report this on each metrics tick; a node that is offline reports nothing."
              : "No guest is over a threshold of the policy below."}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted text-muted-foreground text-xs">
              <tr>
                <th className="p-3 text-left font-medium">Guest</th>
                <th className="p-3 text-left font-medium">Node</th>
                <th className="p-3 text-right font-medium">New conn/s out</th>
                <th className="p-3 text-right font-medium">New conn/s in</th>
                <th className="p-3 text-right font-medium">Packets/s out</th>
                <th className="p-3 text-right font-medium">Packets/s in</th>
                <th className="p-3 text-left font-medium">State</th>
                <th className="p-3 text-right font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {guests.map((g) => (
                <tr key={g.id} className="border-t">
                  <td className="p-3 min-w-0">
                    {/* The hostname is missing precisely when the guest is one the
                        panel does not manage — the case most worth noticing, so it
                        is labelled rather than left blank. */}
                    <div className="font-medium truncate">
                      {g.vm_hostname || (
                        <span className="text-amber-600 inline-flex items-center gap-1">
                          <AlertTriangle className="w-3.5 h-3.5" />
                          Unmanaged guest
                        </span>
                      )}
                    </div>
                    <div className="font-mono text-xs text-muted-foreground truncate">
                      {g.mac}
                      {g.interface_name ? ` · ${g.interface_name}` : ""}
                    </div>
                  </td>
                  <td className="p-3 truncate">{g.node_name || "—"}</td>
                  <td className="p-3 text-right">
                    {rate(g.syn_rate, policy?.outbound_syn)}
                    <div className="text-xs text-muted-foreground tabular-nums">peak {n(g.peak_rate)}</div>
                  </td>
                  <td className="p-3 text-right">{rate(g.syn_in_rate ?? 0, policy?.inbound_syn)}</td>
                  <td className="p-3 text-right">{rate(g.pps_out ?? 0, policy?.outbound_pps)}</td>
                  <td className="p-3 text-right">{rate(g.pps_in ?? 0, policy?.inbound_pps)}</td>
                  <td className="p-3">
                    {g.quarantined ? (
                      <Badge variant="destructive">cut off</Badge>
                    ) : g.first_flagged_at ? (
                      <Badge variant="warning">flagged</Badge>
                    ) : (
                      <Badge variant="success">ok</Badge>
                    )}
                  </td>
                  <td className="p-3">
                    <div className="flex justify-end gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => setNullRouteFor({ nodeId: g.node_id, label: guestLabel(g) })}
                      >
                        Null-route
                      </Button>
                      {g.quarantined ? (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={setQuarantine.isPending}
                          onClick={() => setTarget({ guest: g, mode: "restore" })}
                        >
                          Restore
                        </Button>
                      ) : (
                        <Button
                          type="button"
                          variant="destructive"
                          size="sm"
                          disabled={setQuarantine.isPending}
                          onClick={() => setTarget({ guest: g, mode: "cut" })}
                        >
                          Cut off
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <section className="mt-8" aria-labelledby="abuse-timeline">
        <div className="flex items-center justify-between gap-4 mb-3">
          <h2 id="abuse-timeline" className="text-lg font-semibold">
            Timeline
          </h2>
          <Button type="button" variant="outline" size="sm" onClick={() => setOpenOnly(!openOnly)}>
            {openOnly ? "Show the whole history" : "Show open episodes only"}
          </Button>
        </div>
        {events.isLoading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            Loading…
          </div>
        ) : events.error ? (
          <div className="text-sm">
            <p className="text-destructive">Could not load the timeline: {(events.error as Error).message}</p>
            <Button size="sm" variant="outline" className="mt-2" onClick={() => events.refetch()}>
              Try again
            </Button>
          </div>
        ) : !events.data?.length ? (
          <p className="rounded-lg border bg-card p-6 text-sm text-muted-foreground">
            {openOnly
              ? "Nothing going on. Each time a guest goes over a threshold, or staff act on one, it appears here."
              : "No episodes recorded yet."}
          </p>
        ) : (
          <ol className="rounded-lg border bg-card divide-y">
            {events.data.map((e) => (
              <TimelineRow key={e.id} event={e} onRelease={() => setReleasing(e)} />
            ))}
          </ol>
        )}
      </section>

      <AbusePolicyCard />

      <Dialog open={!!target} onOpenChange={(o) => !o && setTarget(null)}>
        <DialogContent>
          {target && (
            <>
              <DialogHeader>
                {/* Named, not addressed. An operator recognises
                    "mail.example.com"; nobody recognises 00:16:3e:07:32:2c,
                    and a dialog that only shows a MAC is an invitation to act on
                    the wrong customer. */}
                <DialogTitle>
                  {target.mode === "cut" ? "Cut off " : "Restore "}
                  {guestLabel(target.guest)}
                </DialogTitle>
                <DialogDescription>
                  {target.mode === "cut"
                    ? "The machine keeps running — its console and its data are untouched. Only its traffic is dropped, so an owner whose server was compromised does not lose anything while you work out what happened."
                    : "Its traffic starts flowing again immediately. If the machine is still compromised it will resume whatever it was doing, so check before you restore it."}
                </DialogDescription>
              </DialogHeader>

              <div className="rounded-md border bg-muted/40 p-3 space-y-1 text-sm">
                <div className="flex justify-between gap-3">
                  <span className="text-muted-foreground">Node</span>
                  <span className="font-medium">{target.guest.node_name || "—"}</span>
                </div>
                <div className="flex justify-between gap-3">
                  <span className="text-muted-foreground">MAC</span>
                  <span className="font-mono text-xs">{target.guest.mac}</span>
                </div>
                {target.guest.interface_name && (
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">Interface</span>
                    <span className="font-mono text-xs">{target.guest.interface_name}</span>
                  </div>
                )}
                <div className="flex justify-between gap-3">
                  <span className="text-muted-foreground">New connections/s out</span>
                  <span className="tabular-nums">
                    {n(target.guest.syn_rate)} now · {n(target.guest.peak_rate)} peak
                  </span>
                </div>
                <div className="flex justify-between gap-3">
                  <span className="text-muted-foreground">Packets/s out</span>
                  <span className="tabular-nums">{n(target.guest.pps_out ?? 0)}</span>
                </div>
              </div>

              {target.mode === "cut" ? (
                <div className="space-y-2">
                  <Label htmlFor="q-reason">Reason</Label>
                  <Input
                    id="q-reason"
                    placeholder="outbound port scan, ~3000 conn/s"
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    Recorded on the node itself, on the timeline and in the audit log, so whoever reads
                    it next knows why this guest is offline.
                  </p>
                </div>
              ) : (
                target.guest.quarantine_reason && (
                  <div className="space-y-1">
                    <Label>Cut off because</Label>
                    <p className="text-sm text-muted-foreground">
                      {target.guest.quarantine_reason}
                    </p>
                  </div>
                )
              )}

              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setTarget(null)}>
                  Cancel
                </Button>
                <Button
                  type="button"
                  variant={target.mode === "cut" ? "destructive" : "default"}
                  onClick={handleConfirm}
                  disabled={setQuarantine.isPending}
                >
                  {setQuarantine.isPending ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : target.mode === "cut" ? (
                    "Cut off"
                  ) : (
                    "Restore"
                  )}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>

      <NullRouteDialog target={nullRouteFor} onClose={() => setNullRouteFor(null)} defaultMinutes={policy?.null_route_minutes ?? 30} />

      <Dialog open={!!releasing} onOpenChange={(o) => !o && setReleasing(null)}>
        <DialogContent>
          {releasing && (
            <>
              <DialogHeader>
                <DialogTitle>
                  Release the {ABUSE_ACTION_WORDS[releasing.action].toLowerCase()} on {guestLabel(releasing)}
                </DialogTitle>
                <DialogDescription>
                  {releasing.action === "null_route"
                    ? `Traffic to ${releasing.targets.split(",").join(", ")} flows again at once. If the attack is still going on, the node takes the full flood again.`
                    : releasing.action === "rate_limit"
                      ? "The server goes back to its usual speed at once."
                      : "Its traffic starts flowing again at once. If the machine is still compromised it will resume whatever it was doing."}{" "}
                  This episode is not acted on again; a new one starts once the guest has been quiet.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setReleasing(null)}>
                  Cancel
                </Button>
                <Button
                  type="button"
                  disabled={release.isPending}
                  onClick={() =>
                    release.mutate(releasing.id, {
                      onSuccess: () => {
                        toast.success("Released")
                        setReleasing(null)
                      },
                      onError: (err) => toast.error(err.message),
                    })
                  }
                >
                  {release.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Release"}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function TimelineRow({ event: e, onRelease }: { event: AbuseEvent; onRelease: () => void }) {
  const unit = abuseUnit(e.kind)
  const stateVariant =
    e.action_state === "active" ? "destructive" : e.action_state === "failed" ? "warning" : "secondary"
  return (
    <li className="p-4 text-sm">
      <div className="flex flex-col md:flex-row md:items-start md:justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="font-medium">
            {guestLabel(e)} <span className="text-muted-foreground font-normal">on {e.node_name || "an unknown node"}</span>
          </div>
          <div className="text-muted-foreground">
            {ABUSE_KIND_WORDS[e.kind]}
            {e.kind !== "manual" && (
              <>
                {": "}
                <span className="tabular-nums text-foreground">{n(e.peak)}</span> {unit} at peak, over{" "}
                <span className="tabular-nums">{n(e.threshold)}</span> for {e.samples} reading{e.samples === 1 ? "" : "s"}
              </>
            )}
          </div>
          <div className="text-xs text-muted-foreground">
            {when(e.started_at)}
            {e.ended_at ? ` until ${when(e.ended_at)}` : " · going on"}
            {e.expires_at && e.action_state === "active" ? ` · lifts itself ${when(e.expires_at)}` : ""}
            {e.released_at ? ` · released ${when(e.released_at)}${e.released_by ? ` by ${e.released_by}` : ""}` : ""}
          </div>
          {e.targets && <div className="font-mono text-xs">{e.targets.split(",").join(", ")}</div>}
          {e.detail && <div className="text-xs">{e.detail}</div>}
          {!!e.evidence?.length && e.kind !== "manual" && (
            <details className="text-xs">
              <summary className="cursor-pointer text-muted-foreground">Readings ({e.evidence.length})</summary>
              <table className="mt-2 tabular-nums">
                <tbody>
                  {e.evidence.map((s) => (
                    <tr key={s.at}>
                      <td className="pr-4 text-muted-foreground">{when(s.at)}</td>
                      <td className="text-right">
                        {n(sampleValue(e.kind, s))} {unit}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </details>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <span className="text-muted-foreground">{ABUSE_ACTION_WORDS[e.action]}</span>
          {e.action !== "alert" && <Badge variant={stateVariant}>{ABUSE_STATE_WORDS[e.action_state]}</Badge>}
          {releasable(e) && (
            <Button type="button" size="sm" variant="outline" onClick={onRelease}>
              Release
            </Button>
          )}
        </div>
      </div>
    </li>
  )
}

function NullRouteDialog({
  target,
  onClose,
  defaultMinutes,
}: {
  target: { nodeId: string; label: string } | null
  onClose: () => void
  defaultMinutes: number
}) {
  const { data: nodes } = useNodes(!!target)
  const nullRoute = useNullRoute()
  const [nodeId, setNodeId] = useState("")
  const [ip, setIP] = useState("")
  const [minutes, setMinutes] = useState(defaultMinutes)
  const [reason, setReason] = useState("")
  const [opened, setOpened] = useState<typeof target>(null)
  if (target !== opened) {
    // A fresh form each time the dialog opens, on the guest's node when there is one.
    setOpened(target)
    setNodeId(target?.nodeId ?? "")
    setIP("")
    setMinutes(defaultMinutes)
    setReason("")
  }
  const valid = !!nodeId && ip.trim() !== "" && Number.isInteger(minutes) && minutes >= 1 && minutes <= 10080

  return (
    <Dialog open={!!target} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Null-route {target?.label ? `an address of ${target.label}` : "an address"}</DialogTitle>
          <DialogDescription>
            The node drops all traffic to the address until the time is up, then lifts it by itself. Whatever
            answers on it is unreachable meanwhile; the server itself keeps running.
          </DialogDescription>
        </DialogHeader>
        <form
          id="null-route-form"
          className="space-y-4"
          onSubmit={(ev) => {
            ev.preventDefault()
            if (!valid) return
            nullRoute.mutate(
              { node_id: nodeId, ip: ip.trim(), minutes, reason: reason.trim() },
              {
                onSuccess: () => {
                  toast.success(`${ip.trim()} null-routed for ${minutes} minutes`)
                  onClose()
                },
                onError: (err) => toast.error(err.message),
              }
            )
          }}
        >
          <div className="space-y-2">
            <Label htmlFor="nr-node">Node</Label>
            <select
              id="nr-node"
              className="h-9 w-full rounded-[4px] border bg-background px-2 text-sm"
              value={nodeId}
              onChange={(e) => setNodeId(e.target.value)}
            >
              <option value="">Choose the node the address is on</option>
              {(nodes ?? []).map((node) => (
                <option key={node.id} value={node.id}>
                  {node.name}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="nr-ip">Address</Label>
            <Input id="nr-ip" className="font-mono" placeholder="203.0.113.24" value={ip} onChange={(e) => setIP(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="nr-minutes">For (minutes)</Label>
            <Input
              id="nr-minutes"
              type="number"
              min={1}
              max={10080}
              className="tabular-nums"
              value={minutes}
              onChange={(e) => setMinutes(Number(e.target.value))}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="nr-reason">Reason</Label>
            <Input id="nr-reason" placeholder="UDP flood, ~400k packets/s" value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
        </form>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="null-route-form" variant="destructive" disabled={!valid || nullRoute.isPending}>
            {nullRoute.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Null-route"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
