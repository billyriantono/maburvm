"use client"

import { FormEvent, useMemo, useState } from "react"
import Link from "next/link"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft,
  CheckCircle2,
  CircleSlash,
  DownloadCloud,
  Globe,
  Loader2,
  Network,
  Plus,
  RefreshCw,
  Search,
  Unlock,
  Zap,
} from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  useAddIPAddress,
  useAllocateIPAddress,
  useIPAddresses,
  useIPPool,
  useImportRDNS,
  useReleaseIPAddress,
  useSetRDNS,
  downloadReverseZone,
} from "@/lib/hooks/use-ipam"
import { useNodes } from "@/lib/hooks/use-nodes"
import { useVMs } from "@/lib/hooks/use-vms"
import type { IPAddress, IPAddressStatus } from "@/types"

const PAGE_SIZE = 25

function statusVariant(status: IPAddressStatus): "success" | "warning" | "destructive" | "secondary" | "outline" {
  switch (status) {
    case "available": return "success"
    case "reserved": return "warning"
    case "assigned": return "secondary"
    case "disabled": return "destructive"
    default: return "outline"
  }
}

// RDNSCell is an inline editor that commits the address's PTR hostname on blur.
function RDNSCell({ address, poolId }: { address: IPAddress; poolId: string }) {
  const setRDNS = useSetRDNS(poolId)
  const [value, setValue] = useState(address.rdns ?? "")

  const commit = () => {
    if (value === (address.rdns ?? "")) return
    setRDNS.mutate(
      { addressId: address.id, rdns: value.trim() },
      {
        onSuccess: () => toast.success(`rDNS updated for ${address.address}`),
        onError: (err) => {
          toast.error(`rDNS: ${(err as Error).message}`)
          setValue(address.rdns ?? "")
        },
      },
    )
  }

  return (
    <div className="flex items-center gap-1">
      <input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => { if (e.key === "Enter") (e.target as HTMLInputElement).blur() }}
        placeholder="set PTR hostname…"
        title="Reverse DNS (PTR) hostname — type a hostname and press Enter"
        className="w-full bg-white border-2 border-black text-xs font-mono px-2 py-1.5 focus:outline-none focus:shadow-neo-sm placeholder:text-gray-400 placeholder:normal-case"
      />
      {setRDNS.isPending && <Loader2 className="w-3 h-3 animate-spin shrink-0" />}
    </div>
  )
}

