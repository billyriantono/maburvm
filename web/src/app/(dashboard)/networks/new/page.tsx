"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, Zap } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api-client"
import { toast } from "sonner"

export default function CreateNetworkPage() {
  const router = useRouter()
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState({
    vm_id: "",
    ip_address: "",
    bandwidth_limit: 0,
    bandwidth_quota_gb: 0,
    vlan_id: "",
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      await api.post("/api/v1/networks", {
        ...form,
        vlan_id: form.vlan_id ? parseInt(form.vlan_id) : null,
      })
      toast.success("Network created successfully")
      router.push("/networks")
    } catch (err: any) {
      toast.error(err?.response?.data?.message || "Failed to create network")
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="max-w-2xl mx-auto">
      <div className="flex items-center gap-4 mb-6">
        <Button variant="ghost" onClick={() => router.back()} className="border-2 border-black h-10 w-10 p-0">
          <ArrowLeft className="w-5 h-5" />
        </Button>
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-3">
            <Zap className="w-8 h-8" />
            Create Network
          </h1>
          <p className="text-gray-500 font-medium text-sm uppercase tracking-wider">Assign a network interface to a VM</p>
        </div>
      </div>

      <form onSubmit={handleSubmit} className="bg-white border-4 border-black shadow-neo p-6 space-y-6">
        <div>
          <label className="text-xs font-black uppercase text-gray-500 block mb-1">VM ID</label>
          <Input
            value={form.vm_id}
            onChange={(e) => setForm({ ...form, vm_id: e.target.value })}
            placeholder="UUID of the VM"
            className="border-2 border-black"
            required
          />
        </div>

        <div>
          <label className="text-xs font-black uppercase text-gray-500 block mb-1">IP Address</label>
          <Input
            value={form.ip_address}
            onChange={(e) => setForm({ ...form, ip_address: e.target.value })}
            placeholder="e.g., 10.0.1.50"
            className="border-2 border-black"
            required
          />
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="text-xs font-black uppercase text-gray-500 block mb-1">Bandwidth Limit (bps)</label>
            <Input
              type="number"
              value={form.bandwidth_limit}
              onChange={(e) => setForm({ ...form, bandwidth_limit: parseInt(e.target.value) || 0 })}
              placeholder="0 = unlimited"
              className="border-2 border-black"
            />
          </div>
          <div>
            <label className="text-xs font-black uppercase text-gray-500 block mb-1">Bandwidth Quota (GB/mo)</label>
            <Input
              type="number"
              value={form.bandwidth_quota_gb}
              onChange={(e) => setForm({ ...form, bandwidth_quota_gb: parseInt(e.target.value) || 0 })}
              placeholder="0 = unlimited"
              className="border-2 border-black"
            />
          </div>
        </div>

        <div>
          <label className="text-xs font-black uppercase text-gray-500 block mb-1">VLAN ID (optional)</label>
          <Input
            type="number"
            value={form.vlan_id}
            onChange={(e) => setForm({ ...form, vlan_id: e.target.value })}
            placeholder="1-4094"
            className="border-2 border-black"
            min={1}
            max={4094}
          />
        </div>

        <div className="flex gap-3 justify-end pt-4 border-t-2 border-black">
          <Button type="button" variant="ghost" onClick={() => router.back()} className="border-2 border-black">
            Cancel
          </Button>
          <Button type="submit" disabled={loading || !form.vm_id || !form.ip_address}>
            {loading ? "Creating..." : "Create Network"}
          </Button>
        </div>
      </form>
    </div>
  )
}
