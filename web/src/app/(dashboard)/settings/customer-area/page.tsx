"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useCan } from "@/lib/hooks/use-auth"
import { useSaveSystemSettings, useSystemSettings } from "@/lib/hooks/use-settings"
import type { PortalSettings } from "@/types/team"

const blank: PortalSettings = { billingUrl: "", statusUrl: "", isos: [], isoUrlAllowed: null, ipRequests: "", graphHistoryDays: 0 }

const modes = [
  { value: "approval", label: "Staff decide", detail: "Each request waits under Address requests until someone grants or declines it." },
  { value: "auto", label: "Granted at once", detail: "Assigned straight away when the VM's plan allows another and a pool on its node has room. An IPv4 request on a plan that sets no number of extra addresses still waits for staff." },
  { value: "off", label: "Not offered", detail: "Customers do not see the form; they contact support." },
] as const

// What the customer area offers beyond the panel's defaults: links to the
// operator's own billing and status pages, the ISOs customers may mount, how
// requests for more addresses are handled and how long graphs go back.
export default function CustomerAreaSettingsPage() {
  const can = useCan()
  const { data: settings, isLoading } = useSystemSettings()
  const save = useSaveSystemSettings()
  const [form, setForm] = useState<PortalSettings>(blank)
  useEffect(() => {
    if (settings) setForm({ ...blank, ...(settings.portal as Partial<PortalSettings> | undefined) })
  }, [settings])
  const set = (patch: Partial<PortalSettings>) => setForm((f) => ({ ...f, ...patch }))
  const setISO = (i: number, patch: Partial<PortalSettings["isos"][number]>) => set({ isos: form.isos.map((x, n) => (n === i ? { ...x, ...patch } : x)) })
  const editable = can("settings:update")
  const mode = form.ipRequests || "approval"

  if (isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>

  return (
    <form
      className="max-w-3xl space-y-6"
      onSubmit={async (e) => {
        e.preventDefault()
        try {
          await save.mutateAsync({ portal: form })
          toast.success("Customer area saved")
        } catch (err) {
          toast.error((err as Error).message)
        }
      }}
    >
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Customer area</h1>
        <p className="text-sm text-muted-foreground">What customers find in their area of the panel. The support link and email are set under Branding.</p>
      </div>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4"><h2 className="text-lg font-semibold">Links</h2></div>
        <div className="grid gap-4 p-5 sm:grid-cols-2">
          <div>
            <Label htmlFor="ca-billing">Billing</Label>
            <Input id="ca-billing" type="url" value={form.billingUrl} onChange={(e) => set({ billingUrl: e.target.value })} placeholder="https://billing.example.com/clientarea.php" disabled={!editable} />
            <p className="mt-1 text-xs text-muted-foreground">In the customer menu and beside prices on the order page.</p>
          </div>
          <div>
            <Label htmlFor="ca-status">Service status</Label>
            <Input id="ca-status" type="url" value={form.statusUrl} onChange={(e) => set({ statusUrl: e.target.value })} placeholder="https://status.example.com" disabled={!editable} />
          </div>
        </div>
      </section>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4">
          <h2 className="text-lg font-semibold">ISOs customers can mount</h2>
          <p className="text-sm text-muted-foreground">The node downloads the image from its address the first time someone mounts it.</p>
        </div>
        <div className="space-y-3 p-5">
          {form.isos.length === 0 && <p className="text-sm text-muted-foreground">None listed.</p>}
          {form.isos.map((iso, i) => (
            <div key={iso.id ?? i} className="grid gap-2 rounded-md border p-3 sm:grid-cols-[12rem_1fr_auto]">
              <div>
                <Label htmlFor={`iso-name-${i}`} className="text-xs">Name</Label>
                <Input id={`iso-name-${i}`} value={iso.name} onChange={(e) => setISO(i, { name: e.target.value })} placeholder="Debian 12 installer" disabled={!editable} />
              </div>
              <div>
                <Label htmlFor={`iso-url-${i}`} className="text-xs">Address</Label>
                <Input id={`iso-url-${i}`} type="url" value={iso.url} onChange={(e) => setISO(i, { url: e.target.value })} placeholder="https://cdimage.example.org/debian-12.iso" className="font-mono" disabled={!editable} />
              </div>
              <div className="flex items-end">
                <Button type="button" size="sm" variant="ghost" disabled={!editable} onClick={() => set({ isos: form.isos.filter((_, n) => n !== i) })}>Remove</Button>
              </div>
              <div className="sm:col-span-3">
                <Label htmlFor={`iso-desc-${i}`} className="text-xs">Description</Label>
                <Input id={`iso-desc-${i}`} value={iso.description} onChange={(e) => setISO(i, { description: e.target.value })} placeholder="Network installer, 650 MB" disabled={!editable} />
              </div>
            </div>
          ))}
          <Button type="button" size="sm" variant="outline" disabled={!editable} onClick={() => set({ isos: [...form.isos, { name: "", url: "", description: "" }] })}>Add an ISO</Button>
          <label className="flex items-start gap-2 pt-2 text-sm">
            <input type="checkbox" className="mt-1" checked={form.isoUrlAllowed !== false} disabled={!editable} onChange={(e) => set({ isoUrlAllowed: e.target.checked })} />
            <span>Customers may also mount an ISO from an address of their own
              <span className="block text-xs text-muted-foreground">It must be a public http(s) address; the node refuses private ones and oversized files.</span>
            </span>
          </label>
        </div>
      </section>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4">
          <h2 className="text-lg font-semibold">Requests for more addresses</h2>
          <p className="text-sm text-muted-foreground">Always within what the VM&apos;s plan allows (extra IPv4 and IPv6 on the Plans page).</p>
        </div>
        <fieldset className="space-y-2 p-5">
          <legend className="sr-only">How requests are handled</legend>
          {modes.map((m) => (
            <label key={m.value} className="flex items-start gap-2 text-sm">
              <input type="radio" name="ip-requests" className="mt-1" value={m.value} checked={mode === m.value} disabled={!editable} onChange={() => set({ ipRequests: m.value })} />
              <span><span className="font-medium">{m.label}</span><span className="block text-xs text-muted-foreground">{m.detail}</span></span>
            </label>
          ))}
        </fieldset>
      </section>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4"><h2 className="text-lg font-semibold">Graph history</h2></div>
        <div className="p-5">
          <Label htmlFor="ca-days">Days of hourly averages to keep</Label>
          <Input id="ca-days" type="number" min={8} max={400} value={form.graphHistoryDays || 90} disabled={!editable}
            onChange={(e) => set({ graphHistoryDays: Number(e.target.value) })} className="w-32 tabular-nums" />
          <p className="mt-1 text-xs text-muted-foreground">The last 7 days are kept minute by minute; older readings are averaged by the hour. Default 90.</p>
        </div>
      </section>

      {editable && (
        <Button type="submit" disabled={save.isPending}>
          {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          Save
        </Button>
      )}
    </form>
  )
}
