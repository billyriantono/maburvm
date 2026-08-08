"use client"

import { useState } from "react"
import { AlertTriangle, Loader2, ShieldAlert, ShieldCheck } from "lucide-react"
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
import { useAbuseGuests, useSetQuarantine } from "@/lib/hooks/use-abuse"
import type { GuestConnection } from "@/types"

// Abuse is measured in new connections per second, not bytes. A guest running a
// port scan is trivial in bandwidth and enormous in connections, so a traffic
// graph stays flat while the node's connection tracking table fills — at which
// point the node refuses new connections for every VM on it.
export default function AbusePage() {
  const [showAll, setShowAll] = useState(false)
  const { data, isLoading } = useAbuseGuests(showAll)
  const setQuarantine = useSetQuarantine()

  const [target, setTarget] = useState<GuestConnection | null>(null)
  const [reason, setReason] = useState("")

  const guests = data?.guests ?? []
  const threshold = data?.threshold ?? 0

  const handleQuarantine = async () => {
    if (!target) return
    try {
      await setQuarantine.mutateAsync({
        node_id: target.node_id,
        mac: target.mac,
        quarantined: true,
        reason: reason.trim(),
      })
      toast.success(`${target.mac} taken off the network`)
      setTarget(null)
      setReason("")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  const handleRelease = async (guest: GuestConnection) => {
    if (!window.confirm(`Put ${guest.mac} back on the network?`)) return
    try {
      await setQuarantine.mutateAsync({
        node_id: guest.node_id,
        mac: guest.mac,
        quarantined: false,
      })
      toast.success(`${guest.mac} restored`)
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold flex items-center gap-2">
            <ShieldAlert className="w-6 h-6" />
            Abuse
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            Guests opening new outbound connections faster than a real workload does — usually a
            machine that has been compromised. Above {threshold}/s the node is already dropping the
            excess.
          </p>
        </div>
        <Button type="button" variant="outline" onClick={() => setShowAll(!showAll)}>
          {showAll ? "Show flagged only" : "Show every guest"}
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="w-8 h-8 animate-spin" />
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
              : "No guest is opening connections fast enough to threaten its node."}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-3">Guest</div>
            <div className="col-span-2">Node</div>
            <div className="col-span-2 text-right">New conn/s</div>
            <div className="col-span-2 text-right">Peak</div>
            <div className="col-span-1">State</div>
            <div className="col-span-2 text-right">Actions</div>
          </div>
          {guests.map((g) => (
            <div
              key={g.id}
              className="grid grid-cols-12 gap-3 items-center p-4 border-b last:border-0"
            >
              <div className="col-span-3 min-w-0">
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
              </div>
              <div className="col-span-2 text-sm truncate">{g.node_name || "—"}</div>
              <div
                className={`col-span-2 text-right font-mono ${
                  g.syn_rate >= threshold ? "text-red-600 font-semibold" : ""
                }`}
              >
                {Math.round(g.syn_rate).toLocaleString()}
              </div>
              <div className="col-span-2 text-right font-mono text-sm text-muted-foreground">
                {Math.round(g.peak_rate).toLocaleString()}
              </div>
              <div className="col-span-1">
                {g.quarantined ? (
                  <Badge variant="destructive">cut off</Badge>
                ) : g.first_flagged_at ? (
                  <Badge variant="warning">flagged</Badge>
                ) : (
                  <Badge variant="success">ok</Badge>
                )}
              </div>
              <div className="col-span-2 flex justify-end">
                {g.quarantined ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={setQuarantine.isPending}
                    onClick={() => handleRelease(g)}
                  >
                    Restore
                  </Button>
                ) : (
                  <Button
                    type="button"
                    variant="destructive"
                    size="sm"
                    disabled={setQuarantine.isPending}
                    onClick={() => setTarget(g)}
                  >
                    Cut off
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={!!target} onOpenChange={(o) => !o && setTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Cut {target?.mac} off the network</DialogTitle>
            <DialogDescription>
              The machine keeps running — its console and its data are untouched. Only its traffic
              is dropped, so an owner whose server was compromised does not lose anything while you
              work out what happened.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="q-reason">Reason</Label>
            <Input
              id="q-reason"
              placeholder="outbound port scan, ~3000 conn/s"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Recorded on the node itself and in the audit log, so whoever reads it next knows why
              this guest is offline.
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setTarget(null)}>
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={handleQuarantine}
              disabled={setQuarantine.isPending}
            >
              {setQuarantine.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : "Cut off"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
