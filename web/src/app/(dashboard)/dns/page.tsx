"use client"

import { FormEvent, useMemo, useState } from "react"
import { Globe, Plus, Trash2, Loader2, Download, Server, CircleSlash, RefreshCw, CheckCircle2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import {
  useDNSZones,
  useCreateDNSZone,
  useDeleteDNSZone,
  useDNSRecords,
  useCreateDNSRecord,
  useDeleteDNSRecord,
  useDNSProvider,
  useSyncDNSZone,
  downloadZoneFile,
} from "@/lib/hooks/use-dns"
import type { CreateRecordRequest, CreateZoneRequest, DNSRecordType, DNSZone } from "@/types/dns"

const RECORD_TYPES: DNSRecordType[] = ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV"]

const emptyZone: CreateZoneRequest = { name: "", primary_ns: "", admin_email: "", ttl: 3600, description: "" }
const emptyRecord: CreateRecordRequest = { name: "@", type: "A", content: "", ttl: 3600, priority: 0 }

export default function DNSPage() {
  const { data: zones, isLoading, error } = useDNSZones()
  const { data: provider } = useDNSProvider()
  const createZone = useCreateDNSZone()
  const deleteZone = useDeleteDNSZone()
  const syncZone = useSyncDNSZone()

  const [showCreateZone, setShowCreateZone] = useState(false)
  const [zoneForm, setZoneForm] = useState<CreateZoneRequest>(emptyZone)
  const [selectedZoneId, setSelectedZoneId] = useState<string>()

  const selectedZone = useMemo(() => zones?.find((z) => z.id === selectedZoneId), [zones, selectedZoneId])

  const { data: records, isLoading: recordsLoading } = useDNSRecords(selectedZoneId)
  const createRecord = useCreateDNSRecord(selectedZoneId)
  const deleteRecord = useDeleteDNSRecord(selectedZoneId)
  const [recordForm, setRecordForm] = useState<CreateRecordRequest>(emptyRecord)

  const handleCreateZone = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const zone = await createZone.mutateAsync(zoneForm)
      toast.success(`Zone "${zone.name}" created`)
      setZoneForm(emptyZone)
      setShowCreateZone(false)
      setSelectedZoneId(zone.id)
    } catch (err) {
      toast.error(`Failed to create zone: ${(err as Error).message}`)
    }
  }

  const handleDeleteZone = async (zone: DNSZone) => {
    if (!window.confirm(`Delete zone "${zone.name}" and all its records?`)) return
    try {
      await deleteZone.mutateAsync(zone.id)
      if (selectedZoneId === zone.id) setSelectedZoneId(undefined)
      toast.success(`Zone "${zone.name}" deleted`)
    } catch (err) {
      toast.error(`Failed to delete zone: ${(err as Error).message}`)
    }
  }

  const handleCreateRecord = async (e: FormEvent) => {
    e.preventDefault()
    if (!selectedZoneId) return
    try {
      await createRecord.mutateAsync(recordForm)
      toast.success("Record added")
      setRecordForm({ ...emptyRecord, type: recordForm.type })
    } catch (err) {
      toast.error(`Failed to add record: ${(err as Error).message}`)
    }
  }

  const handleDeleteRecord = async (id: string) => {
    try {
      await deleteRecord.mutateAsync(id)
      toast.success("Record deleted")
    } catch (err) {
      toast.error(`Failed to delete record: ${(err as Error).message}`)
    }
  }

  const handleSync = async (zoneId: string) => {
    try {
      await syncZone.mutateAsync(zoneId)
      toast.success(`Zone pushed to ${provider?.name ?? "nameserver"}`)
    } catch (err) {
      toast.error(`Sync failed: ${(err as Error).message}`)
    }
  }

  const needsPriority = recordForm.type === "MX" || recordForm.type === "SRV"

  return (
    <div className="max-w-7xl mx-auto">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 mb-6">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black flex items-center gap-2">
            <Globe className="w-8 h-8" />
            DNS
          </h1>
          <div className="flex items-center gap-2 mt-1">
            <p className="text-gray-500 font-medium uppercase tracking-wider text-sm">
              Authoritative forward zones &amp; records
            </p>
            {provider && (
              provider.configured ? (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase border-2 border-black bg-success">
                  <CheckCircle2 className="w-3 h-3" />
                  Live: {provider.name}
                </span>
              ) : (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-black uppercase border-2 border-black bg-gray-200 text-gray-600" title="Set PDNS_API_URL and PDNS_API_KEY in the panel environment to enable live push">
                  Export only
                </span>
              )
            )}
          </div>
        </div>
        <Button className="gap-2" onClick={() => setShowCreateZone((v) => !v)}>
          <Plus className="w-4 h-4" />
          Create Zone
        </Button>
      </div>

      {showCreateZone && (
        <form onSubmit={handleCreateZone} className="bg-white border-4 border-black p-5 shadow-neo mb-6">
          <h2 className="text-xl font-black uppercase mb-4">Create Zone</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
            <Input placeholder="example.com" value={zoneForm.name} onChange={(e) => setZoneForm({ ...zoneForm, name: e.target.value })} className="border-2 border-black" required />
            <Input placeholder="Primary NS (ns1.example.com)" value={zoneForm.primary_ns} onChange={(e) => setZoneForm({ ...zoneForm, primary_ns: e.target.value })} className="border-2 border-black" />
            <Input placeholder="Admin email (hostmaster@example.com)" value={zoneForm.admin_email} onChange={(e) => setZoneForm({ ...zoneForm, admin_email: e.target.value })} className="border-2 border-black" />
            <Input type="number" min={60} placeholder="TTL (3600)" value={zoneForm.ttl} onChange={(e) => setZoneForm({ ...zoneForm, ttl: Number(e.target.value) })} className="border-2 border-black" />
          </div>
          <div className="flex justify-end gap-3 mt-4">
            <Button type="button" variant="ghost" onClick={() => setShowCreateZone(false)}>Cancel</Button>
            <Button type="submit" disabled={createZone.isPending}>
              {createZone.isPending && <Loader2 className="w-4 h-4 animate-spin mr-2" />}
              Create Zone
            </Button>
          </div>
        </form>
      )}

      {error && (
        <div className="bg-danger/10 border-4 border-danger p-6 shadow-neo mb-6">
          <p className="font-black uppercase">Failed to load zones</p>
          <p className="text-sm font-medium">{(error as Error).message}</p>
        </div>
      )}

      <div className="grid grid-cols-1 xl:grid-cols-5 gap-6">
        {/* Zones list */}
        <div className="xl:col-span-2">
          <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
            <div className="p-4 bg-black text-white font-black uppercase text-xs tracking-wider">Zones</div>
            {isLoading ? (
              <div className="p-12 text-center"><Loader2 className="w-8 h-8 animate-spin mx-auto" /></div>
            ) : !zones || zones.length === 0 ? (
              <div className="p-12 text-center">
                <Globe className="w-16 h-16 text-gray-300 mx-auto mb-4" />
                <p className="text-gray-500 font-bold uppercase">No zones yet</p>
              </div>
            ) : (
              zones.map((zone) => (
                <div
                  key={zone.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => setSelectedZoneId(zone.id)}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") setSelectedZoneId(zone.id) }}
                  className={`w-full text-left p-4 border-b-2 border-black last:border-0 cursor-pointer ${zone.id === selectedZoneId ? "bg-primary" : "bg-white hover:bg-gray-50"}`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <span className="font-black uppercase text-black truncate block">{zone.name}</span>
                      <span className="text-xs font-bold uppercase text-gray-500">TTL {zone.ttl}s</span>
                    </div>
                    <Button type="button" variant="ghost" size="sm" onClick={(e) => { e.stopPropagation(); handleDeleteZone(zone) }} className="h-8 w-8 p-0 border-2 border-black hover:bg-danger hover:text-white">
                      <Trash2 className="w-4 h-4" />
                    </Button>
                  </div>
                </div>
              ))
            )}
          </div>
        </div>

        {/* Records panel */}
        <div className="xl:col-span-3">
          {!selectedZone ? (
            <div className="bg-white border-4 border-black p-12 shadow-neo text-center">
              <Server className="w-16 h-16 text-gray-300 mx-auto mb-4" />
              <h2 className="text-xl font-black uppercase mb-2">Select a Zone</h2>
              <p className="text-gray-500 font-medium">Choose a zone to manage its records and export a zone file.</p>
            </div>
          ) : (
            <div className="bg-white border-4 border-black shadow-neo overflow-hidden">
              <div className="p-4 border-b-4 border-black bg-gray-50 flex flex-col lg:flex-row lg:items-center lg:justify-between gap-3">
                <div>
                  <h2 className="text-xl font-black uppercase">{selectedZone.name}</h2>
                  <p className="text-xs font-bold text-gray-500 uppercase">{records?.length ?? 0} records</p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    variant="success"
                    size="sm"
                    className="gap-1"
                    disabled={syncZone.isPending}
                    title={provider?.configured ? `Push this zone to ${provider.name}` : "Configure PDNS_API_URL/PDNS_API_KEY to enable live push"}
                    onClick={() => handleSync(selectedZone.id)}
                  >
                    {syncZone.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
                    Sync to Nameserver
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="gap-1"
                    onClick={() => downloadZoneFile(selectedZone.id, selectedZone.name).catch((err) => toast.error(`Export failed: ${(err as Error).message}`))}
                  >
                    <Download className="w-4 h-4" />
                    Export Zone File
                  </Button>
                </div>
              </div>

              {/* Add record form */}
              <form onSubmit={handleCreateRecord} className="p-4 border-b-4 border-black grid grid-cols-1 md:grid-cols-12 gap-3">
                <Input placeholder="@ or name" value={recordForm.name} onChange={(e) => setRecordForm({ ...recordForm, name: e.target.value })} className="border-2 border-black md:col-span-2" />
                <select value={recordForm.type} onChange={(e) => setRecordForm({ ...recordForm, type: e.target.value as DNSRecordType })} className="h-12 px-2 border-2 border-black font-medium bg-white md:col-span-2">
                  {RECORD_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
                <Input placeholder="content / value" value={recordForm.content} onChange={(e) => setRecordForm({ ...recordForm, content: e.target.value })} className={`border-2 border-black ${needsPriority ? "md:col-span-3" : "md:col-span-5"}`} required />
                {needsPriority && (
                  <Input type="number" min={0} placeholder="prio" value={recordForm.priority} onChange={(e) => setRecordForm({ ...recordForm, priority: Number(e.target.value) })} className="border-2 border-black md:col-span-2" title="Priority" />
                )}
                <Input type="number" min={60} placeholder="TTL" value={recordForm.ttl} onChange={(e) => setRecordForm({ ...recordForm, ttl: Number(e.target.value) })} className="border-2 border-black md:col-span-1" title="TTL" />
                <Button type="submit" disabled={createRecord.isPending} className="md:col-span-2">
                  {createRecord.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Plus className="w-4 h-4" />}
                  Add
                </Button>
              </form>

              {/* Records table */}
              <div className="grid grid-cols-12 gap-2 p-3 bg-black text-white font-black uppercase text-[10px] tracking-wider">
                <div className="col-span-3">Name</div>
                <div className="col-span-2">Type</div>
                <div className="col-span-4">Content</div>
                <div className="col-span-1">TTL</div>
                <div className="col-span-1">Prio</div>
                <div className="col-span-1 text-right">·</div>
              </div>
              {recordsLoading ? (
                <div className="p-8 text-center"><Loader2 className="w-6 h-6 animate-spin mx-auto" /></div>
              ) : !records || records.length === 0 ? (
                <div className="p-10 text-center">
                  <CircleSlash className="w-12 h-12 text-gray-300 mx-auto mb-2" />
                  <p className="text-gray-500 font-bold uppercase text-sm">No records yet</p>
                </div>
              ) : (
                records.map((r, i) => (
                  <div key={r.id} className={`grid grid-cols-12 gap-2 p-3 items-center border-b-2 border-black last:border-0 ${i % 2 ? "bg-gray-50" : "bg-white"}`}>
                    <div className="col-span-3 font-mono text-xs font-bold truncate">{r.name}</div>
                    <div className="col-span-2"><Badge variant="secondary" className="text-[10px]">{r.type}</Badge></div>
                    <div className="col-span-4 font-mono text-xs truncate" title={r.content}>{r.content}</div>
                    <div className="col-span-1 font-mono text-xs">{r.ttl}</div>
                    <div className="col-span-1 font-mono text-xs">{r.type === "MX" || r.type === "SRV" ? r.priority : "—"}</div>
                    <div className="col-span-1 flex justify-end">
                      <Button variant="ghost" size="sm" className="h-7 w-7 p-0 border-2 border-black hover:bg-danger hover:text-white" onClick={() => handleDeleteRecord(r.id)} title="Delete record">
                        <Trash2 className="w-3 h-3" />
                      </Button>
                    </div>
                  </div>
                ))
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
