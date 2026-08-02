"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, Zap, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCreateNetwork } from "@/lib/hooks/use-networks"
import { useNodes } from "@/lib/hooks/use-nodes"
import { toast } from "sonner"

export default function CreateNetworkPage() {
  const router = useRouter()
  const createNetwork = useCreateNetwork()
  const { data: nodes } = useNodes()
  const [form, setForm] = useState({
    name: "",
    type: "bridge",
    bridge: "",
    subnet: "",
    gateway: "",
    dhcp_start: "",
    dhcp_end: "",
    vlan_id: "",
    node_id: "",
  })

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!form.name.trim()) {
      toast.error("Network name is required")
      return
    }
    try {
      await createNetwork.mutateAsync({
        name: form.name.trim(),
        type: form.type,
        bridge: form.bridge || undefined,
        subnet: form.subnet || undefined,
        gateway: form.gateway || undefined,
        dhcp_start: form.dhcp_start || undefined,
        dhcp_end: form.dhcp_end || undefined,
        vlan_id: form.vlan_id ? parseInt(form.vlan_id) : undefined,
        node_id: form.node_id || undefined,
      })
      toast.success(`Network "${form.name}" created`)
      router.push("/networks")
    } catch (err) {
      toast.error(`Failed to create network: ${(err as Error).message}`)
    }
  }

  return (
    <div className="max-w-2xl mx-auto">
      <div className="flex items-center gap-4 mb-6">
        <Button variant="outline" onClick={() => router.back()} className="h-10 w-10 p-0">
          <ArrowLeft className="w-5 h-5" />
        </Button>
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-3">
            <Zap className="w-6 h-6" />
            Create Network
          </h1>
          <p className="text-muted-foreground text-sm">Define a bridge / NAT / isolated virtual network</p>
        </div>
      </div>

      <form onSubmit={handleSubmit} className="rounded-lg border bg-card text-card-foreground shadow-sm p-6 space-y-6">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">Name</label>
            <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="e.g. public-bridge" required />
          </div>
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">Type</label>
            <select value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value })} className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
              <option value="bridge">Bridge (use an existing host bridge)</option>
              <option value="nat">NAT (private subnet, outbound via host)</option>
              <option value="isolated">Isolated (private VPC, no uplink)</option>
            </select>
          </div>
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">Node</label>
            <select value={form.node_id} onChange={(e) => setForm({ ...form, node_id: e.target.value })} className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm">
              <option value="">— select node —</option>
              {nodes?.map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </div>
        </div>
        {(form.type === "isolated" || form.type === "nat") && !form.node_id && (
          <p className="text-xs text-amber-600 -mt-2">Isolated/NAT networks are provisioned on a node — select a node so the libvirt network is created.</p>
        )}

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">Bridge</label>
            <Input value={form.bridge} onChange={(e) => setForm({ ...form, bridge: e.target.value })} placeholder="e.g. br0 / virbr0" />
          </div>
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">VLAN ID (optional)</label>
            <Input type="number" value={form.vlan_id} onChange={(e) => setForm({ ...form, vlan_id: e.target.value })} placeholder="1-4094" min={1} max={4094} />
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">Subnet (CIDR)</label>
            <Input value={form.subnet} onChange={(e) => setForm({ ...form, subnet: e.target.value })} placeholder="e.g. 10.0.1.0/24" />
          </div>
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">Gateway</label>
            <Input value={form.gateway} onChange={(e) => setForm({ ...form, gateway: e.target.value })} placeholder="e.g. 10.0.1.1" />
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">DHCP Start (optional)</label>
            <Input value={form.dhcp_start} onChange={(e) => setForm({ ...form, dhcp_start: e.target.value })} placeholder="e.g. 10.0.1.100" />
          </div>
          <div>
            <label className="text-xs font-medium text-muted-foreground block mb-1">DHCP End (optional)</label>
            <Input value={form.dhcp_end} onChange={(e) => setForm({ ...form, dhcp_end: e.target.value })} placeholder="e.g. 10.0.1.200" />
          </div>
        </div>

        <div className="flex gap-3 justify-end pt-4 border-t">
          <Button type="button" variant="outline" onClick={() => router.back()}>
            Cancel
          </Button>
          <Button type="submit" disabled={createNetwork.isPending || !form.name.trim()}>
            {createNetwork.isPending ? <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Creating...</> : "Create Network"}
          </Button>
        </div>
      </form>
    </div>
  )
}
