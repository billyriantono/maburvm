"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useSetVMHardware } from "@/lib/hooks/use-vms"
import { hardwareChanged, hardwareProblem, hardwareSummary } from "@/lib/hardware"
import type { VM, VMHardware } from "@/types"

const selectClass =
  "h-10 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"

function Check({ id, label, hint, checked, disabled, onChange }: {
  id: string; label: string; hint?: string; checked: boolean; disabled?: boolean; onChange: (v: boolean) => void
}) {
  return (
    <label htmlFor={id} className="flex items-start gap-2 text-sm">
      <input id={id} type="checkbox" className="mt-0.5 h-4 w-4 rounded border-input" checked={checked} disabled={disabled}
        onChange={(e) => onChange(e.target.checked)} />
      <span>
        <span className="font-medium">{label}</span>
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
      </span>
    </label>
  )
}

// HardwareFields edits a hardware profile. Shared by the plan form, where it
// also offers Secure Boot and a TPM for new VMs, and a VM's own hardware.
export function HardwareFields({ idPrefix, value, onChange, forPlan }: {
  idPrefix: string; value: VMHardware; onChange: (hw: VMHardware) => void; forPlan?: boolean
}) {
  const set = <K extends keyof VMHardware>(k: K) => (v: VMHardware[K]) => onChange({ ...value, [k]: v })
  const preset = !value.cpu_model ? "" : value.cpu_model === "host-model" ? "host-model" : "custom"
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label htmlFor={`${idPrefix}-cpu`} className="mb-1 block text-xs font-medium text-muted-foreground">CPU model</label>
          <select id={`${idPrefix}-cpu`} className={selectClass} value={preset}
            onChange={(e) => set("cpu_model")(e.target.value === "custom" ? "Haswell-noTSX" : e.target.value)}>
            <option value="">The node&apos;s own CPU (fastest)</option>
            <option value="host-model">Modelled on the node&apos;s CPU</option>
            <option value="custom">A named model</option>
          </select>
        </div>
        {preset === "custom" && (
          <div>
            <label htmlFor={`${idPrefix}-cpu-name`} className="mb-1 block text-xs font-medium text-muted-foreground">Model name</label>
            <Input id={`${idPrefix}-cpu-name`} className="font-mono" value={value.cpu_model ?? ""} onChange={(e) => set("cpu_model")(e.target.value.trim())} />
          </div>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        The node&apos;s own CPU is fastest, and a VM with it moves live only to a node with the same CPU. A named model the
        nodes share (such as Haswell-noTSX or EPYC) lets VMs move live between them.
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label htmlFor={`${idPrefix}-sockets`} className="mb-1 block text-xs font-medium text-muted-foreground">Sockets</label>
          <select id={`${idPrefix}-sockets`} className={selectClass} value={value.sockets || 1} onChange={(e) => set("sockets")(Number(e.target.value) > 1 ? Number(e.target.value) : 0)}>
            {[1, 2, 4].map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </div>
        <div>
          <label htmlFor={`${idPrefix}-threads`} className="mb-1 block text-xs font-medium text-muted-foreground">Threads per core</label>
          <select id={`${idPrefix}-threads`} className={selectClass} value={value.threads || 1} onChange={(e) => set("threads")(Number(e.target.value) > 1 ? 2 : 0)}>
            <option value={1}>1</option>
            <option value={2}>2</option>
          </select>
        </div>
      </div>
      <div className="space-y-3">
        <Check id={`${idPrefix}-dedicated`} label="Dedicated cores" checked={!!value.dedicated_cpu}
          hint="Each vCPU gets a host core of its own from the node's pool of dedicated cores (set on the node's page)."
          onChange={(v) => onChange({ ...value, dedicated_cpu: v, numa: v ? value.numa : false })} />
        <Check id={`${idPrefix}-numa`} label="NUMA placement" checked={!!value.numa} disabled={!value.dedicated_cpu}
          hint="Keeps the memory on the NUMA nodes of those cores." onChange={set("numa")} />
        <Check id={`${idPrefix}-huge`} label="Huge pages" checked={!!value.hugepages}
          hint="Backs the memory with the node's reserved huge pages; the node must have enough free." onChange={set("hugepages")} />
        <Check id={`${idPrefix}-watchdog`} label="Watchdog" checked={!!value.watchdog}
          hint="Resets a guest that stops answering, once its system uses the watchdog." onChange={set("watchdog")} />
        <Check id={`${idPrefix}-restart`} label="Restart on crash" checked={!!value.restart_on_crash}
          hint="The node starts it again after a crash, waiting longer each time, and stops after three within an hour." onChange={set("restart_on_crash")} />
        {forPlan && (
          <>
            <Check id={`${idPrefix}-sb`} label="Secure Boot" checked={!!value.secure_boot}
              hint="For VMs installed from UEFI images; images that need it say so themselves." onChange={set("secure_boot")} />
            <Check id={`${idPrefix}-tpm`} label="Virtual TPM 2.0" checked={!!value.tpm}
              hint="Each VM gets an emulated TPM; nodes need swtpm." onChange={set("tpm")} />
          </>
        )}
      </div>
    </div>
  )
}

function HardwareDialog({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const save = useSetVMHardware(vm.id)
  const [value, setValue] = useState<VMHardware>({ ...vm.hardware })
  const problem = hardwareProblem(value)
  const changed = hardwareChanged(vm.hardware, value) || !!vm.hardware_note

  const submit = async () => {
    try {
      const res = await save.mutateAsync(value)
      if (!res.applied) toast.warning(res.note ?? "Saved, but the node does not have it yet.")
      else if (res.restart_required) toast.success("Saved. The VM uses it once it is powered off and on; nothing was restarted.")
      else toast.success("Saved. The VM uses it from its next start.")
      onClose()
    } catch (err) {
      toast.error(`Not saved: ${(err as Error).message}`)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle className="text-lg font-semibold">Hardware of {vm.hostname}</DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            The node saves it in the VM&apos;s definition. A running VM keeps what it booted with until it is powered off
            and on: a restart from inside the guest is not enough. Firmware, Secure Boot and the TPM stay as the VM was created.
          </DialogDescription>
        </DialogHeader>
        <div className="py-2">
          <HardwareFields idPrefix="vm-hw" value={value} onChange={setValue} />
          {problem && <p role="alert" className="mt-3 text-sm text-destructive">{problem}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button onClick={submit} disabled={!!problem || !changed || save.isPending}>
            {save.isPending && <Loader2 className="h-4 w-4 animate-spin" />}Save hardware
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// VMHardwareSection is the VM page's account of its virtual hardware. Staff
// change it here; an owner sees what the VM has, and nothing for a plain VM.
export function VMHardwareSection({ vm, isAdmin }: { vm: VM; isAdmin: boolean }) {
  const [open, setOpen] = useState(false)
  const lines = hardwareSummary(vm.hardware, vm.resources.cpu)
  const busy = vm.status === "creating" || vm.status === "deleting"
  if (!isAdmin && lines.length === 0) return null
  return (
    <section className="p-4">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <h2 className="text-xs font-medium text-muted-foreground">Hardware</h2>
        {isAdmin && <Button variant="outline" size="sm" disabled={busy} onClick={() => setOpen(true)}>Change</Button>}
      </div>
      {lines.length ? (
        <ul className="space-y-0.5">{lines.map((line) => <li key={line}>{line}</li>)}</ul>
      ) : (
        <p className="text-muted-foreground">The node&apos;s own CPU, BIOS, nothing else.</p>
      )}
      {isAdmin && vm.hardware_note && (
        <p role="status" className="mt-3 text-xs">
          <span className="font-medium text-amber-600 dark:text-amber-400">Not in force.</span>{" "}
          <span className="text-muted-foreground">{vm.hardware_note}</span>
        </p>
      )}
      {open && <HardwareDialog vm={vm} onClose={() => setOpen(false)} />}
    </section>
  )
}
