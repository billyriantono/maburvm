"use client"

import { useEffect, useRef, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useCreateInstallCommand } from "@/lib/hooks/use-nodes"
import { useCan } from "@/lib/hooks/use-auth"
import { useCopy } from "@/lib/use-feedback"
import type { NodeInstallCommand as Command } from "@/types"

const OPTIONS: [string, string][] = [
  ["--dry-run", "Print every step and change nothing. Run it first if you are unsure."],
  ["--bridge br0", "Attach VMs to bridge br0. If it does not exist, it is created on the uplink, and the old network comes back by itself unless you confirm the new one works."],
  ["--pool-dir /data/vms", "Where VM disks go. Default /var/lib/libvirt/images, which is on the root filesystem."],
  ["--allowed-panel-ips <address>", "The panel's outgoing address. The agent then refuses connections from anywhere else."],
]

// NodeInstallCommand gives a new node its single-use install command. The
// panel refuses one for a node an agent already answers for, and so does the
// installer on a host that already runs VMs.
export function NodeInstallCommand({ nodeId, installerAllowed, createOnMount = false }: {
  nodeId: string
  installerAllowed: boolean
  createOnMount?: boolean
}) {
  const create = useCreateInstallCommand()
  const mayCreate = useCan()("node:create")
  const [cmd, setCmd] = useState<Command | null>(null)
  const { copy, copied } = useCopy()
  const started = useRef(false)

  const make = async () => {
    try {
      setCmd(await create.mutateAsync(nodeId))
    } catch (err) {
      toast.error(`No install command: ${(err as Error).message}`)
    }
  }

  useEffect(() => {
    if (createOnMount && installerAllowed && mayCreate && !started.current) {
      started.current = true
      void make()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [createOnMount, installerAllowed, mayCreate])

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <h2 className="mb-1 text-lg font-semibold text-foreground">Install the agent</h2>
      {!installerAllowed ? (
        <p className="text-sm text-muted-foreground">
          This node already runs an agent, so there is no install command for it: the installer is only for new machines.
          Update its agent from Settings, System.
        </p>
      ) : (
        <>
          <p className="mb-4 text-sm text-muted-foreground">
            Run this as root on the new machine: Debian 12 or 13, or Ubuntu 22.04 or 24.04, with no VMs on it. It installs
            KVM, libvirt and the tools VMs need, the agent, and connects the machine to this panel as this node. It
            refuses a host that already runs VMs or an agent. It needs curl (apt install curl); when you are root
            already, leave out sudo.
          </p>
          {!mayCreate ? (
            <p className="text-sm text-muted-foreground">Only staff who can add nodes can create the install command.</p>
          ) : cmd ? (
            <>
              <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
                <pre className="flex-1 overflow-x-auto rounded-md border bg-muted p-3 font-mono text-sm">{cmd.command}</pre>
                <Button variant="outline" onClick={() => copy(cmd.command, "install")}>
                  {copied("install") ? "Copied" : "Copy"}
                </Button>
              </div>
              <p className="mt-2 text-sm text-muted-foreground">
                Works once, until {new Date(cmd.expires_at).toLocaleTimeString()}. It is not shown again: if it is lost
                or expires, create a new one.
              </p>
              <h3 className="mb-2 mt-4 text-sm font-medium">Options, added at the end</h3>
              <dl className="space-y-2 text-sm">
                {OPTIONS.map(([flag, what]) => (
                  <div key={flag} className="sm:flex sm:gap-3">
                    <dt className="shrink-0 font-mono sm:w-56">{flag}</dt>
                    <dd className="text-muted-foreground">{what}</dd>
                  </div>
                ))}
              </dl>
              <p className="mt-4 text-sm text-muted-foreground">
                At the end it checks the host and prints the results; the same checks appear on this node&apos;s page once it is online.
              </p>
            </>
          ) : create.isError ? (
            <p className="text-sm text-red-700 dark:text-red-400">No install command: {create.error.message}</p>
          ) : null}
          {mayCreate && (
            <Button className="mt-4" variant={cmd ? "outline" : "default"} onClick={make} disabled={create.isPending}>
              {create.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
              {cmd ? "Create a new command" : "Create install command"}
            </Button>
          )}
          {cmd && <p className="mt-2 text-xs text-muted-foreground">A new command makes this one stop working.</p>}
        </>
      )}
    </div>
  )
}
