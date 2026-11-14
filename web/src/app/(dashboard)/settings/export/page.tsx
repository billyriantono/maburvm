"use client"

import { useState } from "react"
import { Download, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { downloadExport } from "@/lib/hooks/use-ops"

const EXPORTS = [
  { kind: "vms", name: "Virtual machines", holds: "One row per VM: name, state, owner, node, size, addresses and when it was created." },
  { kind: "users", name: "Users", holds: "One row per account: email, name, role, whether two-step sign-in is on, and how many VMs they have." },
  { kind: "ip-addresses", name: "IP addresses in use", holds: "One row per assigned address: which VM holds it, who owns that VM and which node it is on." },
  { kind: "audit-log", name: "Audit log", holds: "Who did what and when, newest first, up to the latest 100,000 entries." },
]

export default function ExportPage() {
  const [busy, setBusy] = useState("")

  const handle = async (kind: string) => {
    setBusy(kind)
    try {
      toast.success(`Saved ${await downloadExport(kind)}`)
    } catch (e) {
      toast.error(`The export failed: ${(e as Error).message}`)
    } finally {
      setBusy("")
    }
  }

  return (
    <div className="max-w-3xl">
      <h1 className="text-3xl font-bold tracking-tight">Export data</h1>
      <p className="text-muted-foreground text-sm mt-1">
        Download what the panel knows as CSV files, for a spreadsheet, an audit or a move to another system. Each file is made at the moment you ask for it.
      </p>

      <ul className="mt-6 rounded-lg border bg-card shadow-sm divide-y">
        {EXPORTS.map((x) => (
          <li key={x.kind} className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 p-4 sm:p-5">
            <div className="min-w-0 flex-1 basis-64">
              <p className="font-medium">{x.name}</p>
              <p className="text-sm text-muted-foreground mt-0.5">{x.holds}</p>
            </div>
            <Button variant="outline" onClick={() => handle(x.kind)} disabled={busy !== ""}>
              {busy === x.kind ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : <Download className="h-4 w-4" aria-hidden="true" />}
              Download CSV
            </Button>
          </li>
        ))}
      </ul>

      <p className="mt-4 text-sm text-muted-foreground max-w-prose">
        These files hold customer email addresses and IP addresses. Keep them somewhere only your team can reach.
        Passwords, keys and tokens are never included.
      </p>
    </div>
  )
}
