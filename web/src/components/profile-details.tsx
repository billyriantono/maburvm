"use client"

import { useEffect, useMemo, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useProfile, useUpdateProfile } from "@/lib/hooks/use-settings"

const LOCALES = [
  { value: "", label: "Follow the browser" },
  { value: "en", label: "English" },
  { value: "en-GB", label: "English (United Kingdom)" },
  { value: "id-ID", label: "Bahasa Indonesia" },
  { value: "de-DE", label: "Deutsch" },
  { value: "es-ES", label: "Español" },
  { value: "fr-FR", label: "Français" },
  { value: "ja-JP", label: "日本語" },
]

const field = "h-10 w-full rounded-md border border-input bg-background px-3 text-sm"

// The account holder's own details: who they are and how dates and times are
// shown to them. The email address and password have their own forms.
export function ProfileDetails({ disabled }: { disabled?: boolean }) {
  const { data: profile } = useProfile()
  const save = useUpdateProfile()
  const [form, setForm] = useState({ name: "", phone: "", company: "", timezone: "", locale: "" })
  useEffect(() => {
    if (profile) setForm({ name: profile.name ?? "", phone: profile.phone ?? "", company: profile.company ?? "", timezone: profile.timezone ?? "", locale: profile.locale ?? "" })
  }, [profile])
  const zones = useMemo(() => {
    try {
      return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? []
    } catch {
      return []
    }
  }, [])
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setForm((f) => ({ ...f, [k]: e.target.value }))
  const changed = !!profile && (["name", "phone", "company", "timezone", "locale"] as const).some((k) => (profile[k] ?? "") !== form[k])

  return (
    <section className="rounded-lg border bg-card text-card-foreground">
      <div className="border-b px-5 py-4">
        <h2 className="text-lg font-semibold">Your details</h2>
        <p className="text-sm text-muted-foreground">Signed in as {profile?.email ?? "…"}.</p>
      </div>
      <form
        className="grid gap-4 p-5 sm:grid-cols-2"
        onSubmit={async (e) => {
          e.preventDefault()
          try {
            await save.mutateAsync(form)
            toast.success("Details saved")
          } catch (err) {
            toast.error((err as Error).message)
          }
        }}
      >
        <div>
          <Label htmlFor="pd-name">Name</Label>
          <Input id="pd-name" value={form.name} onChange={set("name")} autoComplete="name" maxLength={255} disabled={disabled} />
        </div>
        <div>
          <Label htmlFor="pd-company">Company</Label>
          <Input id="pd-company" value={form.company} onChange={set("company")} autoComplete="organization" maxLength={255} disabled={disabled} />
        </div>
        <div>
          <Label htmlFor="pd-phone">Phone</Label>
          <Input id="pd-phone" type="tel" value={form.phone} onChange={set("phone")} autoComplete="tel" maxLength={40} disabled={disabled} />
        </div>
        <div>
          <Label htmlFor="pd-tz">Time zone</Label>
          {zones.length ? (
            <select id="pd-tz" value={form.timezone} onChange={set("timezone")} className={field} disabled={disabled}>
              <option value="">Follow the browser</option>
              {zones.map((z) => <option key={z} value={z}>{z.replace(/_/g, " ")}</option>)}
            </select>
          ) : (
            <Input id="pd-tz" value={form.timezone} onChange={set("timezone")} placeholder="Asia/Jakarta" disabled={disabled} />
          )}
        </div>
        <div>
          <Label htmlFor="pd-locale">Language and date format</Label>
          <select id="pd-locale" value={form.locale} onChange={set("locale")} className={field} disabled={disabled}>
            {LOCALES.map((l) => <option key={l.value} value={l.value}>{l.label}</option>)}
            {form.locale && !LOCALES.some((l) => l.value === form.locale) && <option value={form.locale}>{form.locale}</option>}
          </select>
          <p className="mt-1 text-xs text-muted-foreground">The panel itself is in English; this sets how dates and numbers are written.</p>
        </div>
        <div className="flex items-end sm:col-span-2">
          <Button type="submit" disabled={disabled || !changed || save.isPending}>
            {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
            Save details
          </Button>
        </div>
      </form>
    </section>
  )
}
