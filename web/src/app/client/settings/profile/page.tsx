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
    <div className="space-y-6 max-w-2xl">
      <h1 className="text-3xl font-black uppercase tracking-tighter">Profile</h1>

      {/* Account */}
      <section className="bg-white border-4 border-black shadow-neo">
        <div className="px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight">Account</h2>
        </div>
        <div className="p-5 space-y-2">
          <div className="flex justify-between">
            <span className="font-bold text-gray-600">Email</span>
            <span className="font-black">{user?.email ?? "—"}</span>
          </div>
          <div className="flex justify-between">
            <span className="font-bold text-gray-600">Role</span>
            <span className="font-black uppercase">{user?.role ?? "—"}</span>
          </div>
        </div>
      </section>

      {/* Quota */}
      {quota && (
        <section className="bg-white border-4 border-black shadow-neo">
          <div className="px-5 py-4 border-b-4 border-black">
            <h2 className="text-lg font-black uppercase tracking-tight">Usage &amp; Limits</h2>
          </div>
          <div className="p-5 grid grid-cols-2 sm:grid-cols-4 gap-3 text-center">
            <div className="border-2 border-black p-3">
              <p className="text-[11px] font-black uppercase text-gray-500">VMs</p>
              <p className="font-black">{quotaLabel(quota.usage.vms, quota.quota.max_vms, "")}</p>
            </div>
            <div className="border-2 border-black p-3">
              <p className="text-[11px] font-black uppercase text-gray-500">vCPU</p>
              <p className="font-black">{quotaLabel(quota.usage.vcpu, quota.quota.max_vcpu, "")}</p>
            </div>
            <div className="border-2 border-black p-3">
              <p className="text-[11px] font-black uppercase text-gray-500">RAM</p>
              <p className="font-black">{quotaLabel(quota.usage.ram_mb, quota.quota.max_ram_mb, "MB")}</p>
            </div>
            <div className="border-2 border-black p-3">
              <p className="text-[11px] font-black uppercase text-gray-500">Disk</p>
              <p className="font-black">{quotaLabel(quota.usage.disk_gb, quota.quota.max_disk_gb, "GB")}</p>
            </div>
          </div>
        </section>
      )}

      {/* Change password */}
      <section className="bg-white border-4 border-black shadow-neo">
        <div className="px-5 py-4 border-b-4 border-black">
          <h2 className="text-lg font-black uppercase tracking-tight">Change Password</h2>
        </div>
        <div className="p-5 space-y-3">
          <input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            placeholder="Current password"
            className="w-full h-11 px-3 border-2 border-black bg-white font-bold focus:outline-none focus:shadow-neo-sm"
          />
          <input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            placeholder="New password"
            className="w-full h-11 px-3 border-2 border-black bg-white font-bold focus:outline-none focus:shadow-neo-sm"
          />
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder="Confirm new password"
            className="w-full h-11 px-3 border-2 border-black bg-white font-bold focus:outline-none focus:shadow-neo-sm"
          />
          {msg && (
            <p className={`text-sm font-bold ${msg.ok ? "text-green-700" : "text-destructive"}`}>{msg.text}</p>
          )}
          <button
            onClick={submit}
            disabled={changePassword.isPending || !current || !next}
            className="h-11 px-5 bg-primary text-black border-2 border-black font-black uppercase text-sm shadow-neo hover:shadow-neo-sm transition-all disabled:opacity-50"
          >
            {changePassword.isPending ? "Saving…" : "Change Password"}
          </button>
        </div>
      </section>
    </div>
  )
}
