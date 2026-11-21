"use client"

import { FormEvent, useState } from "react"
import Link from "next/link"
import { History, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useIPHistory, type IPAssignment } from "@/lib/hooks/use-networks"

const KIND_WORDS: Record<IPAssignment["kind"], string> = {
  direct: "Address",
  floating: "Floating address",
  ipv6_prefix: "IPv6 prefix",
}

function when(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })
}

// Who held an address when: what an abuse report needs once the address has
// been handed to someone else.
export function IPHistoryLookup() {
  const [address, setAddress] = useState("")
  const [at, setAt] = useState("")
  const [query, setQuery] = useState<{ address: string; at: string }>({ address: "", at: "" })
  const { data, isFetching, error } = useIPHistory(query.address, query.at)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    // datetime-local is the viewer's local time; the server wants an instant.
    setQuery({ address: address.trim(), at: at ? new Date(at).toISOString() : "" })
  }

  return (
    <section className="bg-card text-card-foreground border rounded-lg p-5 mb-6">
      <h2 className="text-lg font-semibold flex items-center gap-2">
        <History className="w-5 h-5" aria-hidden="true" />
        Who held an address
      </h2>
      <p className="text-sm text-muted-foreground mt-1">
        For an abuse report: give the address and, if the report names one, the moment. Every VM that held it is
        listed with its owner at the time, newest first.
      </p>
      <form onSubmit={submit} className="mt-4 flex flex-wrap items-end gap-3">
        <div className="space-y-1">
          <label htmlFor="ip-history-address" className="text-xs font-medium text-muted-foreground block">Address</label>
          <Input id="ip-history-address" className="font-mono w-64" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="203.0.113.5 or 2001:db8::1" required />
        </div>
        <div className="space-y-1">
          <label htmlFor="ip-history-at" className="text-xs font-medium text-muted-foreground block">At (your local time, optional)</label>
          <Input id="ip-history-at" type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
        </div>
        <Button type="submit" variant="outline" disabled={isFetching || !address.trim()}>
          {isFetching && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}
          Look up
        </Button>
      </form>

      {error ? (
        <p className="mt-4 text-sm text-destructive">{(error as Error).message}</p>
      ) : data && query.address ? (
        data.length === 0 ? (
          <p className="mt-4 text-sm text-muted-foreground">
            {query.at ? "Nobody held this address at that moment." : "No VM has held this address since the history began."}{" "}
            The history starts with this version of the panel; earlier holdings were recorded only if they were still in place then.
          </p>
        ) : (
          <div className="mt-4 overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left">
                  <th className="py-2 pr-4 text-xs font-semibold">Held by</th>
                  <th className="py-2 pr-4 text-xs font-semibold">VM</th>
                  <th className="py-2 pr-4 text-xs font-semibold">Kind</th>
                  <th className="py-2 pr-4 text-xs font-semibold">From</th>
                  <th className="py-2 pr-4 text-xs font-semibold">Until</th>
                </tr>
              </thead>
              <tbody>
                {data.map((r) => (
                  <tr key={r.id} className="border-b last:border-0">
                    <td className="py-2 pr-4">
                      {r.user_id ? <Link href={`/users/${r.user_id}`} className="underline underline-offset-2">{r.user_email || r.user_id}</Link> : "Unknown"}
                    </td>
                    <td className="py-2 pr-4">
                      {r.vm_id ? <Link href={`/vms/${r.vm_id}`} className="underline underline-offset-2">{r.vm_hostname || r.vm_id}</Link> : "Unknown"}
                    </td>
                    <td className="py-2 pr-4">
                      {KIND_WORDS[r.kind] ?? r.kind}
                      {r.kind === "ipv6_prefix" && <span className="block font-mono text-xs text-muted-foreground">{r.address}</span>}
                    </td>
                    <td className="py-2 pr-4 tabular-nums">
                      {r.estimated ? <>By {when(r.assigned_at)} <span className="block text-xs text-muted-foreground">Held when the history began</span></> : when(r.assigned_at)}
                    </td>
                    <td className="py-2 pr-4 tabular-nums">{r.released_at ? when(r.released_at) : "Still held"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : null}
    </section>
  )
}
