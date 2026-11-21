"use client"

import { useState } from "react"
import { Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { useAddVMNIC, useRemoveVMNIC, useSetVMIPv6Addresses, useSetVMNICMAC, useVMIPv6Addresses, useVMNICs } from "@/lib/hooks/use-networks"
import { useIPPools } from "@/lib/hooks/use-ipam"
import { useVPCs } from "@/lib/hooks/use-vpcs"
import { macProblem, parseAddressList } from "@/lib/network-input"
import type { VMNIC } from "@/types"

const MAX_CARDS = 4

// VMNICsCard lists a VM's network cards with their MACs and addresses. Staff
// add a card on another pool's network and change a stopped VM's MACs; a
// customer adds a card on one of their own private networks.
export function VMNICsCard({ vmId, nodeId, isAdmin, stopped, canEdit }: {
  vmId: string
  nodeId: string
  isAdmin: boolean
  stopped: boolean
  canEdit: boolean
}) {
  const confirm = useConfirm()
  const { data: nics, isLoading, error, refetch } = useVMNICs(vmId)
  const remove = useRemoveVMNIC(vmId)
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<VMNIC | null>(null)

  const cards = nics ?? []
  const bridges = new Set(cards.map((n) => n.bridge))

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6">
      <div className="flex items-center justify-between gap-3 mb-4">
        <div>
          <h2 className="text-lg font-semibold text-foreground">Network cards</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Each card sits on one network and holds that network&apos;s addresses. A second card joins a private network or another network.
          </p>
        </div>
        {canEdit && cards.length > 0 && cards.length < MAX_CARDS && (
          <Button type="button" size="sm" variant="outline" onClick={() => setAdding(true)}>
            <Plus className="w-4 h-4 mr-1" aria-hidden="true" />Add card
          </Button>
        )}
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground inline-flex items-center gap-2"><Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />Reading the cards from the node</p>
      ) : error ? (
        <div role="alert" className="text-sm text-destructive">
          {(error as Error).message}
          <Button size="sm" variant="outline" className="ml-3" onClick={() => refetch()}>Try again</Button>
        </div>
      ) : cards.length === 0 ? (
        <p className="text-sm text-muted-foreground">The node did not report this VM&apos;s cards. They are read again when it answers.</p>
      ) : (
        <div className="border overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted text-muted-foreground border-b font-medium text-xs">
                <th className="text-left p-3">Card</th>
                <th className="text-left p-3">MAC address</th>
                <th className="text-left p-3">Network</th>
                <th className="text-left p-3">Addresses</th>
                {canEdit && <th className="text-right p-3"></th>}
              </tr>
            </thead>
            <tbody>
              {cards.map((nic, idx) => (
                <tr key={nic.id || nic.mac_address} className={`border-t ${idx % 2 ? "bg-muted/50" : "bg-card"}`}>
                  <td className="p-3">{nic.position === 0 ? "First" : `Card ${nic.position + 1}`}</td>
                  <td className="p-3 font-mono">{nic.mac_address}</td>
                  <td className="p-3">
                    {nic.network || "—"}
                    {isAdmin && nic.bridge && <span className="block text-xs text-muted-foreground font-mono">{nic.bridge}</span>}
                  </td>
                  <td className="p-3 font-mono">{nic.addresses.length ? nic.addresses.join(", ") : "—"}</td>
                  {canEdit && (
                    <td className="p-3 text-right whitespace-nowrap">
                      {isAdmin && nic.id && (
                        <Button type="button" variant="ghost" size="sm" disabled={!stopped}
                          title={stopped ? "Change this card's MAC address" : "Stop the VM to change a MAC address"}
                          onClick={() => setEditing(nic)}>
                          Change MAC
                        </Button>
                      )}
                      {nic.position > 0 && nic.id && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                          aria-label={`Remove card ${nic.mac_address}`}
                          disabled={remove.isPending}
                          onClick={async () => {
                            let note = ""
                            const ok = await confirm({
                              title: `Remove card ${nic.mac_address}?`,
                              description: "The card is taken out of the VM and its addresses go back to their pool. The guest loses that network; nothing is restarted.",
                              confirmLabel: "Remove card",
                              destructive: true,
                              action: async () => { note = await remove.mutateAsync(nic.id) },
                            })
                            if (!ok) return
                            toast.success("Card removed", note ? { description: note } : undefined)
                          }}
                        >
                          <Trash2 className="w-4 h-4" aria-hidden="true" />
                        </Button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adding && <AddCardDialog vmId={vmId} nodeId={nodeId} isAdmin={isAdmin} taken={bridges} onClose={() => setAdding(false)} />}
      {editing && <ChangeMACDialog vmId={vmId} nic={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function AddCardDialog({ vmId, nodeId, isAdmin, taken, onClose }: {
  vmId: string
  nodeId: string
  isAdmin: boolean
  taken: Set<string>
  onClose: () => void
}) {
  const add = useAddVMNIC(vmId)
  const { data: pools } = useIPPools(isAdmin)
  const { data: vpcs } = useVPCs()
  const [choice, setChoice] = useState("")
  const [mac, setMac] = useState("")

  const poolChoices = (pools ?? []).filter((p) =>
    p.family === "ipv4" && !!p.bridge && !p.delegated_prefix_len && !taken.has(p.bridge) &&
    (!p.node_ids?.length && !p.node_id ? true : p.node_ids?.includes(nodeId) || p.node_id === nodeId))
  const vpcChoices = (vpcs ?? []).filter((v) => v.node_id === nodeId && !!v.bridge && !taken.has(v.bridge!))
  const macError = isAdmin && mac.trim() ? macProblem(mac) : ""

  const submit = async () => {
    const [kind, id] = choice.split(":")
    try {
      const { note } = await add.mutateAsync(kind === "vpc" ? { vpc_id: id } : { pool_id: id, mac_address: mac.trim() || undefined })
      toast.success("Card added", note ? { description: note } : undefined)
      onClose()
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a network card</DialogTitle>
          <DialogDescription>
            The card gets one address from the network you choose. It is added while the VM runs where its machine allows, otherwise at its next start
            from the panel. The guest is given the address the same way an address change is.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="nic-network">Network</Label>
            <select id="nic-network" className="w-full h-9 rounded border bg-background px-2 text-sm" value={choice} onChange={(e) => setChoice(e.target.value)}>
              <option value="">Choose a network</option>
              {vpcChoices.length > 0 && (
                <optgroup label="Private networks">
                  {vpcChoices.map((v) => <option key={v.id} value={`vpc:${v.id}`}>{v.name} ({v.subnet})</option>)}
                </optgroup>
              )}
              {isAdmin && poolChoices.length > 0 && (
                <optgroup label="IP pools">
                  {poolChoices.map((p) => <option key={p.id} value={`pool:${p.id}`}>{p.name} ({p.cidr}, {p.bridge})</option>)}
                </optgroup>
              )}
            </select>
            {vpcChoices.length === 0 && (!isAdmin || poolChoices.length === 0) && (
              <p className="text-xs text-muted-foreground">
                {isAdmin ? "No other network is available on this VM's node." : "You have no private network in this VM's location. Create one under Private networks first."}
              </p>
            )}
          </div>
          {isAdmin && choice.startsWith("pool:") && (
            <div className="space-y-2">
              <Label htmlFor="nic-mac">MAC address (optional)</Label>
              <Input id="nic-mac" className="font-mono" placeholder="Worked out from the VM" value={mac} onChange={(e) => setMac(e.target.value)}
                aria-invalid={!!macError} aria-describedby="nic-mac-help" />
              <p id="nic-mac-help" className={`text-xs ${macError ? "text-destructive" : "text-muted-foreground"}`}>
                {macError || "Unicast and not used by another card, for example 52:54:00:12:34:56."}
              </p>
            </div>
          )}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="button" disabled={!choice || !!macError || add.isPending} onClick={submit}>
            {add.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}Add card
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ChangeMACDialog({ vmId, nic, onClose }: { vmId: string; nic: VMNIC; onClose: () => void }) {
  const set = useSetVMNICMAC(vmId)
  const [mac, setMac] = useState(nic.mac_address)
  const problem = macProblem(mac)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Change the MAC address of {nic.position === 0 ? "the first card" : `card ${nic.position + 1}`}</DialogTitle>
          <DialogDescription>
            The VM is stopped. The card has the new MAC from its next start, and the guest&apos;s network settings are rewritten for it: on its disk
            when it uses systemd-networkd, otherwise through its guest agent once it is running.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="mac-new">MAC address</Label>
          <Input id="mac-new" className="font-mono" value={mac} onChange={(e) => setMac(e.target.value)} aria-invalid={!!problem} aria-describedby="mac-new-help" />
          <p id="mac-new-help" className={`text-xs ${problem ? "text-destructive" : "text-muted-foreground"}`}>
            {problem || "Unicast and not used by another card."}
          </p>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="button" disabled={!!problem || set.isPending} onClick={async () => {
            try {
              await set.mutateAsync({ nicId: nic.id, mac: mac.trim() })
              toast.success("MAC address changed")
              onClose()
            } catch (e) {
              toast.error((e as Error).message)
            }
          }}>
            {set.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}Change MAC
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// IPv6Addresses edits the further addresses a guest holds inside its routed
// prefix, besides its ::1.
export function IPv6Addresses({ vmId, prefix, canEdit }: { vmId: string; prefix: string; canEdit: boolean }) {
  const { data, error } = useVMIPv6Addresses(vmId, true)
  const set = useSetVMIPv6Addresses(vmId)
  const [text, setText] = useState<string | null>(null)
  if (error || !data) return null
  const current = data.extra_addresses.join("\n")
  const draft = text ?? current
  const list = parseAddressList(draft)

  return (
    <div className="mt-6 space-y-2">
      <h3 className="text-sm font-semibold">More addresses in {prefix}</h3>
      <p className="text-xs text-muted-foreground">
        Up to {data.max} addresses from the prefix besides {data.address.split("/")[0]}, one per line. The guest is given them live where it can
        be, otherwise at its next start from the panel.
      </p>
      {canEdit ? (
        <>
          <textarea
            aria-label="More IPv6 addresses"
            className="w-full min-h-24 rounded border bg-background p-2 font-mono text-sm"
            value={draft}
            onChange={(e) => setText(e.target.value)}
            placeholder={`${prefix.split("/")[0]}25`}
          />
          <div className="flex items-center gap-3">
            <Button type="button" size="sm" disabled={text === null || draft === current || list.length > data.max || set.isPending} onClick={async () => {
              try {
                await set.mutateAsync(list)
                setText(null)
                toast.success("IPv6 addresses saved")
              } catch (e) {
                toast.error((e as Error).message)
              }
            }}>
              {set.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}Save addresses
            </Button>
            {list.length > data.max && <span className="text-xs text-destructive">At most {data.max}.</span>}
          </div>
        </>
      ) : (
        <p className="font-mono text-sm">{data.extra_addresses.length ? data.extra_addresses.join(", ") : "None"}</p>
      )}
    </div>
  )
}
