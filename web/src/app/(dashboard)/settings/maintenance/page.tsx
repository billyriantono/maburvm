"use client"

import { FormEvent, useEffect, useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { useCheckUpdate, useMaintenance, useSaveUpdateSettings, useSetMaintenance, useUpdateStatus } from "@/lib/hooks/use-admin-extras"

const when = (iso?: string | null) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "")

function MaintenanceCard() {
  const confirm = useConfirm()
  const { data, isLoading, error } = useMaintenance()
  const setMode = useSetMaintenance()
  const [message, setMessage] = useState("")
  const [hold, setHold] = useState(true)
  useEffect(() => {
    if (data?.enabled) setMessage(data.message)
  }, [data?.enabled, data?.message])

  const turn = async (on: boolean) => {
    const ok = await confirm(on
      ? {
        title: "Turn maintenance mode on?",
        description: "Customers and the billing system can look but not change anything until you turn it off; orders wait. Staff carry on. Running VMs are not touched.",
        confirmLabel: "Turn on",
      }
      : { title: "Turn maintenance mode off?", description: "Customers can make changes again and held jobs start.", confirmLabel: "Turn off" })
    if (!ok) return
    try {
      await setMode.mutateAsync({ enabled: on, message, hold_jobs: on && hold })
      toast.success(on ? "Maintenance mode is on" : "Maintenance mode is off")
    } catch (err) {
      toast.error(`Not changed: ${(err as Error).message}`)
    }
  }

  return (
    <section aria-labelledby="maint-heading" className="rounded-lg border bg-card p-5">
      <h2 id="maint-heading" className="text-lg font-semibold">Maintenance mode</h2>
      <p className="mt-1 max-w-prose text-sm text-muted-foreground">
        For upgrades and repairs: customers and the billing system can read but not change anything, and see a banner saying why.
        Holding jobs also stops new jobs from starting, so the panel can restart without cutting a backup or a move short.
        scripts/upgrade-panel.sh does this by itself.
      </p>
      {isLoading ? (
        <p className="mt-4 flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading</p>
      ) : error || !data ? (
        <p className="mt-4 text-sm" role="alert">Could not read maintenance mode: {(error as Error)?.message}</p>
      ) : (
        <div className="mt-4 space-y-4">
          <p className="flex items-center gap-2 text-sm font-medium" role="status">
            <span className={`h-2 w-2 rounded-full ${data.enabled ? "bg-warning" : "bg-success"}`} aria-hidden="true" />
            {data.enabled
              ? `On since ${when(data.since)}${data.by ? ` (${data.by})` : ""}${data.hold_jobs ? ", new jobs held" : ""}`
              : "Off: customers can make changes"}
          </p>
          {data.enabled && data.running_jobs && (
            <div className="text-sm">
              <p className="font-medium">{data.running_jobs.length === 0 ? "No job is running." : `${data.running_jobs.length} still running:`}</p>
              <ul className="mt-1 space-y-0.5 text-muted-foreground">
                {data.running_jobs.map((j) => (
                  <li key={j.id}><span className="font-mono">{j.id}</span> {j.kind} {j.subject}, since {when(j.attempted_at)}</li>
                ))}
              </ul>
              {data.running_jobs.length > 0 && <Link href="/settings/queue" className="text-sm underline underline-offset-4">Job queue</Link>}
            </div>
          )}
          {data.can_switch ? (
            <div className="space-y-3">
              <div>
                <label htmlFor="maint-msg" className="mb-1 block text-sm font-medium">What customers read</label>
                <Input id="maint-msg" value={message} maxLength={500} onChange={(e) => setMessage(e.target.value)}
                  placeholder="The panel is down for maintenance. Changes are paused for now; your servers keep running." />
              </div>
              {!data.enabled && (
                <label className="flex items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-1" checked={hold} onChange={(e) => setHold(e.target.checked)} />
                  <span>Hold jobs<span className="block text-xs text-muted-foreground">Running jobs finish; queued and new ones wait until maintenance ends.</span></span>
                </label>
              )}
              <div className="flex flex-wrap gap-2">
                {data.enabled ? (
                  <>
                    <Button onClick={() => turn(false)} disabled={setMode.isPending}>Turn off</Button>
                    <Button variant="outline" onClick={() => turn(true)} disabled={setMode.isPending}>Save the message</Button>
                  </>
                ) : (
                  <Button variant="outline" onClick={() => turn(true)} disabled={setMode.isPending}>Turn on</Button>
                )}
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">Only the Owner switches maintenance mode.</p>
          )}
        </div>
      )}
    </section>
  )
}

