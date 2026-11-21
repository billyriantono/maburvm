"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useConfirm } from "@/components/confirm-provider"
import { useAddHostname, useRemoveHostname, useVerifyHostname, useVMNAT } from "@/lib/hooks/use-firewall-groups"
import type { VMHostname } from "@/types"

function HostnameState({ h }: { h: VMHostname }) {
  if (!h.verified_at) {
    return <span className="inline-flex items-center gap-1.5 text-sm"><span className="h-2 w-2 rounded-full bg-warning" aria-hidden="true" />Waiting for verification</span>
  }
  if (h.node_error) {
    return (
      <span className="text-sm">
        <span className="inline-flex items-center gap-1.5"><span className="h-2 w-2 rounded-full bg-destructive" aria-hidden="true" />Not served</span>
        <span className="block text-xs text-muted-foreground">{h.node_error}</span>
      </span>
    )
  }
  return <span className="inline-flex items-center gap-1.5 text-sm"><span className="h-2 w-2 rounded-full bg-success" aria-hidden="true" />Routed</span>
}

// VMNATCard is what a NAT VM's owner needs: the public address and port block
// it is reached on, and the hostnames routed to it on ports 80 and 443. It
// renders nothing for a VM with an address of its own.
export function VMNATCard({ vmId }: { vmId: string }) {
  const confirm = useConfirm()
  const { data: nat, isLoading, error, refetch } = useVMNAT(vmId)
  const add = useAddHostname(vmId)
  const verify = useVerifyHostname(vmId)
  const remove = useRemoveHostname(vmId)
  const [hostname, setHostname] = useState("")
  const [httpPort, setHTTPPort] = useState("80")
  const [httpsPort, setHTTPSPort] = useState("443")

  if (isLoading || (!error && !nat?.nat)) return null
  if (error || !nat) {
    return (
      <div className="bg-card text-card-foreground border rounded-lg p-6">
        <p role="alert" className="text-sm text-destructive">{(error as Error)?.message}</p>
        <Button size="sm" variant="outline" className="mt-3" onClick={() => refetch()}>Try again</Button>
      </div>
    )
  }
  const address = nat.public_address || "the node's address (not reported yet)"
  const remote = nat.remote_target === 3389 ? "Remote Desktop" : "SSH"
  const port = (v: string) => (v.trim() === "" ? 0 : Number(v))

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6 space-y-5">
      <div>
        <h2 className="text-lg font-semibold">Public address and ports</h2>
        <p className="text-sm text-muted-foreground mt-1">
          This VM has a private address. It is reached on the node&apos;s public address through its own range of ports, and through
          hostnames on the web ports.
        </p>
      </div>

      <dl className="grid gap-4 sm:grid-cols-3 text-sm">
        <div>
          <dt className="text-xs text-muted-foreground">Public address</dt>
          <dd className="font-mono mt-1">{nat.public_address || "Not reported by the node yet"}</dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">Your ports</dt>
          <dd className="font-mono mt-1 tabular-nums">{nat.port_start}–{nat.port_end}</dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">{remote}</dt>
          <dd className="font-mono mt-1">
            {nat.remote_port
              ? nat.remote_target === 3389
                ? `${nat.public_address || "address"}:${nat.remote_port}`
                : `ssh -p ${nat.remote_port} root@${nat.public_address || "address"}`
              : "Not forwarded. Add a forward from your range below."}
          </dd>
        </div>
      </dl>
      <p className="text-xs text-muted-foreground">Forward more ports from your range under Port forwards. Ports outside it are refused.</p>

      <section className="space-y-3">
        <div>
          <h3 className="text-sm font-semibold">Hostnames</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Web traffic for a hostname you own reaches this VM on ports 80 and 443 of {address}. Secure connections are passed
            through as they are, so the certificate lives on your VM. Your plan includes {nat.hostname_limit} hostname{nat.hostname_limit === 1 ? "" : "s"}.
          </p>
        </div>

        {nat.hostnames.length > 0 && (
          <div className="border rounded-md overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted text-xs text-muted-foreground">
                <tr><th className="text-left p-3">Hostname</th><th className="text-left p-3">To VM ports</th><th className="text-left p-3">State</th><th className="p-3"><span className="sr-only">Actions</span></th></tr>
              </thead>
              <tbody>
                {nat.hostnames.map((h) => (
                  <tr key={h.id} className="border-t align-top">
                    <td className="p-3 font-mono">
                      {h.hostname}
                      {!h.verified_at && (
                        <span className="block text-xs text-muted-foreground font-sans mt-1">
                          Prove it is yours: add a TXT record <span className="font-mono">{nat.verify_prefix}{h.hostname}</span> with
                          the value <span className="font-mono break-all">maburvm-verify={h.verify_token}</span>, or point the hostname at{" "}
                          <span className="font-mono">{nat.public_address || "the public address"}</span>. Then verify.
                        </span>
                      )}
                    </td>
                    <td className="p-3 tabular-nums">
                      {h.http_port ? `HTTP ${h.http_port}` : "HTTP off"}<br />{h.https_port ? `HTTPS ${h.https_port}` : "HTTPS off"}
                    </td>
                    <td className="p-3"><HostnameState h={h} /></td>
                    <td className="p-3 text-right whitespace-nowrap">
                      {(!h.verified_at || h.node_error) && (
                        <Button size="sm" variant="outline" disabled={verify.isPending && verify.variables === h.id} onClick={async () => {
                          try {
                            await verify.mutateAsync(h.id)
                            toast.success(h.verified_at ? "Sent to the node again. The state updates once it answers." : `${h.hostname} verified and routed`)
                          } catch (e) { toast.error((e as Error).message) }
                        }}>
                          {verify.isPending && verify.variables === h.id && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}
                          {h.verified_at ? "Try again" : "Verify"}
                        </Button>
                      )}
                      <Button size="sm" variant="ghost" onClick={() => confirm({
                        title: `Remove ${h.hostname}?`,
                        description: h.verified_at ? "Web traffic for it stops reaching this VM." : "The pending hostname is forgotten.",
                        confirmLabel: "Remove",
                        destructive: true,
                        action: () => remove.mutateAsync(h.id),
                      })}>Remove</Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {nat.hostnames.length < nat.hostname_limit && (
          <div className="flex flex-wrap items-end gap-2 p-3 border rounded-md bg-muted">
            <div>
              <label htmlFor="nat-host" className="text-xs font-medium text-muted-foreground block mb-1">Hostname</label>
              <Input id="nat-host" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="www.example.com" className="w-56 font-mono" />
            </div>
            <div>
              <label htmlFor="nat-http" className="text-xs font-medium text-muted-foreground block mb-1">HTTP to VM port</label>
              <Input id="nat-http" inputMode="numeric" value={httpPort} onChange={(e) => setHTTPPort(e.target.value)} placeholder="Off" className="w-28 font-mono" />
            </div>
            <div>
              <label htmlFor="nat-https" className="text-xs font-medium text-muted-foreground block mb-1">HTTPS to VM port</label>
              <Input id="nat-https" inputMode="numeric" value={httpsPort} onChange={(e) => setHTTPSPort(e.target.value)} placeholder="Off" className="w-28 font-mono" />
            </div>
            <Button size="sm" disabled={add.isPending || !hostname.trim()} onClick={async () => {
              try {
                await add.mutateAsync({ hostname: hostname.trim(), http_port: port(httpPort), https_port: port(httpsPort) })
                toast.success("Hostname added. Verify it to start routing.")
                setHostname("")
              } catch (e) { toast.error((e as Error).message) }
            }}>
              {add.isPending && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />}Add hostname
            </Button>
          </div>
        )}
      </section>
    </div>
  )
}