export default function IPPoolDetailPage() {
  const params = useParams()
  const router = useRouter()
  const poolId = (params?.id as string) || ""

  const { data: pool, isLoading: poolLoading, error: poolError } = useIPPool(poolId)
  const { data: addresses, isLoading, refetch } = useIPAddresses(poolId)
  const { data: nodes } = useNodes()
  const { data: vmsResp } = useVMs({ pageSize: 1000 })

  // Resolve VM id -> hostname for the VM column (show a name, not a UUID).
  const vmNameById = useMemo(() => {
    const m = new Map<string, string>()
    for (const vm of vmsResp?.data ?? []) m.set(vm.id, vm.hostname)
    return m
  }, [vmsResp])

  const addAddress = useAddIPAddress(poolId)
  const allocateAddress = useAllocateIPAddress(poolId)
  const releaseAddress = useReleaseIPAddress(poolId)
  const importRDNS = useImportRDNS()

  const [search, setSearch] = useState("")
  const [page, setPage] = useState(1)
  const [showAdd, setShowAdd] = useState(false)
  const [addrForm, setAddrForm] = useState({ address: "", status: "available" as IPAddressStatus, note: "" })
  const [isGenerating, setIsGenerating] = useState(false)

  // Node label: a specific address node, else the pool's node(s) — never a bare "Any"
  // when the pool is bound to a node.
  const poolNodeLabel = useMemo(() => {
    if (!pool?.node_ids || pool.node_ids.length === 0) return "Any"
    return pool.node_ids.map((id) => nodes?.find((n) => n.id === id)?.name || id).join(", ")
  }, [pool, nodes])

  const nodeLabelFor = (a: IPAddress) => {
    if (a.node_id) return nodes?.find((n) => n.id === a.node_id)?.name || a.node_id
    return poolNodeLabel
  }

  // The node to link to: the address's own node, else the pool's (first) node.
  const nodeIdFor = (a: IPAddress) => a.node_id || pool?.node_ids?.[0] || ""

  const filtered = useMemo(() => {
    const list = addresses ?? []
    if (!search) return list
    const q = search.toLowerCase()
    return list.filter((a) =>
      a.address.toLowerCase().includes(q) ||
      a.rdns?.toLowerCase().includes(q) ||
      a.status.toLowerCase().includes(q) ||
      a.vm_id?.toLowerCase().includes(q),
    )
  }, [addresses, search])

  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const currentPage = Math.min(page, totalPages)
  const pageItems = filtered.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE)

  const handleGenerate = async () => {
    setIsGenerating(true)
    try {
      const response = await api.post<{ count: number }>(`/api/v1/ip-pools/${poolId}/generate`)
      const count = (response.data as unknown as { data: { count: number } }).data?.count ?? 0
      toast.success(`Generated ${count} IP addresses`)
      refetch()
    } catch (err) {
      toast.error(`Failed to generate addresses: ${(err as Error).message}`)
    } finally {
      setIsGenerating(false)
    }
  }

  const handleAllocate = async () => {
    if (!pool) return
    try {
      const a = await allocateAddress.mutateAsync({ pool_id: pool.id, node_id: pool.node_ids?.[0] })
      toast.success(`Allocated ${a.address}`)
    } catch (err) {
      toast.error(`Failed to allocate: ${(err as Error).message}`)
    }
  }

  const handleAddAddress = async (e: FormEvent) => {
    e.preventDefault()
    if (!pool) return
    try {
      await addAddress.mutateAsync({
        address: addrForm.address,
        status: addrForm.status,
        note: addrForm.note || undefined,
        node_id: pool.node_ids?.[0],
        family: pool.family,
      })
      toast.success("IP address added")
      setAddrForm({ address: "", status: "available", note: "" })
      setShowAdd(false)
    } catch (err) {
      toast.error(`Failed to add address: ${(err as Error).message}`)
    }
  }

  const handleRelease = async (a: IPAddress) => {
    try {
      await releaseAddress.mutateAsync(a.id)
      toast.success(`Released ${a.address}`)
    } catch (err) {
      toast.error(`Failed to release: ${(err as Error).message}`)
    }
  }

  const handleImportRDNS = async () => {
    try {
      const n = await importRDNS.mutateAsync(poolId)
      toast.success(n > 0 ? `Imported ${n} existing PTR record${n === 1 ? "" : "s"} from the nameserver` : "No existing PTRs found on the nameserver")
    } catch (err) {
      toast.error(`Import failed: ${(err as Error).message}`)
    }
  }

  if (poolLoading) {
    return <div className="max-w-7xl mx-auto flex items-center justify-center py-20"><Loader2 className="w-8 h-8 animate-spin" /></div>
  }
  if (poolError || !pool) {
    return (
      <div className="max-w-7xl mx-auto">
        <Button variant="ghost" onClick={() => router.push("/ip-pools")} className="border-2 border-black gap-1 mb-4"><ArrowLeft className="w-4 h-4" />Back</Button>
        <div className="bg-danger/10 border-4 border-danger p-6"><p className="font-black uppercase">Pool not found</p></div>
      </div>
    )
  }

  return (
    <div className="max-w-7xl mx-auto">
      <Button variant="ghost" onClick={() => router.push("/ip-pools")} className="border-2 border-black gap-1 mb-4">
        <ArrowLeft className="w-4 h-4" />Back to Pools
      </Button>

      {/* Identity */}
      <div className="bg-white border-4 border-black shadow-neo p-4 mb-4">
        <h1 className="text-2xl font-black uppercase tracking-tight flex items-center gap-2">
          <Network className="w-7 h-7" />{pool.name}
          <Badge variant={pool.family === "ipv4" ? "secondary" : "warning"}>{pool.family}</Badge>
        </h1>
        <p className="text-sm font-bold text-gray-500 uppercase mt-1">
          {pool.cidr || "No CIDR"} • Gateway {pool.gateway || "-"} • Node {poolNodeLabel} • {addresses?.length ?? 0} addresses
        </p>
      </div>

      {/* Actions — grouped + in their own section for clarity */}
      <div className="bg-white border-4 border-black shadow-neo mb-4">
        <div className="p-3 bg-black text-white font-black uppercase text-xs tracking-wider">Actions</div>
        <div className="p-4 flex flex-col md:flex-row md:items-start gap-4">
          <div>
            <p className="text-[10px] font-bold uppercase tracking-wider text-gray-500 mb-2">Addresses</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" size="sm" className="gap-1" onClick={() => refetch()}><RefreshCw className="w-4 h-4" />Refresh</Button>
              <Button variant="warning" size="sm" className="gap-1" onClick={handleGenerate} disabled={isGenerating}>
                {isGenerating ? <Loader2 className="w-4 h-4 animate-spin" /> : <Zap className="w-4 h-4" />}Generate IPs
              </Button>
              <Button variant="success" size="sm" className="gap-1" onClick={handleAllocate} disabled={allocateAddress.isPending}>
                {allocateAddress.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <CheckCircle2 className="w-4 h-4" />}Allocate Next
              </Button>
              <Button size="sm" className="gap-1" onClick={() => setShowAdd((v) => !v)}><Plus className="w-4 h-4" />Add Address</Button>
            </div>
          </div>
          <div className="md:border-l-2 md:border-black md:pl-4">
            <p className="text-[10px] font-bold uppercase tracking-wider text-gray-500 mb-2">Reverse DNS</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" size="sm" className="gap-1" title="Import existing PTRs from the nameserver into MaburVM (read-only)" onClick={handleImportRDNS} disabled={importRDNS.isPending}>
                {importRDNS.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <DownloadCloud className="w-4 h-4" />}Import PTRs
              </Button>
              <Button variant="secondary" size="sm" className="gap-1" title="Download a BIND PTR zone of this pool's rDNS records" onClick={() => downloadReverseZone(pool.id, pool.name).catch((err) => toast.error(`Export failed: ${(err as Error).message}`))}>
                <Globe className="w-4 h-4" />rDNS Zone
              </Button>
            </div>
          </div>
        </div>

        {showAdd && (
          <form onSubmit={handleAddAddress} className="grid grid-cols-1 md:grid-cols-4 gap-3 p-4 border-t-4 border-black">
            <Input placeholder="IP address" value={addrForm.address} onChange={(e) => setAddrForm({ ...addrForm, address: e.target.value })} className="border-2 border-black" required />
            <select value={addrForm.status} onChange={(e) => setAddrForm({ ...addrForm, status: e.target.value as IPAddressStatus })} className="h-12 px-3 border-2 border-black font-medium bg-white">
              <option value="available">Available</option>
              <option value="reserved">Reserved</option>
              <option value="assigned">Assigned</option>
              <option value="disabled">Disabled</option>
            </select>
            <Input placeholder="Note" value={addrForm.note} onChange={(e) => setAddrForm({ ...addrForm, note: e.target.value })} className="border-2 border-black" />
            <Button type="submit" disabled={addAddress.isPending}>{addAddress.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}Add</Button>
          </form>
        )}
      </div>

      {/* Search */}
      <div className="bg-white border-4 border-black p-3 shadow-neo mb-4">
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
          <Input placeholder="Search address, rDNS, status, or VM…" value={search} onChange={(e) => { setSearch(e.target.value); setPage(1) }} className="pl-10 border-2 border-black" />
        </div>
      </div>

      {/* Address table */}
      <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
        <div className="grid grid-cols-12 gap-3 p-4 bg-black text-white font-black uppercase text-xs tracking-wider">
          <div className="col-span-3">Address</div>
          <div className="col-span-2">Status</div>
          <div className="col-span-4">rDNS (PTR)</div>
          <div className="col-span-1">Node</div>
          <div className="col-span-1">VM</div>
          <div className="col-span-1 text-right">Action</div>
        </div>

        {isLoading ? (
          <div className="flex items-center justify-center py-16"><Loader2 className="w-8 h-8 animate-spin" /><span className="ml-3 font-bold uppercase">Loading addresses...</span></div>
        ) : pageItems.length === 0 ? (
          <div className="p-12 text-center"><CircleSlash className="w-16 h-16 text-gray-300 mx-auto mb-4" /><p className="text-gray-500 font-bold uppercase">No addresses</p></div>
        ) : (
          pageItems.map((address, index) => (
            <div key={address.id} className={`grid grid-cols-12 gap-3 p-4 items-center border-b-2 border-black last:border-0 ${index % 2 === 0 ? "bg-white" : "bg-gray-50"}`}>
              <div className="col-span-3 font-mono text-sm font-black whitespace-nowrap" title={address.address}>{address.address}</div>
              <div className="col-span-2"><Badge variant={statusVariant(address.status)}>{address.status}</Badge></div>
              <div className="col-span-4"><RDNSCell address={address} poolId={poolId} /></div>
              <div className="col-span-1 text-sm font-bold truncate" title={nodeLabelFor(address)}>
                {nodeIdFor(address) ? (
                  <Link href={`/nodes/${nodeIdFor(address)}`} className="text-black hover:text-primary underline decoration-2 underline-offset-2">{nodeLabelFor(address)}</Link>
                ) : (
                  nodeLabelFor(address)
                )}
              </div>
              <div className="col-span-1 text-xs truncate" title={address.vm_id ? (vmNameById.get(address.vm_id) || address.vm_id) : "-"}>
                {address.vm_id ? (
                  <Link href={`/vms/${address.vm_id}`} className="font-bold text-black hover:text-primary underline decoration-2 underline-offset-2">
                    {vmNameById.get(address.vm_id) || `${address.vm_id.slice(0, 8)}…`}
                  </Link>
                ) : (
                  <span className="font-mono text-gray-500">-</span>
                )}
              </div>
              <div className="col-span-1 flex justify-end">
                {address.status === "assigned" ? (
                  <Button variant="warning" size="sm" onClick={() => handleRelease(address)} disabled={releaseAddress.isPending} className="h-8 w-8 p-0" title="Release address"><Unlock className="w-4 h-4" /></Button>
                ) : (
                  <span className="text-gray-500">-</span>
                )}
              </div>
            </div>
          ))
        )}

        {/* Pagination */}
        {filtered.length > PAGE_SIZE && (
          <div className="flex items-center justify-between p-4 border-t-4 border-black bg-gray-50">
            <span className="text-xs font-bold uppercase text-gray-600">
              {(currentPage - 1) * PAGE_SIZE + 1}–{Math.min(currentPage * PAGE_SIZE, filtered.length)} of {filtered.length}
            </span>
            <div className="flex items-center gap-2">
              <Button variant="secondary" size="sm" disabled={currentPage <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>Prev</Button>
              <span className="text-sm font-black">Page {currentPage} / {totalPages}</span>
              <Button variant="secondary" size="sm" disabled={currentPage >= totalPages} onClick={() => setPage((p) => Math.min(totalPages, p + 1))}>Next</Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
