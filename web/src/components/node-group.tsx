"use client"

import { FormEvent, useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCan } from "@/lib/hooks/use-auth"
import { useNodeGroups } from "@/lib/hooks/use-admin-extras"
import { useUpdateNode } from "@/lib/hooks/use-nodes"
import type { Node } from "@/types"

// NodeGroupCard puts a node in a hypervisor group (NVMe, dedicated CPU, ...).
// A plan naming the group has its new VMs placed only on the group's nodes.
export function NodeGroupCard({ node }: { node: Node }) {
  const allow = useCan()
  const updateNode = useUpdateNode(node.id)
  const { data: groups } = useNodeGroups()
  const saved = node.node_group ?? ""
  const [value, setValue] = useState(saved)
  useEffect(() => setValue(saved), [saved])

  const save = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await updateNode.mutateAsync({ node_group: value.trim().toLowerCase() })
      toast.success(value.trim() ? `${node.name} is in the group ${value.trim().toLowerCase()}` : `${node.name} is in no group`)
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  const listId = `node-groups-${node.id}`
  return (
    <section className="rounded-lg border bg-card p-5 text-card-foreground">
      <h2 className="text-base font-semibold">Hypervisor group</h2>
      <p className="mt-1 max-w-prose text-sm text-muted-foreground">
        A label for nodes of one kind, such as nvme or dedicated-cpu. A plan that names the group places its new VMs only on
        nodes in it, within the region the customer chose. Leave it empty for no group.
      </p>
      <form onSubmit={save} className="mt-3 flex flex-wrap items-end gap-3">
        <div className="min-w-0 flex-1 basis-56">
          <label htmlFor={`group-${node.id}`} className="sr-only">Group</label>
          <Input id={`group-${node.id}`} list={listId} value={value} onChange={(e) => setValue(e.target.value)} placeholder="No group"
            className="font-mono" disabled={!allow("node:update")} maxLength={64} />
          <datalist id={listId}>
            {(groups ?? []).map((g) => <option key={g.name} value={g.name} />)}
          </datalist>
        </div>
        <Button type="submit" size="sm" variant={value.trim().toLowerCase() !== saved ? "default" : "outline"}
          disabled={!allow("node:update") || updateNode.isPending || value.trim().toLowerCase() === saved}>
          {updateNode.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          Save group
        </Button>
      </form>
    </section>
  )
}
