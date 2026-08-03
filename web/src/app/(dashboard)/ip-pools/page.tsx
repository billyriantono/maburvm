"use client"

import { FormEvent, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import {
  AlertCircle,
  ChevronRight,
  Loader2,
  Network,
  Plus,
  RefreshCw,
  Search,
  Server,
  Trash2,
} from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCreateIPPool, useDeleteIPPool, useIPPools } from "@/lib/hooks/use-ipam"
import { useNodes } from "@/lib/hooks/use-nodes"
import type { CreateIPPoolRequest, IPFamily, IPPool } from "@/types"

const emptyPoolForm: CreateIPPoolRequest = {
  name: "",
  node_ids: [],
  family: "ipv4",
  cidr: "",
  gateway: "",
  bridge: "",
  range_start: "",
  range_end: "",
  description: "",
}

function cleanPoolPayload(payload: CreateIPPoolRequest): CreateIPPoolRequest {
  return Object.fromEntries(Object.entries(payload).filter(([, value]) => value !== "" && value !== undefined)) as CreateIPPoolRequest
}

export default function IPPoolsPage() {
  const router = useRouter()
  const [searchQuery, setSearchQuery] = useState("")
  const [showCreatePool, setShowCreatePool] = useState(false)
  const [poolForm, setPoolForm] = useState<CreateIPPoolRequest>(emptyPoolForm)

  const { data: pools, isLoading, error, refetch } = useIPPools()
  const { data: nodes } = useNodes()
  const createPool = useCreateIPPool()
  const deletePool = useDeleteIPPool()

  const filteredPools = useMemo(() => {
    if (!pools) return []
    if (!searchQuery) return pools
    const query = searchQuery.toLowerCase()
    return pools.filter((pool) =>
      pool.name.toLowerCase().includes(query) ||
      pool.family.toLowerCase().includes(query) ||
      pool.cidr?.toLowerCase().includes(query) ||
      pool.description?.toLowerCase().includes(query),
    )
  }, [pools, searchQuery])

  const handleCreatePool = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    try {
      const pool = await createPool.mutateAsync(cleanPoolPayload(poolForm))
      setPoolForm(emptyPoolForm)
      setShowCreatePool(false)
      toast.success(`IP pool "${pool.name}" created`)
      router.push(`/ip-pools/${pool.id}`)
    } catch (err) {
      toast.error(`Failed to create IP pool: ${(err as Error).message}`)
    }
  }

  const handleDeletePool = async (pool: IPPool) => {
    if (!window.confirm(`Delete IP pool "${pool.name}"? Addresses in this pool may be removed too.`)) return
    try {
      await deletePool.mutateAsync(pool.id)
      toast.success(`IP pool "${pool.name}" deleted`)
    } catch (err) {
      toast.error(`Failed to delete IP pool: ${(err as Error).message}`)
    }
  }

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2">
            <Network className="w-6 h-6" />
            IP Pools
          </h1>
          <p className="text-muted-foreground text-sm mt-1">
            Virtualizor-style IPAM — click a pool to manage its addresses &amp; rDNS
          </p>
        </div>
        <Button className="gap-2" onClick={() => setShowCreatePool((v) => !v)}>
          <Plus className="w-4 h-4" />
          Create Pool
        </Button>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6 shadow-sm mb-6">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-6 h-6 text-destructive" />
            <div className="flex-1">
              <p className="font-semibold">Error loading IP pools</p>
              <p className="text-sm text-muted-foreground">{(error as Error).message}</p>
            </div>
            <Button variant="outline" size="sm" onClick={() => refetch()} className="gap-1"><RefreshCw className="w-4 h-4" />Retry</Button>
          </div>
        </div>
      )}

      {showCreatePool && (
        <form onSubmit={handleCreatePool} className="rounded-lg border bg-card text-card-foreground p-5 shadow-sm mb-6">
          <h2 className="text-lg font-semibold mb-4">Create IP Pool</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
            <Input placeholder="Pool name" value={poolForm.name} onChange={(e) => setPoolForm({ ...poolForm, name: e.target.value })} required />
            <select value={poolForm.family} onChange={(e) => setPoolForm({ ...poolForm, family: e.target.value as IPFamily })} className="h-10 px-3 rounded-md border border-input bg-background text-sm">
              <option value="ipv4">IPv4</option>
              <option value="ipv6">IPv6</option>
            </select>
            <div className="rounded-md border border-input bg-background p-2 max-h-32 overflow-y-auto">
              <label className="flex items-center gap-2 px-2 py-1 cursor-pointer rounded-sm hover:bg-muted">
                <input type="checkbox" checked={!poolForm.node_ids || poolForm.node_ids.length === 0} onChange={() => setPoolForm({ ...poolForm, node_ids: [] })} className="w-4 h-4" />
                <span className="text-sm">Any Node</span>
              </label>
              {nodes?.map((node) => (
                <label key={node.id} className="flex items-center gap-2 px-2 py-1 cursor-pointer rounded-sm hover:bg-muted">
                  <input type="checkbox" checked={poolForm.node_ids?.includes(node.id) ?? false} onChange={(e) => {
                    const current = poolForm.node_ids ?? []
                    const updated = e.target.checked ? [...current, node.id] : current.filter((id) => id !== node.id)
                    setPoolForm({ ...poolForm, node_ids: updated })
                  }} className="w-4 h-4" />
                  <span className="text-sm">{node.name}</span>
                </label>
              ))}
            </div>
            <Input placeholder="CIDR e.g. 203.0.113.0/24" value={poolForm.cidr ?? ""} onChange={(e) => setPoolForm({ ...poolForm, cidr: e.target.value })} />
            <Input placeholder="Gateway" value={poolForm.gateway ?? ""} onChange={(e) => setPoolForm({ ...poolForm, gateway: e.target.value })} />
            <Input placeholder="Bridge e.g. br0 (blank = node default)" value={poolForm.bridge ?? ""} onChange={(e) => setPoolForm({ ...poolForm, bridge: e.target.value })} />
            <Input placeholder="Range start" value={poolForm.range_start ?? ""} onChange={(e) => setPoolForm({ ...poolForm, range_start: e.target.value })} />
            <Input placeholder="Range end" value={poolForm.range_end ?? ""} onChange={(e) => setPoolForm({ ...poolForm, range_end: e.target.value })} />
            <Input placeholder="Description" value={poolForm.description ?? ""} onChange={(e) => setPoolForm({ ...poolForm, description: e.target.value })} />
          </div>
          <div className="flex justify-end gap-3 mt-4">
            <Button type="button" variant="ghost" onClick={() => setShowCreatePool(false)}>Cancel</Button>
            <Button type="submit" disabled={createPool.isPending}>{createPool.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Create Pool</Button>
          </div>
        </form>
      )}

      <div className="rounded-lg border bg-card p-4 shadow-sm mb-6">
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
          <Input placeholder="Search pools..." value={searchQuery} onChange={(e) => setSearchQuery(e.target.value)} className="pl-10" />
        </div>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20"><Loader2 className="w-8 h-8 animate-spin" /><span className="ml-3 text-muted-foreground">Loading IP pools...</span></div>
      ) : filteredPools.length === 0 ? (
        <div className="rounded-lg border bg-card p-12 shadow-sm text-center">
          <Network className="w-16 h-16 text-muted-foreground/40 mx-auto mb-4" />
          <p className="text-muted-foreground font-medium">No IP pools found</p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card shadow-sm overflow-hidden">
          <div className="grid grid-cols-12 gap-3 p-4 bg-muted text-muted-foreground font-medium text-xs">
            <div className="col-span-4">Name</div>
            <div className="col-span-3">CIDR</div>
            <div className="col-span-2">Family</div>
            <div className="col-span-2">Node</div>
            <div className="col-span-1 text-right">Action</div>
          </div>
          {filteredPools.map((pool) => {
            const poolNodes = pool.node_ids?.map((nid) => nodes?.find((item) => item.id === nid)?.name).filter(Boolean)
            const nodeLabel = poolNodes && poolNodes.length > 0 ? poolNodes.join(", ") : "Any"
            return (
              <div
                key={pool.id}
                role="button"
                tabIndex={0}
                onClick={() => router.push(`/ip-pools/${pool.id}`)}
                onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") router.push(`/ip-pools/${pool.id}`) }}
                className={`grid grid-cols-12 gap-3 p-4 items-center border-b last:border-0 cursor-pointer hover:bg-muted/50`}
              >
                <div className="col-span-4 font-medium text-foreground truncate" title={pool.name}>{pool.name}</div>
                <div className="col-span-3 font-mono text-sm truncate">{pool.cidr || "No CIDR"}</div>
                <div className="col-span-2"><Badge variant={pool.family === "ipv4" ? "secondary" : "warning"}>{pool.family}</Badge></div>
                <div className="col-span-2 text-xs text-muted-foreground truncate flex items-center gap-1" title={nodeLabel}>
                  <Server className="w-3 h-3 shrink-0" />
                  {nodeLabel}
                </div>
                <div className="col-span-1 flex justify-end items-center gap-1">
                  <Button type="button" variant="ghost" size="sm" onClick={(e) => { e.stopPropagation(); handleDeletePool(pool) }} className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive" title="Delete pool">
                    <Trash2 className="w-4 h-4" />
                  </Button>
                  <ChevronRight className="w-4 h-4 text-muted-foreground" />
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