function UpdateCard() {
  const { data, isLoading, error } = useUpdateStatus()
  const save = useSaveUpdateSettings()
  const check = useCheckUpdate()
  const st = data?.status
  const [repo, setRepo] = useState("")
  useEffect(() => setRepo(st?.repository ?? ""), [st?.repository])

  const submit = async (enabled: boolean, e?: FormEvent) => {
    e?.preventDefault()
    try {
      await save.mutateAsync({ enabled, repository: repo })
      if (enabled) await check.mutateAsync()
      toast.success(enabled ? "The panel now checks for new releases daily" : "The update notice is off")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <section aria-labelledby="update-heading" className="rounded-lg border bg-card p-5">
      <h2 id="update-heading" className="text-lg font-semibold">Update notice</h2>
      <p className="mt-1 max-w-prose text-sm text-muted-foreground">
        When on, the panel reads the release list of its GitHub repository once a day and shows the Owner a notice when a newer
        release is out, and whether it fixes a security problem. The request carries nothing about this panel. Off unless turned on.
      </p>
      {isLoading ? (
        <p className="mt-4 flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />Loading</p>
      ) : error || !st ? (
        <p className="mt-4 text-sm" role="alert">Could not read the update notice: {(error as Error)?.message}</p>
      ) : (
        <div className="mt-4 space-y-3 text-sm">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
            <dt className="text-muted-foreground">This panel</dt><dd className="font-mono">{st.current}</dd>
            <dt className="text-muted-foreground">Newest release</dt>
            <dd>
              {st.latest ? <><a href={st.latest.url} target="_blank" rel="noreferrer" className="font-mono underline underline-offset-4">{st.latest.tag}</a>{st.security && <span className="ml-2 font-semibold text-destructive">security fix</span>}</> : "Not checked yet"}
              {st.update_available === false && <span className="ml-2 text-muted-foreground">this panel is up to date</span>}
              {st.update_available === null && st.latest && <span className="ml-2 text-muted-foreground">this build is not a release, so it is not compared</span>}
            </dd>
            <dt className="text-muted-foreground">Last check</dt><dd>{st.checked_at ? when(st.checked_at) : "Never"}{st.error && <span className="block text-destructive">{st.error}</span>}</dd>
          </dl>
          {data.can_manage ? (
            <form onSubmit={(e) => submit(true, e)} className="space-y-3">
              <div>
                <label htmlFor="update-repo" className="mb-1 block font-medium">Repository</label>
                <Input id="update-repo" className="max-w-sm font-mono" value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="owner/maburvm" />
                <p className="mt-1 text-xs text-muted-foreground">The GitHub owner and name the panel is released from, as in its address on github.com.</p>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button type="submit" variant={st.enabled ? "outline" : "default"} disabled={save.isPending || check.isPending || !repo.trim()}>
                  {(save.isPending || check.isPending) && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                  {st.enabled ? "Save and check now" : "Turn on"}
                </Button>
                {st.enabled && <Button type="button" variant="ghost" disabled={save.isPending} onClick={() => submit(false)}>Turn off</Button>}
              </div>
            </form>
          ) : (
            <p className="text-muted-foreground">{st.enabled ? "On." : "Off."} Only the Owner changes it.</p>
          )}
        </div>
      )}
    </section>
  )
}

export default function MaintenancePage() {
  return (
    <div className="max-w-3xl space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Maintenance and updates</h1>
        <p className="mt-1 text-sm text-muted-foreground">Pausing customer changes for an upgrade, and hearing about new releases.</p>
      </div>
      <MaintenanceCard />
      <UpdateCard />
    </div>
  )
}
