"use client"

import { useState } from "react"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useChangePassword } from "@/lib/hooks/use-settings"
import { useMyQuota } from "@/lib/hooks/use-quota"

function quotaLabel(used: number, max: number, unit: string) {
  return `${used}${unit} / ${max === 0 ? "∞" : `${max}${unit}`}`
}

export default function ClientProfilePage() {
  const { data: user } = useCurrentUser()
  const { data: quota } = useMyQuota()
  const changePassword = useChangePassword()

  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)

  const submit = () => {
    setMsg(null)
    if (next.length < 8) {
      setMsg({ ok: false, text: "New password must be at least 8 characters." })
      return
    }
    if (next !== confirm) {
      setMsg({ ok: false, text: "New password and confirmation do not match." })
      return
    }
    changePassword.mutate(
      { current_password: current, new_password: next },
      {
        onSuccess: () => {
          setMsg({ ok: true, text: "Password changed." })
          setCurrent("")
          setNext("")
          setConfirm("")
        },
        onError: (e: Error) => setMsg({ ok: false, text: e.message || "Failed to change password" }),
      }
    )
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Profile</h1>

      {/* Account */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">Account</h2>
        </div>
        <div className="p-5 space-y-2">
          <div className="flex justify-between">
            <span className="text-muted-foreground">Email</span>
            <span className="font-medium">{user?.email ?? "—"}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Role</span>
            <span className="font-medium capitalize">{user?.role ?? "—"}</span>
          </div>
        </div>
      </section>

      {/* Quota */}
      {quota && (
        <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
          <div className="px-5 py-4 border-b">
            <h2 className="text-lg font-semibold">Usage &amp; Limits</h2>
          </div>
          <div className="p-5 grid grid-cols-2 sm:grid-cols-4 gap-3 text-center">
            <div className="rounded-md border p-3">
              <p className="text-xs font-medium text-muted-foreground">VMs</p>
              <p className="font-semibold mt-1">{quotaLabel(quota.usage.vms, quota.quota.max_vms, "")}</p>
            </div>
            <div className="rounded-md border p-3">
              <p className="text-xs font-medium text-muted-foreground">vCPU</p>
              <p className="font-semibold mt-1">{quotaLabel(quota.usage.vcpu, quota.quota.max_vcpu, "")}</p>
            </div>
            <div className="rounded-md border p-3">
              <p className="text-xs font-medium text-muted-foreground">RAM</p>
              <p className="font-semibold mt-1">{quotaLabel(quota.usage.ram_mb, quota.quota.max_ram_mb, "MB")}</p>
            </div>
            <div className="rounded-md border p-3">
              <p className="text-xs font-medium text-muted-foreground">Disk</p>
              <p className="font-semibold mt-1">{quotaLabel(quota.usage.disk_gb, quota.quota.max_disk_gb, "GB")}</p>
            </div>
          </div>
        </section>
      )}

      {/* Change password */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">Change Password</h2>
        </div>
        <div className="p-5 space-y-3">
          <input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            placeholder="Current password"
            className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
          <input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            placeholder="New password"
            className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder="Confirm new password"
            className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
          {msg && (
            <p className={`text-sm font-medium ${msg.ok ? "text-emerald-600 dark:text-emerald-400" : "text-destructive"}`}>{msg.text}</p>
          )}
          <button
            onClick={submit}
            disabled={changePassword.isPending || !current || !next}
            className="h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors disabled:opacity-50"
          >
            {changePassword.isPending ? "Saving…" : "Change Password"}
          </button>
        </div>
      </section>
    </div>
  )
}
