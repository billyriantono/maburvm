import type { VMHardware } from '@/types'

// cpuModelWord names a CPU model the way the panel explains it.
export function cpuModelWord(model: string | undefined): string {
  if (!model || model === 'host-passthrough') return "The node's own CPU"
  if (model === 'host-model') return "A CPU modelled on the node's"
  return model
}

// hardwareSummary words a hardware profile, one line per thing it asks for;
// empty for a plain VM. vcpus shapes the topology line.
export function hardwareSummary(hw: VMHardware | null | undefined, vcpus?: number): string[] {
  if (!hw) return []
  const out: string[] = []
  if (hw.cpu_model) out.push(`CPU: ${cpuModelWord(hw.cpu_model)}`)
  const sockets = Math.max(hw.sockets ?? 1, 1)
  const threads = Math.max(hw.threads ?? 1, 1)
  if (sockets > 1 || threads > 1) {
    const cores = vcpus && vcpus % (sockets * threads) === 0 ? vcpus / (sockets * threads) : undefined
    out.push(cores
      ? `${sockets} socket${sockets > 1 ? 's' : ''} × ${cores} core${cores > 1 ? 's' : ''} × ${threads} thread${threads > 1 ? 's' : ''}`
      : `${sockets} socket${sockets > 1 ? 's' : ''}, ${threads} thread${threads > 1 ? 's' : ''} per core`)
  }
  if (hw.dedicated_cpu) out.push(hw.pinned_cpus ? `Dedicated cores ${hw.pinned_cpus}` : 'Dedicated cores')
  if (hw.numa) out.push('Memory on the NUMA nodes of its cores')
  if (hw.hugepages) out.push('Memory on huge pages')
  if (hw.firmware === 'uefi') out.push(hw.secure_boot ? 'UEFI with Secure Boot' : 'UEFI')
  else if (hw.secure_boot) out.push('Secure Boot (UEFI images)')
  if (hw.tpm) out.push('Virtual TPM 2.0')
  if (hw.watchdog) out.push('Watchdog resets it when it hangs')
  if (hw.restart_on_crash) out.push('Started again when it crashes')
  return out
}

// hardwareProblem is what the panel would refuse, said before saving.
export function hardwareProblem(hw: VMHardware): string | null {
  if (hw.cpu_model && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(hw.cpu_model)) {
    return 'A CPU model is a name such as host-model or Haswell-noTSX.'
  }
  if (hw.numa && !hw.dedicated_cpu) return 'NUMA placement follows dedicated cores: turn those on too.'
  if ((hw.sockets ?? 0) < 0 || (hw.sockets ?? 0) > 8) return 'Sockets are from 1 to 8.'
  if ((hw.threads ?? 0) < 0 || (hw.threads ?? 0) > 2) return 'Threads per core are 1 or 2.'
  return null
}

// hardwareChanged reports a change to the parts a VM takes at its next
// power off and on.
export function hardwareChanged(a: VMHardware | undefined, b: VMHardware): boolean {
  const keys: (keyof VMHardware)[] = ['cpu_model', 'sockets', 'threads', 'dedicated_cpu', 'numa', 'hugepages', 'watchdog', 'restart_on_crash']
  return keys.some((k) => (a?.[k] || undefined) !== (b[k] || undefined))
}
